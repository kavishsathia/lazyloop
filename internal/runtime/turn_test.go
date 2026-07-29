package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hamster/internal/hook"
	"hamster/internal/store"
)

func stopEvent(agentID string, active bool) hook.Event {
	event := hook.Event{SessionID: "session", AgentID: agentID, EventName: "Stop", StopHookActive: active}
	event.Raw = hook.MarshalEvent(event)
	return event
}

func promptEvent(text string) hook.Event {
	event := hook.Event{SessionID: "session", EventName: "UserPromptSubmit", Prompt: text}
	event.Raw = hook.MarshalEvent(event)
	return event
}

func trapped(t *testing.T, source string) (*Runner, *store.Run, string) {
	t.Helper()
	ctx := context.Background()
	runner, run, root := runtimeRun(t, source)
	if _, err := runner.Start(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "marker"), []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.HandleHook(ctx, postEvent("tool-1")); err != nil {
		t.Fatal(err)
	}
	return runner, run, root
}

// Ending the turn is how an interactive session reaches a person, so a trapped
// run must let the agent stop rather than holding it in a denial loop.
func TestStopIsAllowedAtATrapAndCarriesTheQuestion(t *testing.T) {
	ctx := context.Background()
	runner, run, _ := trapped(t, approvalLoop)
	decision, err := runner.HandleStop(ctx, stopEvent("", false))
	if err != nil {
		t.Fatal(err)
	}
	if decision.Block {
		t.Fatal("blocking the stop would keep the question from ever reaching a person")
	}
	if decision.RunID != run.ID {
		t.Fatalf("run = %q", decision.RunID)
	}
	for _, want := range []string{"Does this look right?", "approve", "reject"} {
		if !contains(decision.Context, want) {
			t.Fatalf("context is missing %q: %s", want, decision.Context)
		}
	}
}

// A run whose next move belongs to the agent is unfinished work, so ending the
// turn abandons a loop Hamster is still enforcing.
func TestStopIsBlockedWhileTheLoopIsUnfinished(t *testing.T) {
	ctx := context.Background()
	runner, run, _ := runtimeRun(t, approvalLoop)
	if _, err := runner.Start(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	decision, err := runner.HandleStop(ctx, stopEvent("", false))
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Block || decision.Continuation == "" {
		t.Fatalf("decision = %#v, want a continuation prompt", decision)
	}
	if !contains(decision.Continuation, run.ID) {
		t.Fatalf("continuation = %q", decision.Continuation)
	}

	// Codex re-enters with stop_hook_active once it has already been blocked, so
	// the nudge happens once instead of trapping the agent forever.
	again, err := runner.HandleStop(ctx, stopEvent("", true))
	if err != nil {
		t.Fatal(err)
	}
	if again.Block {
		t.Fatal("a second block would loop the agent forever")
	}
}

func TestUserReplyResolvesTheTrapAndResumesTheRun(t *testing.T) {
	ctx := context.Background()
	runner, run, _ := trapped(t, approvalLoop)

	// Anything Hamster cannot read mechanically leaves the trap open, because it
	// has no model and must never guess what a person meant.
	vague, err := runner.HandleUserPrompt(ctx, promptEvent("yeah looks fine to me"))
	if err != nil {
		t.Fatal(err)
	}
	if !contains(vague.Context, "did not read that as an answer") {
		t.Fatalf("context = %q", vague.Context)
	}
	still, err := runner.store.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if still.Status != store.TrapStatusID {
		t.Fatalf("status = %s; an unreadable reply must not resolve anything", still.Status)
	}

	answered, err := runner.HandleUserPrompt(ctx, promptEvent("approve"))
	if err != nil {
		t.Fatal(err)
	}
	if !contains(answered.Context, "resumed run") {
		t.Fatalf("context = %q", answered.Context)
	}
	done, err := runner.store.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != "complete" {
		t.Fatalf("status = %s, want complete", done.Status)
	}
}

// A rejection re-derives the work, so the agent needs to know what was wrong.
func TestRejectionRequiresAReason(t *testing.T) {
	ctx := context.Background()
	runner, run, _ := trapped(t, approvalLoop)
	bare, err := runner.HandleUserPrompt(ctx, promptEvent("reject"))
	if err != nil {
		t.Fatal(err)
	}
	if !contains(bare.Context, "did not read that as an answer") {
		t.Fatalf("context = %q", bare.Context)
	}

	if _, err := runner.HandleUserPrompt(ctx, promptEvent("reject the names read like Java")); err != nil {
		t.Fatal(err)
	}
	looped, err := runner.store.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if looped.Status != "running" || looped.Iteration != 1 {
		t.Fatalf("run = %s at iteration %d, want running at 1", looped.Status, looped.Iteration)
	}
	traps, err := runner.store.ListOpenTraps(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(traps) != 0 {
		t.Fatalf("the answered trap is still open: %#v", traps)
	}
}

func TestSelectionIsAnsweredByChoosingADeclaredOption(t *testing.T) {
	ctx := context.Background()
	runner, run, _ := trapped(t, `goal {
  verify exists = bash.check("test -f marker");
  return style;
} with flow {
  step action = bash.run("touch marker");
  trap style = user.select("Which error style?", options: ["result", "sentinel"]);
}`)
	if _, err := runner.HandleUserPrompt(ctx, promptEvent("choose panic")); err != nil {
		t.Fatal(err)
	}
	open, err := runner.store.ListOpenTraps(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 1 {
		t.Fatal("an undeclared option must not resolve the trap")
	}

	if _, err := runner.HandleUserPrompt(ctx, promptEvent("choose sentinel")); err != nil {
		t.Fatal(err)
	}
	done, err := runner.store.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != "complete" {
		t.Fatalf("status = %s, want complete", done.Status)
	}
}

// A silent observer would otherwise hold its parent until its deadline expires.
func TestObserverThatStopsWithoutSpeakingAbstains(t *testing.T) {
	ctx := context.Background()
	runner, run, root := judgeRunner(t, judgedLoop)
	ratify(t, runner, root, "naming", "Exported names must read like the rest of the package.")
	observer, _ := driveToObserver(t, runner, run)

	event := stopEvent("PENDING:fork-key-1", false)
	event.EventName = "SubagentStop"
	decision, err := runner.HandleSubagentStop(ctx, event)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(decision.Context, "abstention") {
		t.Fatalf("context = %q", decision.Context)
	}
	judgement, err := runner.store.FindObservedJudgement(ctx, observer.ID)
	if err != nil {
		t.Fatal(err)
	}
	if judgement.Verdict != store.VerdictAbstain {
		t.Fatalf("verdict = %q, want abstain", judgement.Verdict)
	}
	parent, err := runner.store.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if parent.Status != store.TrapStatusID {
		t.Fatalf("parent = %s; an abstention must reach a human", parent.Status)
	}
}

func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}
