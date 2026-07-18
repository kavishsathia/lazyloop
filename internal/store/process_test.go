package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"hamster/internal/ir"
)

func TestForkClonesCompleteRunStateAndBindsChild(t *testing.T) {
	state := testStore(t)
	parent, err := state.CreateRun(context.Background(), CreateRunParams{
		WorkspaceRoot: "/repo", CodexSessionID: "session-process", Machine: testMachine(),
	})
	if err != nil {
		t.Fatal(err)
	}
	parent.StateID = "state_at_fork"
	parent.Iteration = 7
	parent.Variables = json.RawMessage(`{"scope":"parent"}`)
	parent.Stack = []Frame{{Machine: testMachine(), ResumeState: "resume_here", Kind: "nested", Iteration: 3, Variables: json.RawMessage(`{"nested":true}`)}}
	if err := state.UpdateRun(context.Background(), "state_0", parent); err != nil {
		t.Fatal(err)
	}

	child, err := state.ForkRun(context.Background(), parent.ID, "child-agent", "spawn-tool-1")
	if err != nil {
		t.Fatal(err)
	}
	if child.ID == parent.ID || child.ProcessID == parent.ProcessID || child.ParentRunID != parent.ID {
		t.Fatalf("fork identities: parent=%#v child=%#v", parent, child)
	}
	if child.StateID != parent.StateID || child.Iteration != parent.Iteration || string(child.Variables) != string(parent.Variables) || len(child.Stack) != 1 {
		t.Fatalf("child did not clone parent execution state: %#v", child)
	}
	bound, err := state.FindCurrentRun(context.Background(), "session-process", "child-agent")
	if err != nil || bound.ID != child.ID {
		t.Fatalf("child binding = %#v, %v", bound, err)
	}
	replayed, err := state.ForkRun(context.Background(), parent.ID, "child-agent", "spawn-tool-1")
	if err != nil || replayed.ID != child.ID {
		t.Fatalf("duplicate fork = %#v, %v", replayed, err)
	}
}

func TestPendingForksBindStableChildIdentitiesInStartOrder(t *testing.T) {
	state := testStore(t)
	parent, err := state.CreateRun(context.Background(), CreateRunParams{
		WorkspaceRoot: "/repo", CodexSessionID: "session-token", Machine: testMachine(),
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := state.PrepareFork(context.Background(), parent.ID, "token-first")
	if err != nil {
		t.Fatal(err)
	}
	second, err := state.PrepareFork(context.Background(), parent.ID, "token-second")
	if err != nil {
		t.Fatal(err)
	}

	claimedFirst, err := state.ClaimPendingFork(context.Background(), "session-token", "agent-first")
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := state.ClaimPendingFork(context.Background(), "session-token", "agent-first")
	if err != nil {
		t.Fatal(err)
	}
	if claimedFirst.ID != first.ID || replayed.ID != first.ID {
		t.Fatalf("first binding = %s/%s replay=%s", first.ID, claimedFirst.ID, replayed.ID)
	}
	claimedSecond, err := state.ClaimPendingFork(context.Background(), "session-token", "agent-second")
	if err != nil || claimedSecond.ID != second.ID {
		t.Fatalf("second binding = %#v, %v", claimedSecond, err)
	}
	pending, err := state.HasPendingFork(context.Background(), "session-token")
	if err != nil || pending {
		t.Fatalf("pending = %v, error = %v", pending, err)
	}
}

func TestExecReplacesMachineAndStackButPreservesProcessIdentity(t *testing.T) {
	state := testStore(t)
	run, err := state.CreateRun(context.Background(), CreateRunParams{
		WorkspaceRoot: "/repo", CodexSessionID: "session-exec", Machine: testMachine(),
	})
	if err != nil {
		t.Fatal(err)
	}
	run.Stack = []Frame{{Machine: testMachine(), ResumeState: "old", Kind: "nested"}}
	run.Iteration = 4
	run.Result = json.RawMessage(`"old"`)
	if err := state.UpdateRun(context.Background(), run.StateID, run); err != nil {
		t.Fatal(err)
	}
	replacement := ir.Machine{ID: "machine_exec", Name: "replacement", Kind: ir.LoopGoal, InitialState: "replacement_0", States: []ir.State{{ID: "replacement_0", Kind: ir.StateAgentWork}}}
	executed, err := state.ExecRun(context.Background(), run.ID, replacement, json.RawMessage(`{"argument":42}`))
	if err != nil {
		t.Fatal(err)
	}
	if executed.ID != run.ID || executed.ProcessID != run.ProcessID || executed.Machine.ID != replacement.ID || executed.StateID != replacement.InitialState {
		t.Fatalf("exec identity or machine = %#v", executed)
	}
	if len(executed.Stack) != 0 || executed.Iteration != 0 || string(executed.Variables) != `{"argument":42}` || len(executed.Result) != 0 {
		t.Fatalf("exec did not replace execution state = %#v", executed)
	}
}

func TestWaitSuspendsParentUntilChildReturns(t *testing.T) {
	state := testStore(t)
	parent, err := state.CreateRun(context.Background(), CreateRunParams{
		WorkspaceRoot: "/repo", CodexSessionID: "session-wait", Machine: testMachine(),
	})
	if err != nil {
		t.Fatal(err)
	}
	child, err := state.ForkRun(context.Background(), parent.ID, "child-wait", "spawn-wait")
	if err != nil {
		t.Fatal(err)
	}
	waited, err := state.WaitRun(context.Background(), parent.ID, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if waited.Ready || waited.ChildStatus != "running" {
		t.Fatalf("initial wait = %#v", waited)
	}
	suspended, err := state.GetRun(context.Background(), parent.ID)
	if err != nil || suspended.Status != "waiting" {
		t.Fatalf("suspended parent = %#v, %v", suspended, err)
	}

	returned, err := state.ReturnRun(context.Background(), child.ID, json.RawMessage(`{"ok":true}`))
	if err != nil || returned.Status != "complete" {
		t.Fatalf("returned child = %#v, %v", returned, err)
	}
	resumed, err := state.GetRun(context.Background(), parent.ID)
	if err != nil || resumed.Status != "running" {
		t.Fatalf("resumed parent = %#v, %v", resumed, err)
	}
	waited, err = state.WaitRun(context.Background(), parent.ID, child.ID)
	if err != nil || !waited.Ready || string(waited.Result) != `{"ok":true}` {
		t.Fatalf("completed wait = %#v, %v", waited, err)
	}
}

func TestReturnIsIdempotentAndUnbindsChild(t *testing.T) {
	state := testStore(t)
	parent, err := state.CreateRun(context.Background(), CreateRunParams{
		WorkspaceRoot: "/repo", CodexSessionID: "session-return", Machine: testMachine(),
	})
	if err != nil {
		t.Fatal(err)
	}
	child, err := state.ForkRun(context.Background(), parent.ID, "child-return", "spawn-return")
	if err != nil {
		t.Fatal(err)
	}
	result := json.RawMessage(`"finished"`)
	first, err := state.ReturnRun(context.Background(), child.ID, result)
	if err != nil {
		t.Fatal(err)
	}
	second, err := state.ReturnRun(context.Background(), child.ID, result)
	if err != nil || second.ID != first.ID || string(second.Result) != string(result) {
		t.Fatalf("duplicate return = %#v, %v", second, err)
	}
	if _, err := state.FindCurrentRun(context.Background(), "session-return", "child-return"); err != sql.ErrNoRows {
		t.Fatalf("child remains bound: %v", err)
	}
	if _, err := state.ReturnRun(context.Background(), child.ID, json.RawMessage(`"different"`)); err == nil {
		t.Fatal("conflicting duplicate return succeeded")
	}
}
