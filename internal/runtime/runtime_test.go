package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hamster/internal/compiler"
	"hamster/internal/hook"
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
	runner, run, root := runtimeRun(t, `flow test {
  step action = bash.run("touch marker");
  verify exists = bash.check("test -f marker");
  return exists;
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

func TestAttemptLimitStartsAtDeclaration(t *testing.T) {
	runner, run, _ := runtimeRun(t, `flow retrying {
  limit attempts 2;
  step action = bash.run("true");
  verify never = false;
  return never;
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
	runner, run, _ := runtimeRun(t, `flow collect {
  step values = foreach item in [true, true] {
    goal {
      verify ready = item;
      return item;
    }
  };
  verify complete = true;
  return values;
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

func TestAgentToolCallsDoNotClaimGoalCompletion(t *testing.T) {
	runner, run, _ := runtimeRun(t, `goal work {
  verify ready = true;
  return ready;
}`)
	started, err := runner.Start(context.Background(), run.ID)
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
	if current.StateID != started.StateID || current.Status != "running" || current.Iteration != 0 {
		t.Fatalf("ordinary tool advanced goal: before=%s after=%#v", started.StateID, current)
	}
	if observations := runner.TakeObservations(run.ID); len(observations) != 0 {
		t.Fatalf("ordinary tool ran verifiers: %#v", observations)
	}
}

func TestGoalSignalReturnsRemediationThenCompletes(t *testing.T) {
	defaultRunner, run, _ := runtimeRun(t, `goal work {
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
	if failed.Permission != "allow" || !strings.Contains(failed.Context, "Failed verification: tests") || !strings.Contains(failed.Context, "iteration 1") {
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
	runner, run, _ := runtimeRun(t, `goal work {
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
	defaultRunner, run, _ := runtimeRun(t, `goal work {
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
	if decision.Permission != "allow" || !strings.Contains(decision.Context, "tests: runner unavailable") {
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
	runner, run, _ := runtimeRun(t, `flow work {
  step action = bash.run("true");
  verify ready = true;
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
	defaultRunner, run, _ := runtimeRun(t, `flow replay {
  limit time 1s;
  step action = bash.run("ignored");
  verify ready = bash.check("recorded-check");
  return ready;
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
	defaultRunner, run, _ := runtimeRun(t, `goal timed {
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
