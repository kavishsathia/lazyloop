package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"hamster/internal/hook"
	"hamster/internal/store"
)

const judgedLoop = `goal {
  verify built = bash.check("true");
  return true;
} with flow {
  step ran = bash.run("go build ./...");
  judge "the new names match the package";
}`

func judgeRunner(t *testing.T, source string) (*Runner, *store.Run, string) {
	t.Helper()
	runner, run, root := runtimeRun(t, source)
	runner.SetCollector(&ReplayCollector{Outputs: map[string]string{
		"git diff -- *.go": "+func DoTheThing() error {",
	}})
	return runner, run, root
}

func ratify(t *testing.T, runner *Runner, root, name, clause string) {
	t.Helper()
	ctx := context.Background()
	if _, err := runner.store.RecordTaste(ctx, store.RecordTasteParams{
		WorkspaceRoot: root, Name: name, Question: "Do the names match the package?", Clause: clause,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.store.RatifyTaste(ctx, root, name); err != nil {
		t.Fatal(err)
	}
}

// forkEvent mimics the Codex subagent spawn the fork state requires.
func forkEvent(id, childRunID string) hook.Event {
	response, _ := json.Marshal(map[string]any{"exit_code": 0, "hamster_child_run_id": childRunID})
	event := hook.Event{SessionID: "session", EventName: "PostToolUse", ToolName: "Agent", ToolUseID: id, ToolResponse: response}
	event.Raw = hook.MarshalEvent(event)
	return event
}

// driveToObserver advances the parent to the fork, forks a child, and returns
// the observer run the child became after exec.
func driveToObserver(t *testing.T, runner *Runner, run *store.Run) (*store.Run, store.Judgement) {
	t.Helper()
	ctx := context.Background()
	if _, err := runner.Start(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	// Completing the declared step opens the judgement and reaches the fork.
	if _, err := runner.HandleHook(ctx, postEvent("step-1")); err != nil {
		t.Fatal(err)
	}
	parent, err := runner.store.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	judgement, err := runner.store.FindJudgement(ctx, run.ID, judgeStateID(t, parent), parent.Iteration)
	if err != nil {
		t.Fatalf("no judgement was opened: %v", err)
	}

	child, err := runner.store.PrepareFork(ctx, run.ID, "fork-key-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.HandleHook(ctx, forkEvent("fork-1", child.ID)); err != nil {
		t.Fatal(err)
	}
	observer, err := runner.ResumeForkChild(ctx, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	return observer, *judgement
}

func judgeStateID(t *testing.T, run *store.Run) string {
	t.Helper()
	for _, state := range run.Machine.States {
		if state.Kind == "judge" {
			return state.ID
		}
	}
	t.Fatal("machine has no judge state")
	return ""
}

// The observer is a different agent running a different program. Exec clears the
// working agent's context, which is what stops the reasoning that produced the
// work from contaminating the judgement of it.
func TestObserverRunsACleanProgram(t *testing.T) {
	runner, run, root := judgeRunner(t, judgedLoop)
	ratify(t, runner, root, "naming", "Exported names must read like the rest of the package.")
	observer, judgement := driveToObserver(t, runner, run)

	if observer.Machine.Name != "observer" {
		t.Fatalf("observer machine = %q, want the synthesized observer", observer.Machine.Name)
	}
	if len(observer.Stack) != 0 {
		t.Fatalf("exec left %d stack frames; the observer must start clean", len(observer.Stack))
	}
	var environment Environment
	if err := json.Unmarshal(observer.Variables, &environment); err != nil {
		t.Fatal(err)
	}
	// Only the observer's own machine state may be present. Nothing the working
	// agent bound — its steps, its fork handle — survives the exec.
	for _, inherited := range []string{"ran", "__judge_naming"} {
		if _, present := environment[inherited]; present {
			t.Fatalf("observer inherited %q from the agent whose work it judges", inherited)
		}
	}

	// The observer is given the ratified standards and the question, and looks
	// at the workspace itself for everything else.
	if !strings.Contains(judgement.Clause, "Exported names must read like the rest of the package.") {
		t.Fatalf("standards = %q", judgement.Clause)
	}
	if judgement.Question != "the new names match the package" {
		t.Fatalf("question = %q", judgement.Question)
	}

	bound, err := runner.store.FindObservedJudgement(context.Background(), observer.ID)
	if err != nil || bound.ID != judgement.ID {
		t.Fatalf("observer is not bound to its judgement: %v", err)
	}
}

func TestPassingVerdictAdvancesTheParent(t *testing.T) {
	ctx := context.Background()
	runner, run, root := judgeRunner(t, judgedLoop)
	ratify(t, runner, root, "naming", "Exported names must read like the rest of the package.")
	observer, _ := driveToObserver(t, runner, run)

	if _, err := runner.store.RecordVerdict(ctx, observer.ID, store.VerdictPass, "reads like the package", "consistent"); err != nil {
		t.Fatal(err)
	}
	// The observer's own gate now passes, so it completes and wakes the parent.
	if _, err := runner.HandleHook(ctx, childPostEvent("observer-1")); err != nil {
		t.Fatal(err)
	}
	parent, err := runner.store.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if parent.Status != "complete" {
		t.Fatalf("parent = %s at %s, want complete", parent.Status, parent.StateID)
	}
	judgement, err := runner.store.FindObservedJudgement(ctx, observer.ID)
	if err != nil {
		t.Fatal(err)
	}
	if judgement.Verdict != store.VerdictPass || judgement.Citation != "reads like the package" {
		t.Fatalf("judgement = %#v", judgement)
	}
}

// An abstention is not an answer. It is the observer admitting the profile did
// not cover this, which is exactly when a human must decide.
func TestAbstentionEscalatesToTheSupervisor(t *testing.T) {
	ctx := context.Background()
	runner, run, root := judgeRunner(t, judgedLoop)
	ratify(t, runner, root, "naming", "Exported names must read like the rest of the package.")
	observer, _ := driveToObserver(t, runner, run)

	if _, err := runner.store.RecordVerdict(ctx, observer.ID, store.VerdictAbstain, "", "the clause says nothing about generics"); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.HandleHook(ctx, childPostEvent("observer-1")); err != nil {
		t.Fatal(err)
	}
	parent, err := runner.store.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if parent.Status != store.TrapStatusID {
		t.Fatalf("parent = %s, want trapped", parent.Status)
	}
	traps, err := runner.store.ListOpenTraps(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(traps) != 1 {
		t.Fatalf("open traps = %d, want 1", len(traps))
	}
}

// A confident opinion with nothing behind it is worse than no judge at all, so
// an uncited verdict is coerced to an abstention and reaches a human.
func TestUncitedVerdictBecomesAnAbstention(t *testing.T) {
	ctx := context.Background()
	runner, run, root := judgeRunner(t, judgedLoop)
	ratify(t, runner, root, "naming", "Exported names must read like the rest of the package.")
	observer, _ := driveToObserver(t, runner, run)

	recorded, err := runner.store.RecordVerdict(ctx, observer.ID, store.VerdictPass, "", "looks good to me")
	if err != nil {
		t.Fatal(err)
	}
	if recorded.Verdict != store.VerdictAbstain {
		t.Fatalf("verdict = %q; a pass with no citation has no standing", recorded.Verdict)
	}
}

// An observer answers once, so it cannot argue itself from fail to pass.
func TestVerdictIsImmutable(t *testing.T) {
	ctx := context.Background()
	runner, run, root := judgeRunner(t, judgedLoop)
	ratify(t, runner, root, "naming", "Exported names must read like the rest of the package.")
	observer, _ := driveToObserver(t, runner, run)

	if _, err := runner.store.RecordVerdict(ctx, observer.ID, store.VerdictFail, "does not match", "off"); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.store.RecordVerdict(ctx, observer.ID, store.VerdictPass, "does match", "changed my mind"); err == nil {
		t.Fatal("an observer revised its recorded verdict")
	}
}

// Without a ratified clause the observer would be applying its own taste, so the
// judgement never runs and goes straight to a human.
func TestUnratifiedClauseSkipsTheObserverEntirely(t *testing.T) {
	ctx := context.Background()
	runner, run, _ := judgeRunner(t, judgedLoop)
	if _, err := runner.Start(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.HandleHook(ctx, postEvent("step-1")); err != nil {
		t.Fatal(err)
	}
	parent, err := runner.store.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if parent.Status != store.TrapStatusID {
		t.Fatalf("parent = %s, want trapped without a ratified clause", parent.Status)
	}
	count, err := runner.store.CountJudgements(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("opened %d judgements; an observer with no standing must not be paid for", count)
	}
}

// childPostEvent speaks as the forked observer. PrepareFork parks the child
// under a pending identity until Codex reports its real agent ID.
func childPostEvent(id string) hook.Event {
	response, _ := json.Marshal(map[string]any{"exit_code": 0})
	event := hook.Event{SessionID: "session", AgentID: "PENDING:fork-key-1", EventName: "PostToolUse",
		ToolName: "Bash", ToolUseID: id, ToolResponse: response}
	event.Raw = hook.MarshalEvent(event)
	return event
}
