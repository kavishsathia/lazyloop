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
  judge naming = taste.check("git diff -- *.go");
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

// The observer's allow-list is the enforcement. It may run Hamster commands and
// nothing else, so it structurally cannot edit the work it is judging.
func TestObserverMachineCannotTouchTheWork(t *testing.T) {
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
	allowed := []string{}
	for _, state := range observer.States {
		if state.Kind == ir.StatePolicy && state.Policy != nil && state.Policy.Allow {
			allowed = append(allowed, state.Policy.Value.Name)
		}
	}
	if len(allowed) != 1 || allowed[0] != "hamster" {
		t.Fatalf("observer allow-list = %v, want exactly [hamster]", allowed)
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

func TestCompileRejectsUnjudgeableRequests(t *testing.T) {
	cases := []struct {
		flow    string
		message string
	}{
		{`judge n = user.approve("ok?");`, "judge requires taste.check"},
		{`judge n = taste.check(ran);`, "static command or list"},
		{`judge n = taste.check([]);`, "at least one evidence command"},
		{`judge n = taste.check("a", "b");`, "one evidence argument"},
		{`judge n = taste.check("   ");`, "non-empty string literal"},
	}
	for _, testCase := range cases {
		t.Run(testCase.message, func(t *testing.T) {
			diagnostics := compileDiagnostics(t, `goal {
  verify passed = bash.check("true");
  return passed;
} with flow {
  step ran = bash.run("true");
  `+testCase.flow+`
}`)
			if len(diagnostics) == 0 {
				t.Fatal("expected a diagnostic")
			}
			if !strings.Contains(diagnostics.Error(), testCase.message) {
				t.Fatalf("diagnostics = %v, want %q", diagnostics, testCase.message)
			}
		})
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
  judge naming = taste.check("git diff");
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
