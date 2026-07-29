package compiler

import (
	"strings"
	"testing"

	"hamster/internal/ir"
)

const judgedFlow = `goal {
  verify passed = bash.check("go test ./...");
  return passed;
} with flow {
  step ran = bash.run("go build ./...");
  judge "the new names match the package";
}`

// A judge is not a runtime instruction. It desugars into process primitives, and
// that desugaring is what buys the observer a cleared context and an allow-list
// it cannot escape rather than a prompt asking it to behave.
func TestJudgeDesugarsIntoProcessPrimitives(t *testing.T) {
	machine := compileSource(t, judgedFlow).Machines[0]
	kinds := []ir.StateKind{}
	for _, state := range machine.States {
		kinds = append(kinds, state.Kind)
	}
	want := []ir.StateKind{
		ir.StateStep, ir.StateJudge, ir.StateFork, ir.StateBranch, ir.StateExec,
		ir.StateWait, ir.StateVerdict, ir.StateTrap, ir.StateVerify, ir.StateBoundary, ir.StateComplete,
	}
	if len(kinds) != len(want) {
		t.Fatalf("states = %v, want %v", kinds, want)
	}
	for index := range want {
		if kinds[index] != want[index] {
			t.Fatalf("states = %v, want %v", kinds, want)
		}
	}

	byKind := map[ir.StateKind]ir.State{}
	for _, state := range machine.States {
		byKind[state.Kind] = state
	}
	// A pass must skip the escalation trap; only an undecidable verdict reaches it.
	verdict := byKind[ir.StateVerdict]
	if verdict.Next == byKind[ir.StateTrap].ID {
		t.Fatal("a passing verdict must not fall into the escalation trap")
	}
	if verdict.Judge.Escalate != byKind[ir.StateTrap].ID {
		t.Fatalf("escalation target = %q, want the trap %q", verdict.Judge.Escalate, byKind[ir.StateTrap].ID)
	}
	if verdict.JudgeRef != byKind[ir.StateJudge].ID {
		t.Fatalf("verdict is not linked to its judgement: %q", verdict.JudgeRef)
	}
	// A failed judgement re-derives the work, exactly like a failed verifier.
	if verdict.OnFailure != machine.InitialState {
		t.Fatalf("failure target = %q, want initial state %q", verdict.OnFailure, machine.InitialState)
	}
}

// A reviewer who can only see what it was handed is a weak reviewer, so the
// observer investigates the workspace itself. What it must not do is change the
// work it is judging.
func TestObserverMayInvestigateButNotEdit(t *testing.T) {
	machine := compileSource(t, judgedFlow).Machines[0]
	var observer *ir.Machine
	for _, state := range machine.States {
		if state.Kind == ir.StateExec && state.Machine != nil {
			observer = state.Machine
		}
	}
	if observer == nil {
		t.Fatal("no observer machine was synthesized")
	}
	allowed, denied := map[string]bool{}, map[string]bool{}
	for _, state := range observer.States {
		if state.Kind != ir.StatePolicy || state.Policy == nil {
			continue
		}
		if state.Policy.Allow {
			allowed[state.Policy.Value.Name] = true
		} else {
			denied[state.Policy.Value.Name] = true
		}
	}
	if !allowed["bash"] || !allowed["hamster"] {
		t.Fatalf("an observer that cannot look around cannot review: allowed = %v", allowed)
	}
	if !denied["apply_patch"] {
		t.Fatalf("an observer must not patch the work it judges: denied = %v", denied)
	}
	// Silence must not hang the parent, so the observer is time-bounded and its
	// silence becomes an abstention rather than a guess.
	deadline := false
	spoke := false
	for _, state := range observer.States {
		if state.Kind == ir.StateLimit && state.Limit.Kind == "time" {
			deadline = true
		}
		if state.Kind == ir.StateVerify && state.Name == "spoke" {
			spoke = true
		}
	}
	if !deadline {
		t.Fatal("an observer without a deadline can hang its parent forever")
	}
	if !spoke {
		t.Fatal("the observer must be unable to terminate without recording a verdict")
	}
}

func TestJudgementBudgetIsSeparateFromAttention(t *testing.T) {
	machine := compileSource(t, `goal {
  limit traps 2;
  limit judgements 8;
  verify passed = bash.check("true");
  return passed;
} with flow {
  step ran = bash.run("true");
  judge "the new names match the package";
}`).Machines[0]
	kinds := map[string]bool{}
	for _, state := range machine.States {
		if state.Kind == ir.StateLimit {
			kinds[state.Limit.Kind] = true
		}
	}
	if !kinds["traps"] || !kinds["judgements"] {
		t.Fatalf("limits = %v, want both traps and judgements", kinds)
	}

	diagnostics := compileDiagnostics(t, `goal {
  limit judgements 0;
  verify passed = bash.check("true");
  return passed;
}`)
	if len(diagnostics) == 0 || !strings.Contains(diagnostics.Error(), "limit judgements requires a positive integer") {
		t.Fatalf("diagnostics = %v", diagnostics)
	}
}

// A judge in a goal body used to be parsed and then silently dropped, so a loop
// looked like it was judging while nothing judged anything.
func TestGoalBodyJudgeIsNotSilentlyDropped(t *testing.T) {
	machine := compileSource(t, `goal {
  verify exists = bash.check("test -f NOTES.md");
  judge "the notes read plainly";
  return exists;
}`).Machines[0]
	kinds := map[ir.StateKind]bool{}
	for _, state := range machine.States {
		kinds[state.Kind] = true
	}
	for _, required := range []ir.StateKind{ir.StateJudge, ir.StateFork, ir.StateExec, ir.StateWait, ir.StateVerdict} {
		if !kinds[required] {
			t.Fatalf("a goal-body judge did not emit %s: %v", required, kinds)
		}
	}
}

// A name nothing binds is a typo, and it must be caught while you are looking at
// the editor rather than as an evaluator error at the very end of a run.
func TestUnknownNamesAreRejectedWhereverTheyAppear(t *testing.T) {
	cases := map[string]string{
		`goal { verify ok = bash.check("true"); return shipped; }`:                             "shipped",
		`goal { verify ok = bash.check("true"); return ok; } with flow { wait ghost; }`:        "ghost",
		`goal { verify ok = bash.check("true"); return ok; } with flow { if absent.child {} }`: "absent",
		`goal { verify ok = missing; return ok; }`:                                             "missing",
	}
	for source, name := range cases {
		diagnostics := compileDiagnostics(t, source)
		if len(diagnostics) == 0 || !strings.Contains(diagnostics.Error(), `unknown name "`+name+`"`) {
			t.Fatalf("%s -> diagnostics = %v, want unknown name %q", source, diagnostics, name)
		}
	}

	// Capability names in a policy are not bindings and must not be flagged.
	if diagnostics := compileDiagnostics(t, `goal {
  allow bash;
  disallow apply_patch;
  verify ok = bash.check("true");
  return ok;
}`); len(diagnostics) > 0 {
		t.Fatalf("policy capabilities were treated as bindings: %v", diagnostics)
	}
}
