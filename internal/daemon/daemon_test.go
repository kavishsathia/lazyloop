package daemon

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hamster/internal/compiler"
	"hamster/internal/hook"
	"hamster/internal/ir"
	"hamster/internal/language"
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

func TestDashboardRejectsNonLoopbackAddress(t *testing.T) {
	for _, address := range []string{"0.0.0.0:7331", "[::]:7331", "example.com:7331"} {
		if err := validateDashboardAddress(address); err == nil {
			t.Fatalf("validateDashboardAddress(%q) error = nil", address)
		}
	}
	for _, address := range []string{"127.0.0.1:7331", "[::1]:0", "localhost:7331"} {
		if err := validateDashboardAddress(address); err != nil {
			t.Fatalf("validateDashboardAddress(%q) error = %v", address, err)
		}
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

func TestForkLifecycleInjectsTokenQuarantinesAndAttachesExactly(t *testing.T) {
	state, err := store.Open(filepath.Join(t.TempDir(), "hamster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	machine := ir.Machine{ID: "machine_fork", Name: "parallel", Kind: ir.LoopGoal, InitialState: "state_work", States: []ir.State{{ID: "state_work", Kind: ir.StateAgentWork}}}
	parent, err := state.CreateRun(context.Background(), store.CreateRunParams{WorkspaceRoot: "/repo", CodexSessionID: "session-fork", Machine: machine})
	if err != nil {
		t.Fatal(err)
	}
	server := New(state)
	spawn := hook.Event{
		SessionID: "session-fork", EventName: "PreToolUse", ToolName: "collaborationspawn_agent", ToolUseID: "spawn-1", WorkspaceRoot: "/repo",
		ToolInput: json.RawMessage(`{"task_name":"child","message":"do the work"}`),
	}
	params, _ := json.Marshal(hook.HookRequest{Event: spawn})
	response := server.Handle(context.Background(), protocol.Request{Version: protocol.Version, RequestID: "spawn-hook", Method: "hook.event", Params: params})
	if response.Error != nil {
		t.Fatalf("spawn hook error = %#v", response.Error)
	}
	var spawnDecision hook.Decision
	if err := json.Unmarshal(response.Result, &spawnDecision); err != nil {
		t.Fatal(err)
	}
	token := forkToken(spawn.RequestID())
	if !spawnDecision.Tracked || !strings.Contains(string(spawnDecision.UpdatedInput), "hamster attach "+token) {
		t.Fatalf("spawn decision = %#v", spawnDecision)
	}

	started := hook.Event{SessionID: "session-fork", AgentID: "agent-child", EventName: "SubagentStart", WorkspaceRoot: "/repo", TurnID: "turn-child"}
	params, _ = json.Marshal(hook.HookRequest{Event: started})
	startParams := append(json.RawMessage(nil), params...)
	response = server.Handle(context.Background(), protocol.Request{Version: protocol.Version, RequestID: "start-hook", Method: "hook.event", Params: params})
	if response.Error != nil {
		t.Fatalf("subagent start error = %#v", response.Error)
	}
	if _, err := state.FindCurrentRun(context.Background(), "session-fork", "agent-child"); err != sql.ErrNoRows {
		t.Fatalf("SubagentStart guessed a fork binding: %v", err)
	}

	blocked := hook.Event{SessionID: "session-fork", AgentID: "agent-child", EventName: "PreToolUse", ToolName: "Bash", ToolUseID: "before-attach", ToolInput: json.RawMessage(`{"command":"git status"}`)}
	params, _ = json.Marshal(hook.HookRequest{Event: blocked})
	response = server.Handle(context.Background(), protocol.Request{Version: protocol.Version, RequestID: "blocked-hook", Method: "hook.event", Params: params})
	var blockedDecision hook.Decision
	if response.Error != nil || json.Unmarshal(response.Result, &blockedDecision) != nil || blockedDecision.Permission != "deny" || !strings.Contains(blockedDecision.Remediation, "hamster attach") {
		t.Fatalf("quarantine decision = %#v, error = %#v", blockedDecision, response.Error)
	}

	attachInput, _ := json.Marshal(map[string]string{"command": "hamster attach " + token})
	attach := hook.Event{SessionID: "session-fork", AgentID: "agent-child", EventName: "PreToolUse", ToolName: "Bash", ToolUseID: "attach", ToolInput: attachInput}
	params, _ = json.Marshal(hook.HookRequest{Event: attach})
	response = server.Handle(context.Background(), protocol.Request{Version: protocol.Version, RequestID: "attach-hook", Method: "hook.event", Params: params})
	if response.Error != nil {
		t.Fatalf("attach hook error = %#v", response.Error)
	}
	child, err := state.FindCurrentRun(context.Background(), "session-fork", "agent-child")
	if err != nil {
		t.Fatal(err)
	}
	if child.ID == parent.ID || child.ParentRunID != parent.ID || child.StateID != parent.StateID {
		t.Fatalf("forked child = %#v, parent = %#v", child, parent)
	}

	response = server.Handle(context.Background(), protocol.Request{Version: protocol.Version, RequestID: "start-hook-replay", Method: "hook.event", Params: startParams})
	if response.Error != nil {
		t.Fatalf("replayed subagent start error = %#v", response.Error)
	}
	replayed, err := state.FindCurrentRun(context.Background(), "session-fork", "agent-child")
	if err != nil || replayed.ID != child.ID {
		t.Fatalf("replayed child binding = %#v, %v", replayed, err)
	}
}

func TestLanguageForkPostStoresParentHandleAndEntersRuntimeWait(t *testing.T) {
	state, err := store.Open(filepath.Join(t.TempDir(), "hamster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	program, diagnostics := language.Parse(`goal {
  verify done = true;
  return child.result;
} with flow {
  step child = fork;
  if child.child { exec child-work; }
  wait child;
}`)
	if len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	compiled, diagnostics := compiler.Compile(program)
	if len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	workspace := t.TempDir()
	scripts := filepath.Join(workspace, ".hamster", "scripts")
	if err := os.MkdirAll(scripts, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scripts, "child-work.hmstr"), []byte(`goal { verify ready = true; return "child"; }`), 0o600); err != nil {
		t.Fatal(err)
	}
	parent, err := state.CreateRun(context.Background(), store.CreateRunParams{WorkspaceRoot: workspace, CodexSessionID: "language-fork", Machine: compiled.Machines[0]})
	if err != nil {
		t.Fatal(err)
	}
	server := New(state)
	if _, err := server.runner.Start(context.Background(), parent.ID); err != nil {
		t.Fatal(err)
	}
	spawn := hook.Event{SessionID: "language-fork", EventName: "PreToolUse", ToolName: "collaborationspawn_agent", ToolUseID: "spawn", ToolInput: json.RawMessage(`{"message":"work","task_name":"child"}`)}
	params, _ := json.Marshal(hook.HookRequest{Event: spawn})
	response := server.Handle(context.Background(), protocol.Request{Version: protocol.Version, RequestID: "fork-pre", Method: "hook.event", Params: params})
	if response.Error != nil {
		t.Fatal(response.Error)
	}
	post := spawn
	post.EventName = "PostToolUse"
	post.ToolResponse = json.RawMessage(`{"success":true}`)
	params, _ = json.Marshal(hook.HookRequest{Event: post})
	response = server.Handle(context.Background(), protocol.Request{Version: protocol.Version, RequestID: "fork-post", Method: "hook.event", Params: params})
	if response.Error != nil {
		t.Fatal(response.Error)
	}
	current, err := state.GetRun(context.Background(), parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	var variables map[string]any
	if json.Unmarshal(current.Variables, &variables) != nil {
		t.Fatalf("variables = %s", current.Variables)
	}
	handle, _ := variables["child"].(map[string]any)
	if current.Status != "waiting" || handle["child"] != false || handle["parent"] != true || handle["run_id"] == "" {
		t.Fatalf("parent after fork = %#v, variables=%#v", current, variables)
	}
	attach := hook.Event{SessionID: "language-fork", AgentID: "agent-child", EventName: "PreToolUse", ToolName: "Bash", ToolUseID: "attach", WorkspaceRoot: workspace}
	attach.ToolInput, _ = json.Marshal(map[string]string{"command": "hamster attach " + forkToken(spawn.RequestID())})
	params, _ = json.Marshal(hook.HookRequest{Event: attach})
	response = server.Handle(context.Background(), protocol.Request{Version: protocol.Version, RequestID: "fork-attach", Method: "hook.event", Params: params})
	if response.Error != nil {
		t.Fatal(response.Error)
	}
	child, err := state.FindCurrentRun(context.Background(), "language-fork", "agent-child")
	if err != nil {
		t.Fatal(err)
	}
	childState := child.Machine.States[0]
	if child.ParentRunID != parent.ID || child.Machine.Imperative || childState.Kind != ir.StateAgentWork {
		t.Fatalf("attached child did not auto-exec replacement: %#v", child)
	}
}

func TestAgentCannotBypassRuntimeOwnedExecAndWait(t *testing.T) {
	state, err := store.Open(filepath.Join(t.TempDir(), "hamster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	machine := ir.Machine{ID: "machine_control", Name: "control", Kind: ir.LoopGoal, InitialState: "state_work", States: []ir.State{{ID: "state_work", Kind: ir.StateAgentWork}}}
	_, err = state.CreateRun(context.Background(), store.CreateRunParams{WorkspaceRoot: "/repo", CodexSessionID: "session-exec-control", Machine: machine})
	if err != nil {
		t.Fatal(err)
	}
	server := New(state)
	execEvent := hook.Event{
		SessionID: "session-exec-control", EventName: "PreToolUse", ToolName: "Bash", ToolUseID: "exec-tool", WorkspaceRoot: "/repo",
		ToolInput: json.RawMessage(`{"command":"hamster exec --text 'goal { verify passed = bash.check(\"true\"); return passed; } with flow { step command = bash.run(\"true\"); }'"}`),
	}
	params, _ := json.Marshal(hook.HookRequest{Event: execEvent})
	response := server.Handle(context.Background(), protocol.Request{Version: protocol.Version, RequestID: "exec-hook", Method: "hook.event", Params: params})
	if response.Error != nil {
		t.Fatalf("exec hook error = %#v", response.Error)
	}
	var decision hook.Decision
	if json.Unmarshal(response.Result, &decision) != nil || decision.Permission != "deny" || !strings.Contains(decision.Reason, "runtime-owned") {
		t.Fatalf("exec decision = %#v", decision)
	}

	parent, err := state.CreateRun(context.Background(), store.CreateRunParams{WorkspaceRoot: "/repo", CodexSessionID: "session-wait-control", Machine: machine})
	if err != nil {
		t.Fatal(err)
	}
	child, err := state.ForkRun(context.Background(), parent.ID, "wait-child", "wait-child-fork")
	if err != nil {
		t.Fatal(err)
	}
	waitEvent := hook.Event{
		SessionID: "session-wait-control", EventName: "PreToolUse", ToolName: "Bash", ToolUseID: "wait-tool", WorkspaceRoot: "/repo",
		ToolInput: json.RawMessage(`{"command":"hamster wait ` + child.ID + `"}`),
	}
	params, _ = json.Marshal(hook.HookRequest{Event: waitEvent})
	response = server.Handle(context.Background(), protocol.Request{Version: protocol.Version, RequestID: "wait-hook", Method: "hook.event", Params: params})
	if response.Error != nil {
		t.Fatalf("wait hook error = %#v", response.Error)
	}
	if json.Unmarshal(response.Result, &decision) != nil || decision.Permission != "deny" || !strings.Contains(decision.Reason, "runtime-owned") {
		t.Fatalf("wait decision = %#v", decision)
	}
}
