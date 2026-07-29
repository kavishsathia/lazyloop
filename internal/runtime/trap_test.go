package runtime

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"hamster/internal/hook"
	"hamster/internal/store"
)

const approvalLoop = `goal {
  verify exists = bash.check("test -f marker");
  return exists;
} with flow {
  step action = bash.run("touch marker");
  trap shipped = user.approve("Does this look right?");
}`

func openTrap(t *testing.T, runner *Runner, runID string) store.Trap {
	t.Helper()
	traps, err := runner.store.ListOpenTraps(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(traps) != 1 {
		t.Fatalf("open traps = %d, want 1", len(traps))
	}
	if traps[0].RunID != runID {
		t.Fatalf("trap run = %s, want %s", traps[0].RunID, runID)
	}
	return traps[0]
}

func TestTrapSuspendsRunUntilSupervisorAnswers(t *testing.T) {
	ctx := context.Background()
	runner, run, root := runtimeRun(t, approvalLoop)
	if _, err := runner.Start(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "marker"), []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	decision, err := runner.HandleHook(ctx, postEvent("tool-1"))
	if err != nil {
		t.Fatal(err)
	}

	suspended, err := runner.store.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if suspended.Status != store.TrapStatusID {
		t.Fatalf("status = %s, want trapped", suspended.Status)
	}
	if decision.Context == "" {
		t.Fatal("a suspended run must tell the agent to stop working on the loop")
	}

	trap := openTrap(t, runner, run.ID)
	if trap.Kind != store.TrapApproval || trap.Prompt != "Does this look right?" {
		t.Fatalf("trap = %#v", trap)
	}

	// The agent must have no permitted action while the question is open.
	event := hook.Event{SessionID: "session", EventName: "PreToolUse", ToolName: "Bash", ToolUseID: "blocked"}
	denied, err := hook.NewEngine(runner.store).Decide(ctx, event)
	if err != nil {
		t.Fatal(err)
	}
	if denied.Permission != "deny" {
		t.Fatalf("permission = %s, want deny while trapped", denied.Permission)
	}
}

func TestApprovedTrapCompletesTheRun(t *testing.T) {
	ctx := context.Background()
	runner, run, root := runtimeRun(t, approvalLoop)
	if _, err := runner.Start(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "marker"), []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.HandleHook(ctx, postEvent("tool-1")); err != nil {
		t.Fatal(err)
	}
	trap := openTrap(t, runner, run.ID)

	if _, err := runner.store.ResolveTrap(ctx, trap.ID, store.TrapApprove, "looks good"); err != nil {
		t.Fatal(err)
	}
	resumed, err := runner.Start(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Status != "complete" {
		t.Fatalf("status = %s, want complete", resumed.Status)
	}
	remaining, err := runner.store.ListOpenTraps(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 0 {
		t.Fatalf("open traps after resolution = %d, want 0", len(remaining))
	}
}

func TestRejectedTrapReDerivesFromTheInitialState(t *testing.T) {
	ctx := context.Background()
	runner, run, root := runtimeRun(t, approvalLoop)
	if _, err := runner.Start(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "marker"), []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.HandleHook(ctx, postEvent("tool-1")); err != nil {
		t.Fatal(err)
	}
	trap := openTrap(t, runner, run.ID)

	if _, err := runner.store.ResolveTrap(ctx, trap.ID, store.TrapReject, "wrong shape"); err != nil {
		t.Fatal(err)
	}
	resumed, err := runner.Start(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Status != "running" {
		t.Fatalf("status = %s, want running", resumed.Status)
	}
	if resumed.StateID != resumed.Machine.InitialState {
		t.Fatalf("state = %s, want initial state %s", resumed.StateID, resumed.Machine.InitialState)
	}
	if resumed.Iteration != 1 {
		t.Fatalf("iteration = %d, want 1", resumed.Iteration)
	}
}

func TestTrapIsAskedOncePerOccurrence(t *testing.T) {
	ctx := context.Background()
	runner, run, root := runtimeRun(t, approvalLoop)
	if _, err := runner.Start(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "marker"), []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.HandleHook(ctx, postEvent("tool-1")); err != nil {
		t.Fatal(err)
	}
	first := openTrap(t, runner, run.ID)

	// Re-pumping a suspended run must re-attach to the same question rather
	// than asking the supervisor twice.
	if _, err := runner.Start(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	second := openTrap(t, runner, run.ID)
	if first.ID != second.ID {
		t.Fatalf("trap %s was re-raised as %s", first.ID, second.ID)
	}
	count, err := runner.store.CountTraps(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("raised traps = %d, want 1", count)
	}
}

func TestSelectionTrapBindsTheChosenOption(t *testing.T) {
	ctx := context.Background()
	runner, run, root := runtimeRun(t, `goal {
  verify exists = bash.check("test -f marker");
  return style;
} with flow {
  step action = bash.run("touch marker");
  trap style = user.select("Which error style?", options: ["result", "sentinel"]);
}`)
	if _, err := runner.Start(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "marker"), []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.HandleHook(ctx, postEvent("tool-1")); err != nil {
		t.Fatal(err)
	}
	trap := openTrap(t, runner, run.ID)
	if trap.Kind != store.TrapSelection || len(trap.Options) != 2 {
		t.Fatalf("trap = %#v", trap)
	}

	if _, err := runner.store.ResolveTrap(ctx, trap.ID, "sentinel", ""); err != nil {
		t.Fatal(err)
	}
	resumed, err := runner.Start(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Status != "complete" {
		t.Fatalf("status = %s, want complete", resumed.Status)
	}
	if string(resumed.Result) != `{"choice":"sentinel","note":"","resolution":"sentinel"}` {
		t.Fatalf("result = %s", resumed.Result)
	}
}

func TestSelectionTrapRejectsAnUndeclaredOption(t *testing.T) {
	ctx := context.Background()
	runner, run, root := runtimeRun(t, `goal {
  verify exists = bash.check("test -f marker");
  return style;
} with flow {
  step action = bash.run("touch marker");
  trap style = user.select("Which error style?", options: ["result", "sentinel"]);
}`)
	if _, err := runner.Start(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "marker"), []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.HandleHook(ctx, postEvent("tool-1")); err != nil {
		t.Fatal(err)
	}
	trap := openTrap(t, runner, run.ID)
	if _, err := runner.store.ResolveTrap(ctx, trap.ID, "panic", ""); err == nil {
		t.Fatal("resolving with an undeclared option must fail closed")
	}
}

const gatedGoal = `goal {
  verify exists = bash.check("test -f marker");
  trap shipped = user.approve("Is this done?");
  return shipped;
}`

// The whole point of a goal trap: the machine cannot certify itself complete.
func TestGoalTrapGatesCompletion(t *testing.T) {
	ctx := context.Background()
	runner, run, root := runtimeRun(t, gatedGoal)
	if _, err := runner.Start(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "marker"), []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.HandleHook(ctx, postEvent("tool-1")); err != nil {
		t.Fatal(err)
	}
	gated, err := runner.store.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gated.Status != store.TrapStatusID {
		t.Fatalf("status = %s; every machine check passed, so the run must stop at the human", gated.Status)
	}
	trap := openTrap(t, runner, run.ID)

	if _, err := runner.store.ResolveTrap(ctx, trap.ID, store.TrapApprove, "ship it"); err != nil {
		t.Fatal(err)
	}
	resumed, err := runner.Start(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Status != "complete" {
		t.Fatalf("status = %s, want complete", resumed.Status)
	}
}

// Work whose only standard of completion is a person's judgement — a poem, a
// design call — runs with no verifier at all. The trap is the entire ladder.
func TestJudgementOnlyGoalRunsAndCompletes(t *testing.T) {
	ctx := context.Background()
	runner, run, _ := runtimeRun(t, `goal {
  trap reads_well = user.approve("Is this poem any good?");
  return reads_well;
}`)
	if _, err := runner.Start(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.HandleHook(ctx, postEvent("tool-1")); err != nil {
		t.Fatal(err)
	}
	gated, err := runner.store.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gated.Status != store.TrapStatusID {
		t.Fatalf("status = %s, want trapped", gated.Status)
	}
	trap := openTrap(t, runner, run.ID)

	// Rejection still re-derives, so the agent gets another attempt at the poem.
	if _, err := runner.store.ResolveTrap(ctx, trap.ID, store.TrapReject, "the meter is off"); err != nil {
		t.Fatal(err)
	}
	looped, err := runner.Start(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if looped.Status != "running" || looped.Iteration != 1 {
		t.Fatalf("run = %s at iteration %d, want running at 1", looped.Status, looped.Iteration)
	}

	if _, err := runner.HandleHook(ctx, postEvent("tool-2")); err != nil {
		t.Fatal(err)
	}
	second := openTrap(t, runner, run.ID)
	if second.ID == trap.ID {
		t.Fatal("the re-derived work must be judged as a fresh question")
	}
	if _, err := runner.store.ResolveTrap(ctx, second.ID, store.TrapApprove, "much better"); err != nil {
		t.Fatal(err)
	}
	done, err := runner.Start(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != "complete" {
		t.Fatalf("status = %s, want complete", done.Status)
	}
}

// Attention is the scarcest resource in the loop, so a failing machine check
// must short-circuit the ladder before the question is ever asked.
func TestFailingVerifierNeverReachesTheHuman(t *testing.T) {
	ctx := context.Background()
	runner, run, _ := runtimeRun(t, gatedGoal)
	if _, err := runner.Start(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	// The marker is deliberately absent, so `exists` fails.
	if _, err := runner.HandleHook(ctx, postEvent("tool-1")); err != nil {
		t.Fatal(err)
	}
	traps, err := runner.store.ListOpenTraps(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(traps) != 0 {
		t.Fatalf("open traps = %d; a failing verifier must not spend supervisor attention", len(traps))
	}
	looped, err := runner.store.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if looped.Status != "running" || looped.Iteration != 1 {
		t.Fatalf("run = %s at iteration %d, want running at 1", looped.Status, looped.Iteration)
	}
}

// A rejected verdict must not be mistaken for a passing ladder entry: the
// verdict is a structured value, and plain truthiness would read it as a pass.
func TestRejectedGoalTrapDoesNotSatisfyTheLadder(t *testing.T) {
	ctx := context.Background()
	runner, run, root := runtimeRun(t, gatedGoal)
	if _, err := runner.Start(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "marker"), []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.HandleHook(ctx, postEvent("tool-1")); err != nil {
		t.Fatal(err)
	}
	trap := openTrap(t, runner, run.ID)
	if _, err := runner.store.ResolveTrap(ctx, trap.ID, store.TrapReject, "not yet"); err != nil {
		t.Fatal(err)
	}
	resumed, err := runner.Start(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Status == "complete" {
		t.Fatal("a rejected trap completed the goal")
	}
	if resumed.Status != "running" || resumed.Iteration != 1 {
		t.Fatalf("run = %s at iteration %d, want running at 1", resumed.Status, resumed.Iteration)
	}
}

// A run suspended at a trap is exactly when an operator most needs the brake,
// so kill must terminate it and clear its question from the queue.
func TestKillTerminatesATrappedRunAndClearsItsQuestion(t *testing.T) {
	ctx := context.Background()
	runner, run, root := runtimeRun(t, approvalLoop)
	if _, err := runner.Start(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "marker"), []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.HandleHook(ctx, postEvent("tool-1")); err != nil {
		t.Fatal(err)
	}
	openTrap(t, runner, run.ID)

	killed, err := runner.store.KillCurrentRun(ctx, "session", store.RootAgent, "repair the runtime")
	if err != nil {
		t.Fatal(err)
	}
	if killed.Status != "killed" {
		t.Fatalf("status = %s, want killed", killed.Status)
	}
	remaining, err := runner.store.ListOpenTraps(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 0 {
		t.Fatalf("open traps after kill = %d, want 0", len(remaining))
	}
}

func TestTrapBudgetFailsClosed(t *testing.T) {
	ctx := context.Background()
	runner, run, root := runtimeRun(t, `goal {
  limit traps 1;
  verify exists = bash.check("test -f marker");
  return exists;
} with flow {
  step action = bash.run("touch marker");
  trap shipped = user.approve("Does this look right?");
}`)
	if _, err := runner.Start(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "marker"), []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.HandleHook(ctx, postEvent("tool-1")); err != nil {
		t.Fatal(err)
	}
	trap := openTrap(t, runner, run.ID)

	// Rejection sends the flow around again; the second question would exceed
	// the declared attention budget, so the run must terminate instead.
	if _, err := runner.store.ResolveTrap(ctx, trap.ID, store.TrapReject, "again"); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Start(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.HandleHook(ctx, postEvent("tool-2")); err != nil {
		t.Fatal(err)
	}
	final, err := runner.store.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Status != "limit_exceeded" {
		t.Fatalf("status = %s, want limit_exceeded", final.Status)
	}
	if string(final.Result) != `{"limit":"traps","value":1}` {
		t.Fatalf("result = %s", final.Result)
	}
}
