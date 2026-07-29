package runtime

import (
	"context"
	"testing"

	"hamster/internal/store"
)

const escalatingLoop = `goal {
  verify built = bash.check("true");
  return true;
} with flow {
  step ran = bash.run("go build ./...");
  judge "the new names match the package";
}`

// failOnce drives one complete judge cycle to a recorded verdict.
func failOnce(t *testing.T, runner *Runner, run *store.Run, forkKey, toolID, verdict string) {
	t.Helper()
	ctx := context.Background()
	if _, err := runner.HandleHook(ctx, postEvent("step-"+toolID)); err != nil {
		t.Fatal(err)
	}
	child, err := runner.store.PrepareFork(ctx, run.ID, forkKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.HandleHook(ctx, forkEvent("fork-"+toolID, child.ID)); err != nil {
		t.Fatal(err)
	}
	observer, err := runner.ResumeForkChild(ctx, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.store.RecordVerdict(ctx, observer.ID, verdict, "the clause", "reasoning"); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.AdvanceAgentWork(ctx, observer.ID); err != nil {
		t.Fatal(err)
	}
}

// A taste failure the agent cannot perceive will not fix itself. Repeating it
// spends attempts without converging, so after enough consecutive failures the
// question becomes a human's.
func TestRepeatedFailuresEscalateToTheSupervisor(t *testing.T) {
	ctx := context.Background()
	runner, run, root := judgeRunner(t, escalatingLoop)
	ratify(t, runner, root, "naming", "Exported names must read like the rest of the package.")
	if _, err := runner.Start(ctx, run.ID); err != nil {
		t.Fatal(err)
	}

	failOnce(t, runner, run, "fork-a", "a", store.VerdictFail)
	afterFirst, err := runner.store.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterFirst.Status != "running" {
		t.Fatalf("status = %s; one failure must re-derive rather than escalate", afterFirst.Status)
	}
	if afterFirst.Iteration != 1 {
		t.Fatalf("iteration = %d, want 1", afterFirst.Iteration)
	}

	failOnce(t, runner, run, "fork-b", "b", store.VerdictFail)
	failOnce(t, runner, run, "fork-c", "c", store.VerdictFail)
	afterLimit, err := runner.store.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterLimit.Status != store.TrapStatusID {
		t.Fatalf("status = %s; repeated failure must eventually reach a human", afterLimit.Status)
	}
	failures, err := runner.store.CountConsecutiveFailures(ctx, run.ID, judgeStateID(t, afterLimit))
	if err != nil {
		t.Fatal(err)
	}
	if failures != consecutiveFailureLimit {
		t.Fatalf("consecutive failures = %d, want %d", failures, consecutiveFailureLimit)
	}
}

// Resolving a stand-in trap is the supervisor teaching the profile what it did
// not cover, so the reasoning becomes a proposed refinement rather than a note
// nobody reads.
func TestEscalatedTrapWritesTheReasoningBackAsARefinement(t *testing.T) {
	ctx := context.Background()
	runner, run, root := judgeRunner(t, judgedLoop)
	ratify(t, runner, root, "naming", "Exported names must read like the rest of the package.")
	observer, _ := driveToObserver(t, runner, run)
	if _, err := runner.store.RecordVerdict(ctx, observer.ID, store.VerdictAbstain, "", "generics are not covered"); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.AdvanceAgentWork(ctx, observer.ID); err != nil {
		t.Fatal(err)
	}
	traps, err := runner.store.ListOpenTraps(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(traps) != 1 {
		t.Fatalf("open traps = %d, want 1", len(traps))
	}

	if _, err := runner.store.ResolveTrap(ctx, traps[0].ID, store.TrapApprove,
		"Generic type parameters count as package idiom."); err != nil {
		t.Fatal(err)
	}

	// The standing clause is untouched; the lesson is a draft awaiting adoption.
	standing, err := runner.store.ListTaste(ctx, root, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(standing) != 1 || standing[0].Clause != "Exported names must read like the rest of the package." {
		t.Fatalf("standing profile changed without adoption: %#v", standing)
	}
	all, err := runner.store.ListTaste(ctx, root, false)
	if err != nil {
		t.Fatal(err)
	}
	var refinement *store.TasteClause
	for index := range all {
		if all[index].Status == store.TasteDraft {
			refinement = &all[index]
		}
	}
	if refinement == nil {
		t.Fatal("the supervisor's reasoning was not captured as a refinement")
	}
	if refinement.Clause != "Generic type parameters count as package idiom." || refinement.Source != store.TasteVerdict {
		t.Fatalf("refinement = %#v", refinement)
	}
}

// A judge is only as trustworthy as the clause behind it. Spot-checking passes
// is the only way a rubric that has gone hollow becomes visible.
func TestSampledPassIsQueuedForAudit(t *testing.T) {
	ctx := context.Background()
	runner, run, root := judgeRunner(t, `goal {
  verify built = bash.check("true");
  return true;
} with flow {
  step ran = bash.run("go build ./...");
  judge "the new names match the package";
}`)
	ratify(t, runner, root, "naming", "Exported names must read like the rest of the package.")
	observer, _ := driveToObserver(t, runner, run)
	if _, err := runner.store.RecordVerdict(ctx, observer.ID, store.VerdictPass, "reads like the package", "consistent"); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.AdvanceAgentWork(ctx, observer.ID); err != nil {
		t.Fatal(err)
	}

	// Auditing never blocks: the run completed on the observer's word.
	parent, err := runner.store.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if parent.Status != "complete" {
		t.Fatalf("status = %s; an audit must not gate the run", parent.Status)
	}
	judgement, err := runner.store.FindObservedJudgement(ctx, observer.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.store.SampleForAudit(ctx, judgement.ID, 1); err != nil {
		t.Fatal(err)
	}
	audits, err := runner.store.ListAudits(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(audits) != 1 || audits[0].Verdict != store.VerdictPass {
		t.Fatalf("audits = %#v", audits)
	}

	// Disagreeing with the observer proposes what the clause should have said.
	if _, err := runner.store.ResolveAudit(ctx, audits[0].ID, store.AuditFlagged,
		"Abbreviations are never package idiom."); err != nil {
		t.Fatal(err)
	}
	all, err := runner.store.ListTaste(ctx, root, false)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, clause := range all {
		if clause.Status == store.TasteDraft && clause.Source == store.TasteVerdict {
			found = true
		}
	}
	if !found {
		t.Fatal("a flagged audit must propose a refinement to the clause")
	}
	remaining, err := runner.store.ListAudits(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 0 {
		t.Fatalf("resolved audit is still queued: %#v", remaining)
	}
}

func TestAgreeingWithAnObserverProposesNothing(t *testing.T) {
	ctx := context.Background()
	runner, run, root := judgeRunner(t, `goal {
  verify built = bash.check("true");
  return true;
} with flow {
  step ran = bash.run("go build ./...");
  judge "the new names match the package";
}`)
	ratify(t, runner, root, "naming", "Exported names must read like the rest of the package.")
	observer, _ := driveToObserver(t, runner, run)
	if _, err := runner.store.RecordVerdict(ctx, observer.ID, store.VerdictPass, "reads like the package", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.AdvanceAgentWork(ctx, observer.ID); err != nil {
		t.Fatal(err)
	}
	judgement, err := runner.store.FindObservedJudgement(ctx, observer.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.store.SampleForAudit(ctx, judgement.ID, 1); err != nil {
		t.Fatal(err)
	}
	audits, err := runner.store.ListAudits(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.store.ResolveAudit(ctx, audits[0].ID, store.AuditAgreed, ""); err != nil {
		t.Fatal(err)
	}
	all, err := runner.store.ListTaste(ctx, root, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("agreement must not change the profile: %#v", all)
	}
}
