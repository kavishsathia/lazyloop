package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hamster/internal/compiler"
	"hamster/internal/hook"
	"hamster/internal/ir"
	"hamster/internal/language"
	"hamster/internal/store"
)

func runtimeRun(t *testing.T, source string) (*Runner, *store.Run, string) {
	t.Helper()
	program, parseDiagnostics := language.Parse(source)
	if len(parseDiagnostics) > 0 {
		t.Fatal(parseDiagnostics)
	}
	compiled, compileDiagnostics := compiler.Compile(program)
	if len(compileDiagnostics) > 0 {
		t.Fatal(compileDiagnostics)
	}
	root := t.TempDir()
	state, err := store.Open(filepath.Join(t.TempDir(), "hamster.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { state.Close() })
	run, err := state.CreateRun(context.Background(), store.CreateRunParams{
		WorkspaceRoot: root, CodexSessionID: "session", Machine: compiled.Machines[0],
	})
	if err != nil {
		t.Fatal(err)
	}
	return New(state), run, root
}

func postEvent(id string) hook.Event {
	response, _ := json.Marshal(map[string]any{"exit_code": 0})
	event := hook.Event{SessionID: "session", EventName: "PostToolUse", ToolName: "Bash", ToolUseID: id, ToolResponse: response}
	event.Raw = hook.MarshalEvent(event)
	return event
}

func signalEvent(id string) hook.Event {
	event := hook.Event{SessionID: "session", EventName: "PreToolUse", ToolName: "Bash", ToolUseID: id}
	event.Raw = hook.MarshalEvent(event)
	return event
}

func TestFlowAdvancesThenRunsIndependentVerifier(t *testing.T) {
	runner, run, root := runtimeRun(t, `goal {
  verify exists = bash.check("test -f marker");
  return exists;
} with flow {
  step action = bash.run("touch marker");
}`)
	started, err := runner.Start(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if started.StateID != "state_0" {
		t.Fatalf("start state = %s", started.StateID)
	}
	if err := os.WriteFile(filepath.Join(root, "marker"), []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.HandleHook(context.Background(), postEvent("tool-1")); err != nil {
		t.Fatal(err)
	}
	finished, err := runner.store.GetRun(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.Status != "complete" || string(finished.Result) != "true" {
		t.Fatalf("finished = %#v, result = %s", finished, finished.Result)
	}
}

func TestAttachedFlowProbesOnlyAfterCompleteCycle(t *testing.T) {
	defaultRunner, run, _ := runtimeRun(t, `goal {
  verify light = bash.check("light");
  verify heavy = bash.check("heavy");
} with flow {
  step first = bash.run("first");
  step second = bash.run("second");
}`)
	verifier := NewReplayVerifier([]VerificationResult{{Passed: false}})
	runner := NewWithDependencies(defaultRunner.store, NewFixedClock(time.Now()), verifier)
	started, err := runner.Start(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.HandleHook(context.Background(), postEvent("first")); err != nil {
		t.Fatal(err)
	}
	afterFirst, err := runner.store.GetRun(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(verifier.Calls()) != 0 || afterFirst.StateID == started.StateID {
		t.Fatalf("first step probed or failed to advance: state=%s calls=%#v", afterFirst.StateID, verifier.Calls())
	}
	decision, err := runner.HandleHook(context.Background(), postEvent("second"))
	if err != nil {
		t.Fatal(err)
	}
	current, err := runner.store.GetRun(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.StateID != started.StateID || current.Iteration != 1 || !strings.Contains(decision.Context, "at light") {
		t.Fatalf("cycle failure = %#v, decision=%#v", current, decision)
	}
	calls := verifier.Calls()
	if len(calls) != 1 || calls[0].Command != "light" {
		t.Fatalf("verifiers did not short-circuit: %#v", calls)
	}
}

func TestTwoForkedChildrenExecAutomaticallyAndWaitWakesParent(t *testing.T) {
	runner, parent, _ := runtimeRun(t, `goal {
  verify alpha_returned = alpha.result == "alpha";
  verify beta_returned = beta.result == "beta";
  return [alpha.result, beta.result];
} with flow {
  step alpha = fork;
  if alpha.child {
    exec alpha-work;
  }
  step beta = fork;
  if beta.child {
    exec beta-work;
  }
  wait [alpha, beta];
}`)
	runner.SetExecResolver(func(script, _ string) (ir.Machine, error) {
		if script != "alpha-work" && script != "beta-work" {
			t.Fatalf("exec script = %q", script)
		}
		result := strings.TrimSuffix(script, "-work")
		program, diagnostics := language.Parse(fmt.Sprintf(`goal {
  verify finished = true;
  return %q;
}`, result))
		if len(diagnostics) != 0 {
			t.Fatal(diagnostics)
		}
		compiled, diagnostics := compiler.Compile(program)
		if len(diagnostics) != 0 {
			t.Fatal(diagnostics)
		}
		return compiled.Machines[0], nil
	})
	started, err := runner.Start(context.Background(), parent.ID)
	if err != nil || started.Machine.States[0].Kind != ir.StateFork {
		t.Fatalf("parent start = %#v, %v", started, err)
	}
	for index, key := range []string{"fork-alpha", "fork-beta"} {
		child, err := runner.store.PrepareFork(context.Background(), parent.ID, key)
		if err != nil {
			t.Fatal(err)
		}
		response, _ := json.Marshal(map[string]any{"success": true, "hamster_child_run_id": child.ID})
		spawnPost := hook.Event{SessionID: "session", EventName: "PostToolUse", ToolName: "collaborationspawn_agent", ToolUseID: fmt.Sprintf("spawn-%d", index), ToolResponse: response}
		spawnPost.Raw = hook.MarshalEvent(spawnPost)
		if _, err := runner.HandleHook(context.Background(), spawnPost); err != nil {
			t.Fatal(err)
		}
	}
	waiting, _ := runner.store.GetRun(context.Background(), parent.ID)
	if waiting.Status != "waiting" || waiting.Machine.States[stateIndex(waiting.Machine, waiting.StateID)].Kind != ir.StateWait {
		t.Fatalf("parent did not wait automatically: %#v", waiting)
	}
	for index := range []string{"fork-alpha", "fork-beta"} {
		agentID := fmt.Sprintf("agent-%d", index)
		child, err := runner.store.ClaimPendingFork(context.Background(), "session", agentID)
		if err != nil {
			t.Fatal(err)
		}
		child, err = runner.ResumeForkChild(context.Background(), child.ID)
		if err != nil {
			t.Fatal(err)
		}
		childState := child.Machine.States[stateIndex(child.Machine, child.StateID)]
		if childState.Kind != ir.StateAgentWork || len(child.Stack) != 0 || child.Machine.Imperative {
			t.Fatalf("child exec did not replace complete machine: %#v", child)
		}
		childPost := postEvent(fmt.Sprintf("child-%d-finished", index))
		childPost.AgentID = agentID
		childPost.Raw = hook.MarshalEvent(childPost)
		if _, err := runner.HandleHook(context.Background(), childPost); err != nil {
			t.Fatal(err)
		}
		if index == 0 {
			stillWaiting, _ := runner.store.GetRun(context.Background(), parent.ID)
			if stillWaiting.Status != "waiting" {
				t.Fatalf("parent woke before both children: %#v", stillWaiting)
			}
		}
	}
	finished, err := runner.store.GetRun(context.Background(), parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.Status != "complete" || string(finished.Result) != `["alpha","beta"]` {
		t.Fatalf("parent result = %#v", finished)
	}
}

func stateIndex(machine ir.Machine, stateID string) int {
	for index, state := range machine.States {
		if state.ID == stateID {
			return index
		}
	}
	return -1
}

func TestAttemptLimitStartsAtDeclaration(t *testing.T) {
	runner, run, _ := runtimeRun(t, `goal {
  limit attempts 2;
  verify never = false;
  return never;
} with flow {
  step action = bash.run("true");
}`)
	if _, err := runner.Start(context.Background(), run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.HandleHook(context.Background(), postEvent("attempt-1")); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.HandleHook(context.Background(), postEvent("attempt-2")); err != nil {
		t.Fatal(err)
	}
	finished, _ := runner.store.GetRun(context.Background(), run.ID)
	if finished.Status != "limit_exceeded" || finished.Iteration != 2 {
		t.Fatalf("finished status = %s, iteration = %d", finished.Status, finished.Iteration)
	}
	if _, err := runner.store.FindCurrentRun(context.Background(), "session", ""); err != sql.ErrNoRows {
		t.Fatalf("limit-exceeded run remains bound: %v", err)
	}
}

func TestForeachCollectsInnerLoopReturns(t *testing.T) {
	runner, run, _ := runtimeRun(t, `goal {
  verify complete = true;
  return values;
} with flow {
  step values = foreach item in [true, true] {
    goal {
      verify ready = item;
      return item;
    }
  };
}`)
	started, err := runner.Start(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(started.Stack) != 2 {
		t.Fatalf("stack depth = %d, want outer flow and foreach", len(started.Stack))
	}
	if _, err := runner.SignalGoalAchieved(context.Background(), signalEvent("item-1")); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.SignalGoalAchieved(context.Background(), signalEvent("item-2")); err != nil {
		t.Fatal(err)
	}
	finished, _ := runner.store.GetRun(context.Background(), run.ID)
	if finished.Status != "complete" || string(finished.Result) != `[true,true]` {
		t.Fatalf("status = %s, result = %s", finished.Status, finished.Result)
	}
}

func TestDeclarativeGoalProbesAfterEveryToolCall(t *testing.T) {
	runner, run, _ := runtimeRun(t, `goal {
  verify ready = true;
  return ready;
}`)
	_, err := runner.Start(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.HandleHook(context.Background(), postEvent("ordinary-tool")); err != nil {
		t.Fatal(err)
	}
	current, err := runner.store.GetRun(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != "complete" || string(current.Result) != "true" {
		t.Fatalf("ordinary tool did not complete goal: %#v", current)
	}
	if observations := runner.TakeObservations(run.ID); len(observations) != 1 || !observations[0].Passed {
		t.Fatalf("ordinary tool observations: %#v", observations)
	}
}

func TestConsecutiveGoalsStopAtEachAgentWorkBoundary(t *testing.T) {
	defaultRunner, run, _ := runtimeRun(t, `goal {
  verify outer = bash.check("outer");
  return outer;
} with flow {
  step first = goal {
    verify first_done = bash.check("first");
    return first_done;
  };
  step second = goal {
    verify second_done = bash.check("second");
    return second_done;
  };
}`)
	verifier := NewReplayVerifier([]VerificationResult{{Passed: true}, {Passed: true}, {Passed: true}})
	runner := NewWithDependencies(defaultRunner.store, NewFixedClock(time.Now()), verifier)
	if _, err := runner.Start(context.Background(), run.ID); err != nil {
		t.Fatal(err)
	}

	decision, err := runner.HandleHook(context.Background(), postEvent("first-goal"))
	if err != nil {
		t.Fatal(err)
	}
	current, err := runner.store.GetRun(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != "running" || len(current.Stack) != 1 || current.Machine.States[0].Name != "work" {
		t.Fatalf("first goal did not stop at second goal boundary: %#v", current)
	}
	if calls := verifier.Calls(); len(calls) != 1 || calls[0].Command != "first" {
		t.Fatalf("first hook crossed into second goal: %#v", calls)
	}
	if !strings.Contains(decision.Context, "advanced") {
		t.Fatalf("first goal transition context = %q", decision.Context)
	}

	if _, err := runner.HandleHook(context.Background(), postEvent("second-goal")); err != nil {
		t.Fatal(err)
	}
	finished, err := runner.store.GetRun(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.Status != "complete" {
		t.Fatalf("second goal and outer cycle did not complete: %#v", finished)
	}
	calls := verifier.Calls()
	if len(calls) != 3 || calls[1].Command != "second" || calls[2].Command != "outer" {
		t.Fatalf("goal boundary verifier order = %#v", calls)
	}
}

func TestGoalSignalReturnsRemediationThenCompletes(t *testing.T) {
	defaultRunner, run, _ := runtimeRun(t, `goal {
  verify tests = bash.check("test-command");
  return tests;
}`)
	verifier := NewReplayVerifier([]VerificationResult{{Passed: false}, {Passed: true}})
	runner := NewWithDependencies(defaultRunner.store, NewFixedClock(time.Now()), verifier)
	if _, err := runner.Start(context.Background(), run.ID); err != nil {
		t.Fatal(err)
	}

	failed, err := runner.SignalGoalAchieved(context.Background(), signalEvent("claim-1"))
	if err != nil {
		t.Fatal(err)
	}
	if failed.Permission != "allow" || !strings.Contains(failed.Context, "at tests") || !strings.Contains(failed.Context, "Iteration 1") {
		t.Fatalf("failed signal decision = %#v", failed)
	}
	current, err := runner.store.GetRun(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != "running" || current.Iteration != 1 {
		t.Fatalf("failed signal run = %#v", current)
	}

	passed, err := runner.SignalGoalAchieved(context.Background(), signalEvent("claim-2"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(passed.Context, "independently verified") || !strings.Contains(passed.Context, "result true") {
		t.Fatalf("passed signal decision = %#v", passed)
	}
	finished, err := runner.store.GetRun(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.Status != "complete" || string(finished.Result) != "true" {
		t.Fatalf("finished = %#v", finished)
	}
	if _, err := runner.store.FindCurrentRun(context.Background(), "session", ""); err != sql.ErrNoRows {
		t.Fatalf("completed run remains bound: %v", err)
	}
}

func TestGoalSignalReplayIsIdempotentAfterCompletion(t *testing.T) {
	runner, run, _ := runtimeRun(t, `goal {
  verify ready = true;
  return ready;
}`)
	if _, err := runner.Start(context.Background(), run.ID); err != nil {
		t.Fatal(err)
	}
	event := signalEvent("same-claim")
	first, err := runner.SignalGoalAchieved(context.Background(), event)
	if err != nil {
		t.Fatal(err)
	}
	second, err := runner.SignalGoalAchieved(context.Background(), event)
	if err != nil {
		t.Fatal(err)
	}
	if first.Context != second.Context || first.RunID != second.RunID || !second.Tracked {
		t.Fatalf("replay changed decision: first=%#v second=%#v", first, second)
	}
	finished, err := runner.store.GetRun(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.Iteration != 0 || finished.Status != "complete" {
		t.Fatalf("replay mutated run: %#v", finished)
	}
}

func TestGoalSignalTurnsVerifierErrorIntoRemediation(t *testing.T) {
	defaultRunner, run, _ := runtimeRun(t, `goal {
  verify tests = bash.check("missing-verifier");
}`)
	runner := NewWithDependencies(defaultRunner.store, NewFixedClock(time.Now()), NewReplayVerifier([]VerificationResult{{Error: "runner unavailable"}}))
	if _, err := runner.Start(context.Background(), run.ID); err != nil {
		t.Fatal(err)
	}
	decision, err := runner.SignalGoalAchieved(context.Background(), signalEvent("errored-claim"))
	if err != nil {
		t.Fatal(err)
	}
	if decision.Permission != "allow" || !strings.Contains(decision.Context, "tests (`missing-verifier` must pass): runner unavailable") {
		t.Fatalf("decision = %#v", decision)
	}
	current, err := runner.store.GetRun(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != "running" || current.Iteration != 1 {
		t.Fatalf("verifier error did not return to work: %#v", current)
	}
}

func TestGoalSignalDeniedDuringImperativeStep(t *testing.T) {
	runner, run, _ := runtimeRun(t, `goal {
  verify ready = true;
} with flow {
  step action = bash.run("true");
}`)
	started, err := runner.Start(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := runner.SignalGoalAchieved(context.Background(), signalEvent("early-claim"))
	if err != nil {
		t.Fatal(err)
	}
	if decision.Permission != "deny" || !strings.Contains(decision.Remediation, "imperative step") {
		t.Fatalf("decision = %#v", decision)
	}
	current, err := runner.store.GetRun(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.StateID != started.StateID {
		t.Fatalf("denied signal advanced from %s to %s", started.StateID, current.StateID)
	}
}

func TestInjectedClockAndVerifierMakeLimitReplayDeterministic(t *testing.T) {
	defaultRunner, run, _ := runtimeRun(t, `goal {
  limit time 1s;
  verify ready = bash.check("recorded-check");
  return ready;
} with flow {
  step action = bash.run("ignored");
}`)
	clock := NewFixedClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	verifier := NewReplayVerifier([]VerificationResult{{Passed: false}})
	runner := NewWithDependencies(defaultRunner.store, clock, verifier)
	if _, err := runner.Start(context.Background(), run.ID); err != nil {
		t.Fatal(err)
	}
	clock.Advance(2 * time.Second)
	if _, err := runner.HandleHook(context.Background(), postEvent("replayed-tool")); err != nil {
		t.Fatal(err)
	}
	finished, err := runner.store.GetRun(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.Status != "limit_exceeded" {
		t.Fatalf("status = %s, want limit_exceeded", finished.Status)
	}
	calls := verifier.Calls()
	if len(calls) != 1 || calls[0].Command != "recorded-check" {
		t.Fatalf("verifier calls = %#v", calls)
	}
	observations := runner.TakeObservations(run.ID)
	if len(observations) != 1 || observations[0].Name != "ready" || observations[0].Command != "recorded-check" || observations[0].Passed {
		t.Fatalf("verification observations = %#v", observations)
	}
}

func TestExpiredTimeLimitRejectsOtherwisePassingVerification(t *testing.T) {
	defaultRunner, run, _ := runtimeRun(t, `goal {
  limit time 1s;
  verify ready = bash.check("recorded-check");
  return ready;
}`)
	clock := NewFixedClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	runner := NewWithDependencies(defaultRunner.store, clock, NewReplayVerifier([]VerificationResult{{Passed: true}}))
	if _, err := runner.Start(context.Background(), run.ID); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Second)
	decision, err := runner.SignalGoalAchieved(context.Background(), signalEvent("expired-success"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(decision.Context, "limit_exceeded") {
		t.Fatalf("decision = %#v", decision)
	}
	finished, err := runner.store.GetRun(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.Status != "limit_exceeded" || !strings.Contains(string(finished.Result), `"limit":"time"`) {
		t.Fatalf("finished = %#v, result = %s", finished, finished.Result)
	}
}
