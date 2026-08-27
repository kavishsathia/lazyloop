package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"hamster/internal/ir"
)

func consoleMachine() ir.Machine {
	return ir.Machine{ID: "machine_0", Name: "tests", Kind: ir.LoopGoal, InitialState: "state_0",
		States: []ir.State{{ID: "state_0", Kind: ir.StateAgentWork, Executor: ir.ExecutorAgent, Name: "work", Next: "state_1"}, {ID: "state_1", Kind: ir.StateComplete}}}
}

func TestConsoleBindingFollowsTheLatestSession(t *testing.T) {
	state, err := Open(filepath.Join(t.TempDir(), "hamster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	ctx := context.Background()
	if _, err := state.FindConsole(ctx, "console-1"); err != sql.ErrNoRows {
		t.Fatalf("unbound console error = %v, want no rows", err)
	}
	if err := state.BindConsole(ctx, "console-1", "session-a", "/repo"); err != nil {
		t.Fatal(err)
	}
	if err := state.BindConsole(ctx, "console-1", "session-b", "/repo"); err != nil {
		t.Fatal(err)
	}
	binding, err := state.FindConsole(ctx, "console-1")
	if err != nil {
		t.Fatal(err)
	}
	if binding.CodexSessionID != "session-b" || binding.WorkspaceRoot != "/repo" {
		t.Fatalf("binding = %+v", binding)
	}
}

func TestLaunchIsClaimedExactlyOnce(t *testing.T) {
	state, err := Open(filepath.Join(t.TempDir(), "hamster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	ctx := context.Background()
	if _, err := state.ClaimLaunch(ctx, "console-1", "session"); err != sql.ErrNoRows {
		t.Fatalf("claim with nothing armed error = %v, want no rows", err)
	}
	if _, err := state.ArmLaunch(ctx, "console-1", "/repo", consoleMachine(), `goal { verify ok = true; return ok; }`); err != nil {
		t.Fatal(err)
	}
	pending, err := state.PendingLaunch(ctx, "console-1")
	if err != nil {
		t.Fatal(err)
	}
	if pending.Machine.Name != "tests" || pending.ClaimedRunID != "" {
		t.Fatalf("pending = %+v", pending)
	}
	run, err := state.ClaimLaunch(ctx, "console-1", "session")
	if err != nil {
		t.Fatal(err)
	}
	if run.WorkspaceRoot != "/repo" || run.Status != "running" {
		t.Fatalf("claimed run = %+v", run)
	}
	bound, err := state.FindCurrentRun(ctx, "session", "")
	if err != nil || bound.ID != run.ID {
		t.Fatalf("root agent bound to %v (%v), want %s", bound, err, run.ID)
	}
	if _, err := state.ClaimLaunch(ctx, "console-1", "session"); err != sql.ErrNoRows {
		t.Fatalf("second claim error = %v, want no rows", err)
	}
	if _, err := state.PendingLaunch(ctx, "console-1"); err != sql.ErrNoRows {
		t.Fatalf("claimed launch still pending: %v", err)
	}
}

func TestDisarmDropsOnlyUnclaimedLaunches(t *testing.T) {
	state, err := Open(filepath.Join(t.TempDir(), "hamster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	ctx := context.Background()
	if _, err := state.ArmLaunch(ctx, "console-1", "/repo", consoleMachine(), ""); err != nil {
		t.Fatal(err)
	}
	if err := state.DisarmLaunch(ctx, "console-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := state.PendingLaunch(ctx, "console-1"); err != sql.ErrNoRows {
		t.Fatalf("disarmed launch still pending: %v", err)
	}
	// Re-arming after a claim starts a fresh launch rather than reviving the
	// claimed one.
	if _, err := state.ArmLaunch(ctx, "console-1", "/repo", consoleMachine(), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := state.ClaimLaunch(ctx, "console-1", "session"); err != nil {
		t.Fatal(err)
	}
	if _, err := state.ArmLaunch(ctx, "console-1", "/repo", consoleMachine(), "again"); err != nil {
		t.Fatal(err)
	}
	pending, err := state.PendingLaunch(ctx, "console-1")
	if err != nil || pending.Source != "again" {
		t.Fatalf("re-armed launch = %+v (%v)", pending, err)
	}
}

func TestObserveConsoleReportsRunTrapsAndLaunch(t *testing.T) {
	state, err := Open(filepath.Join(t.TempDir(), "hamster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	ctx := context.Background()
	view, err := state.ObserveConsole(ctx, "console-1")
	if err != nil {
		t.Fatal(err)
	}
	if view.Bound || view.Run != nil || view.Launch != nil {
		t.Fatalf("fresh console view = %+v", view)
	}
	if _, err := state.ArmLaunch(ctx, "console-1", "/repo", consoleMachine(), ""); err != nil {
		t.Fatal(err)
	}
	if err := state.BindConsole(ctx, "console-1", "session", "/repo"); err != nil {
		t.Fatal(err)
	}
	view, err = state.ObserveConsole(ctx, "console-1")
	if err != nil {
		t.Fatal(err)
	}
	if !view.Bound || view.Run != nil || view.Launch == nil {
		t.Fatalf("armed, unclaimed view = %+v", view)
	}
	run, err := state.ClaimLaunch(ctx, "console-1", "session")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.RaiseTrap(ctx, RaiseTrapParams{RunID: run.ID, StateID: "state_0", Iteration: 0, Name: "check", Kind: TrapApproval, Prompt: "Sure?"}); err != nil {
		t.Fatal(err)
	}
	view, err = state.ObserveConsole(ctx, "console-1")
	if err != nil {
		t.Fatal(err)
	}
	if view.Run == nil || view.Run.ID != run.ID || view.Launch != nil {
		t.Fatalf("claimed view = %+v", view)
	}
	if view.TrapCount != 1 || len(view.Traps) != 1 || view.Traps[0].Prompt != "Sure?" {
		t.Fatalf("trap view = count %d, traps %+v", view.TrapCount, view.Traps)
	}
}
