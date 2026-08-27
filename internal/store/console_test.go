package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
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

func TestLoopIsClaimedExactlyOnce(t *testing.T) {
	state, err := Open(filepath.Join(t.TempDir(), "hamster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	ctx := context.Background()
	if _, err := state.ClaimLoop(ctx, "console-1", "session"); err != sql.ErrNoRows {
		t.Fatalf("claim with nothing armed error = %v, want no rows", err)
	}
	if _, err := state.ArmLoop(ctx, "console-1", "/repo", consoleMachine(), "go test ./...", "go test ./..."); err != nil {
		t.Fatal(err)
	}
	pending, err := state.PendingLoop(ctx, "console-1")
	if err != nil {
		t.Fatal(err)
	}
	if pending.Machine.Name != "tests" || pending.RunID != "" || pending.Label != "go test ./..." {
		t.Fatalf("pending = %+v", pending)
	}
	run, err := state.ClaimLoop(ctx, "console-1", "session")
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
	if _, err := state.ClaimLoop(ctx, "console-1", "session"); err != sql.ErrNoRows {
		t.Fatalf("second claim error = %v, want no rows", err)
	}
	if _, err := state.PendingLoop(ctx, "console-1"); err != sql.ErrNoRows {
		t.Fatalf("claimed loop still pending: %v", err)
	}
	// The claimed loop stays in the list, now following its run.
	loops, err := state.ListLoops(ctx, "/repo", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(loops) != 1 || loops[0].RunID != run.ID || loops[0].Status != "running" {
		t.Fatalf("list = %+v", loops)
	}
}

func TestListLoopsIsPersistentHistoryNewestFirst(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hamster.db")
	state, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, label := range []string{"first", "second", "third"} {
		if _, err := state.ArmLoop(ctx, "console-1", "/repo", consoleMachine(), "src "+label, label); err != nil {
			t.Fatal(err)
		}
	}
	// Arming replaces the console's own unclaimed loop, so only the last of the
	// three unclaimed survives — but a claimed one would persist.
	loops, err := state.ListLoops(ctx, "/repo", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(loops) != 1 || loops[0].Label != "third" {
		t.Fatalf("after three arms without claims, list = %+v", loops)
	}
	claimed, err := state.ClaimLoop(ctx, "console-1", "session")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.ArmLoop(ctx, "console-1", "/repo", consoleMachine(), "src next", "next"); err != nil {
		t.Fatal(err)
	}
	state.Close()

	// A fresh open of the same database keeps the history: lazyloop restarting
	// still sees the loops from before.
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	loops, err = reopened.ListLoops(ctx, "/repo", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(loops) != 2 || loops[0].Label != "next" || loops[1].Label != "third" || loops[1].RunID != claimed.ID {
		t.Fatalf("reopened list = %+v", loops)
	}
}

func TestLoadLoopReturnsSourceAndLogs(t *testing.T) {
	state, err := Open(filepath.Join(t.TempDir(), "hamster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	ctx := context.Background()
	armed, err := state.ArmLoop(ctx, "console-1", "/repo", consoleMachine(), "goal { verify ok = bash.check(\"true\"); return ok; }", "true")
	if err != nil {
		t.Fatal(err)
	}
	detail, err := state.LoadLoop(ctx, armed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Status != "armed" || detail.Source == "" || detail.Run != nil {
		t.Fatalf("armed detail = %+v", detail)
	}
	if !strings.Contains(detail.Source, "bash.check") {
		t.Fatalf("source not carried: %q", detail.Source)
	}
}

func TestDisarmDropsOnlyUnclaimedLoops(t *testing.T) {
	state, err := Open(filepath.Join(t.TempDir(), "hamster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	ctx := context.Background()
	if _, err := state.ArmLoop(ctx, "console-1", "/repo", consoleMachine(), "", "x"); err != nil {
		t.Fatal(err)
	}
	if err := state.DisarmLoop(ctx, "console-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := state.PendingLoop(ctx, "console-1"); err != sql.ErrNoRows {
		t.Fatalf("disarmed loop still pending: %v", err)
	}
	if _, err := state.ArmLoop(ctx, "console-1", "/repo", consoleMachine(), "", "y"); err != nil {
		t.Fatal(err)
	}
	if _, err := state.ClaimLoop(ctx, "console-1", "session"); err != nil {
		t.Fatal(err)
	}
	if _, err := state.ArmLoop(ctx, "console-1", "/repo", consoleMachine(), "again", "again"); err != nil {
		t.Fatal(err)
	}
	pending, err := state.PendingLoop(ctx, "console-1")
	if err != nil || pending.Source != "again" {
		t.Fatalf("re-armed loop = %+v (%v)", pending, err)
	}
	// Disarm leaves the claimed loop in the history.
	if err := state.DisarmLoop(ctx, "console-1"); err != nil {
		t.Fatal(err)
	}
	loops, err := state.ListLoops(ctx, "/repo", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(loops) != 1 || loops[0].Status != "running" {
		t.Fatalf("history after disarm = %+v", loops)
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
	if view.Bound || view.Run != nil || view.Armed != nil {
		t.Fatalf("fresh console view = %+v", view)
	}
	if _, err := state.ArmLoop(ctx, "console-1", "/repo", consoleMachine(), "", "x"); err != nil {
		t.Fatal(err)
	}
	if err := state.BindConsole(ctx, "console-1", "session", "/repo"); err != nil {
		t.Fatal(err)
	}
	view, err = state.ObserveConsole(ctx, "console-1")
	if err != nil {
		t.Fatal(err)
	}
	if !view.Bound || view.Run != nil || view.Armed == nil {
		t.Fatalf("armed, unclaimed view = %+v", view)
	}
	run, err := state.ClaimLoop(ctx, "console-1", "session")
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
	if view.Run == nil || view.Run.ID != run.ID || view.Armed != nil {
		t.Fatalf("claimed view = %+v", view)
	}
	if view.TrapCount != 1 || len(view.Traps) != 1 || view.Traps[0].Prompt != "Sure?" {
		t.Fatalf("trap view = count %d, traps %+v", view.TrapCount, view.Traps)
	}
}
