package daemon

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"hamster/internal/hook"
	"hamster/internal/ir"
	"hamster/internal/protocol"
	"hamster/internal/store"
)

func TestHandlePingAndCreateRun(t *testing.T) {
	state, err := store.Open(filepath.Join(t.TempDir(), "hamster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	server := New(state)
	response := server.Handle(context.Background(), protocol.Request{Version: protocol.Version, RequestID: "ping-1", Method: "ping"})
	if response.Error != nil {
		t.Fatalf("ping error = %v", response.Error)
	}

	machine := ir.Machine{ID: "machine_0", Name: "test", Kind: ir.LoopFlow, InitialState: "state_0", States: []ir.State{{ID: "state_0", Kind: ir.StateComplete}}}
	params, _ := json.Marshal(CreateRunRequest{WorkspaceRoot: "/repo", CodexSessionID: "session", Machine: machine})
	response = server.Handle(context.Background(), protocol.Request{Version: protocol.Version, RequestID: "create-1", Method: "run.create", Params: params})
	if response.Error != nil {
		t.Fatalf("run.create error = %v", response.Error)
	}
	var run store.Run
	if err := json.Unmarshal(response.Result, &run); err != nil {
		t.Fatal(err)
	}
	if run.Machine.Name != "test" || run.StateID != "state_0" {
		t.Fatalf("run = %#v", run)
	}
}

func TestRemoveStaleSocketRefusesRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hamster.sock")
	if err := os.WriteFile(path, []byte("not a socket"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeStaleSocket(path); err == nil {
		t.Fatal("removeStaleSocket() error = nil, want refusal")
	}
}

func TestHookHandlingCapturesActiveTrace(t *testing.T) {
	state, err := store.Open(filepath.Join(t.TempDir(), "hamster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	recording, err := state.StartTrace(context.Background(), "wire", "codex-cli", "0.144.5", "session")
	if err != nil {
		t.Fatal(err)
	}
	server := New(state)
	raw := json.RawMessage(`{"session_id":"session","hook_event_name":"PreToolUse","tool_name":"Bash","tool_use_id":"tool","tool_input":{"command":"true"}}`)
	event, err := hook.ParseEvent(raw)
	if err != nil {
		t.Fatal(err)
	}
	params, _ := json.Marshal(hook.HookRequest{Event: event})
	response := server.Handle(context.Background(), protocol.Request{Version: protocol.Version, RequestID: "hook", Method: "hook.event", Params: params})
	if response.Error != nil {
		t.Fatalf("hook response error = %#v", response.Error)
	}
	if _, err := state.StopTrace(context.Background(), recording.ID); err != nil {
		t.Fatal(err)
	}
	data, err := state.GetTraceData(context.Background(), recording.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Events) != 1 || string(data.Events[0].Input) != string(raw) || len(data.Events[0].Decision) == 0 {
		t.Fatalf("captured events = %#v", data.Events)
	}
}

func TestKillControlCommandEscapesActiveRun(t *testing.T) {
	state, err := store.Open(filepath.Join(t.TempDir(), "hamster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	machine := ir.Machine{ID: "machine_kill", Name: "wheel", Kind: ir.LoopGoal, InitialState: "state_work", States: []ir.State{{ID: "state_work", Kind: ir.StateAgentWork}}}
	run, err := state.CreateRun(context.Background(), store.CreateRunParams{WorkspaceRoot: "/repo", CodexSessionID: "session-kill", Machine: machine})
	if err != nil {
		t.Fatal(err)
	}
	server := New(state)
	event := hook.Event{
		SessionID: "session-kill", EventName: "PreToolUse", ToolName: "Bash", ToolUseID: "kill-tool", WorkspaceRoot: "/repo",
		ToolInput: json.RawMessage(`{"command":"hamster kill --reason 'repair runtime'"}`),
	}
	params, _ := json.Marshal(hook.HookRequest{Event: event})
	response := server.Handle(context.Background(), protocol.Request{Version: protocol.Version, RequestID: "kill-hook", Method: "hook.event", Params: params})
	if response.Error != nil {
		t.Fatalf("kill hook error = %#v", response.Error)
	}
	var decision hook.Decision
	if err := json.Unmarshal(response.Result, &decision); err != nil {
		t.Fatal(err)
	}
	if decision.Permission != "allow" || !decision.Tracked || decision.RunID != run.ID {
		t.Fatalf("kill decision = %#v", decision)
	}
	killed, err := state.GetRun(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if killed.Status != "killed" || killed.TerminationReason != "repair runtime" {
		t.Fatalf("killed run = %#v", killed)
	}
	if _, err := state.FindCurrentRun(context.Background(), "session-kill", ""); err != sql.ErrNoRows {
		t.Fatalf("current run error = %v, want sql.ErrNoRows", err)
	}

	event.ToolUseID = "after-kill"
	event.ToolInput = json.RawMessage(`{"command":"go test ./..."}`)
	params, _ = json.Marshal(hook.HookRequest{Event: event})
	response = server.Handle(context.Background(), protocol.Request{Version: protocol.Version, RequestID: "after-kill-hook", Method: "hook.event", Params: params})
	if response.Error != nil {
		t.Fatalf("post-kill hook error = %#v", response.Error)
	}
	if err := json.Unmarshal(response.Result, &decision); err != nil {
		t.Fatal(err)
	}
	if decision.Permission != "allow" || decision.Tracked {
		t.Fatalf("post-kill decision = %#v", decision)
	}
}
