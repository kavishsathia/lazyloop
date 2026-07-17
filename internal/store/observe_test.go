package store

import (
	"context"
	"encoding/json"
	"testing"

	"hamster/internal/ir"
)

func TestObserveSessionsBuildsAgentProcessTree(t *testing.T) {
	state := testStore(t)
	parent, err := state.CreateRun(context.Background(), CreateRunParams{
		WorkspaceRoot: "/repo", CodexSessionID: "session-observe", Machine: testMachine(),
	})
	if err != nil {
		t.Fatal(err)
	}
	child, err := state.ForkRun(context.Background(), parent.ID, "agent-observe", "observe-fork")
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := state.ObserveSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].CodexSessionID != "session-observe" || sessions[0].WorkspaceRoot != "/repo" {
		t.Fatalf("sessions = %#v", sessions)
	}
	if len(sessions[0].Processes) != 2 {
		t.Fatalf("processes = %#v", sessions[0].Processes)
	}
	root, fork := sessions[0].Processes[0], sessions[0].Processes[1]
	if root.AgentID != RootAgent || root.Run == nil || root.Run.ID != parent.ID {
		t.Fatalf("root observation = %#v", root)
	}
	if fork.AgentID != "agent-observe" || fork.ParentProcessID != parent.ProcessID || fork.Run == nil || fork.Run.ID != child.ID {
		t.Fatalf("fork observation = %#v", fork)
	}
}

func TestObserveRunReportsStateEventsDurationsAndVerifications(t *testing.T) {
	state := testStore(t)
	machine := ir.Machine{
		ID: "machine_observe", Name: "observed", Kind: ir.LoopFlow, InitialState: "state_work",
		States: []ir.State{{ID: "state_work", Kind: ir.StateStep, Executor: ir.ExecutorAgent, Name: "tests"}, {ID: "state_verify", Kind: ir.StateVerify, Executor: ir.ExecutorRuntime, Name: "green"}},
	}
	run, err := state.CreateRun(context.Background(), CreateRunParams{WorkspaceRoot: "/repo", CodexSessionID: "session-detail", Machine: machine})
	if err != nil {
		t.Fatal(err)
	}
	pre := json.RawMessage(`{"session_id":"session-detail","hook_event_name":"PreToolUse","tool_name":"Bash","tool_use_id":"tool-observe","tool_input":{"command":"go test ./..."}}`)
	post := json.RawMessage(`{"session_id":"session-detail","hook_event_name":"PostToolUse","tool_name":"Bash","tool_use_id":"tool-observe","tool_input":{"command":"go test ./..."},"tool_response":{"exit_code":0}}`)
	if _, _, err := state.RecordEvent(context.Background(), "observe-pre", run.ID, "PreToolUse", pre, EventDecision{Decision: "allow"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := state.RecordEvent(context.Background(), "observe-post", run.ID, "PostToolUse", post, EventDecision{Decision: "allow"}); err != nil {
		t.Fatal(err)
	}
	verification := json.RawMessage(`[{"name":"green","command":"go test ./...","passed":true}]`)
	if err := state.SetEventVerifications(context.Background(), "observe-post", verification); err != nil {
		t.Fatal(err)
	}
	detail, err := state.ObserveRun(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Run.StateName != "tests" || len(detail.States) != 2 || len(detail.Events) != 2 {
		t.Fatalf("detail = %#v", detail)
	}
	last := detail.Events[1]
	if last.ToolName != "Bash" || last.Command != "go test ./..." || last.DurationMS < 0 || len(last.Verifications) != 1 || !last.Verifications[0].Passed {
		t.Fatalf("last event = %#v", last)
	}
}
