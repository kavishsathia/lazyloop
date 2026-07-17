package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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
	if _, err := runner.HandleHook(context.Background(), postEvent("item-1")); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.HandleHook(context.Background(), postEvent("item-2")); err != nil {
		t.Fatal(err)
	}
	finished, _ := runner.store.GetRun(context.Background(), run.ID)
	if finished.Status != "complete" || string(finished.Result) != `[true,true]` {
		t.Fatalf("status = %s, result = %s", finished.Status, finished.Result)
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
