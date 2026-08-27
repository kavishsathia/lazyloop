package daemon

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"hamster/internal/control"
	"hamster/internal/hook"
	"hamster/internal/ir"
	"hamster/internal/protocol"
	"hamster/internal/store"
)

func consoleTestMachine(t *testing.T) ir.Machine {
	t.Helper()
	machine, err := control.CompileSource(`goal {
  limit attempts 3;
  verify ok = bash.check("true");
  return ok;
}`)
	if err != nil {
		t.Fatal(err)
	}
	return machine
}

func call(t *testing.T, server *Server, id, method string, params any, result any) {
	t.Helper()
	payload, _ := json.Marshal(params)
	response := server.Handle(context.Background(), protocol.Request{Version: protocol.Version, RequestID: id, Method: method, Params: payload})
	if response.Error != nil {
		t.Fatalf("%s error = %v", method, response.Error)
	}
	if result != nil {
		// Decoding into a reused value keeps fields the response omitted, so
		// start from zero every time.
		reflect.ValueOf(result).Elem().Set(reflect.Zero(reflect.TypeOf(result).Elem()))
		if err := json.Unmarshal(response.Result, result); err != nil {
			t.Fatal(err)
		}
	}
}

func TestArmedLaunchIsClaimedByTheNextPromptAndAnnounced(t *testing.T) {
	state, err := store.Open(filepath.Join(t.TempDir(), "hamster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	server := New(state)

	call(t, server, "arm", "console.arm", map[string]any{
		"console": "console-1", "workspace_root": "/repo", "machine": consoleTestMachine(t), "source": "go test",
	}, nil)

	// A turn ending must not claim: the agent should meet the loop as it
	// starts work, not as it stops.
	var decision hook.Decision
	call(t, server, "stop-1", "hook.event", hook.HookRequest{Console: "console-1", Event: hook.Event{
		SessionID: "session", EventName: "Stop", WorkspaceRoot: "/repo",
	}}, &decision)
	var view store.ConsoleObservation
	call(t, server, "observe-1", "console.observe", map[string]string{"console": "console-1"}, &view)
	if !view.Bound || view.CodexSessionID != "session" || view.Run != nil || view.Launch == nil {
		t.Fatalf("after Stop, view = %+v", view)
	}

	call(t, server, "prompt-1", "hook.event", hook.HookRequest{Console: "console-1", Event: hook.Event{
		SessionID: "session", EventName: "UserPromptSubmit", TurnID: "turn-1", WorkspaceRoot: "/repo", Prompt: "fix the tests",
	}}, &decision)
	if decision.Permission != "allow" || !decision.Tracked || decision.RunID == "" {
		t.Fatalf("claim decision = %+v", decision)
	}
	if !strings.Contains(decision.Context, "started run "+decision.RunID) || !strings.Contains(decision.Context, "lazyloop console") {
		t.Fatalf("claim context = %q", decision.Context)
	}
	// The agent did not write this loop, so the announcement must say what
	// it has to make pass.
	if !strings.Contains(decision.Context, "`true` passes") {
		t.Fatalf("claim context does not name the verifier: %q", decision.Context)
	}
	run, err := state.FindCurrentRun(context.Background(), "session", "")
	if err != nil || run.ID != decision.RunID || run.Status != "running" {
		t.Fatalf("bound run = %+v (%v)", run, err)
	}

	call(t, server, "observe-2", "console.observe", map[string]string{"console": "console-1"}, &view)
	if view.Run == nil || view.Run.ID != run.ID || view.Launch != nil {
		t.Fatalf("after claim, view = %+v", view)
	}
	if len(view.Limits) != 1 || view.Limits[0].Kind != "attempts" {
		t.Fatalf("limits = %+v", view.Limits)
	}

	// Governed tool calls see the run; nothing is claimed twice.
	toolInput, _ := json.Marshal(map[string]string{"command": "ls"})
	call(t, server, "pre-1", "hook.event", hook.HookRequest{Console: "console-1", Event: hook.Event{
		SessionID: "session", EventName: "PreToolUse", ToolName: "Bash", ToolUseID: "tool-1", ToolInput: toolInput, WorkspaceRoot: "/repo",
	}}, &decision)
	if decision.Permission != "allow" || decision.RunID != run.ID || strings.Contains(decision.Context, "lazyloop console") {
		t.Fatalf("governed decision = %+v", decision)
	}
}

func TestDisarmedLaunchIsNeverClaimed(t *testing.T) {
	state, err := store.Open(filepath.Join(t.TempDir(), "hamster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	server := New(state)
	call(t, server, "arm", "console.arm", map[string]any{"console": "console-1", "workspace_root": "/repo", "machine": consoleTestMachine(t)}, nil)
	call(t, server, "disarm", "console.disarm", map[string]string{"console": "console-1"}, nil)
	var decision hook.Decision
	call(t, server, "prompt-1", "hook.event", hook.HookRequest{Console: "console-1", Event: hook.Event{
		SessionID: "session", EventName: "UserPromptSubmit", TurnID: "turn-1", WorkspaceRoot: "/repo",
	}}, &decision)
	if decision.RunID != "" || decision.Context != "" {
		t.Fatalf("decision after disarm = %+v", decision)
	}
}
