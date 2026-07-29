package compiler

import (
	"strings"
	"testing"

	"hamster/internal/ir"
	"hamster/internal/language"
)

func compileDiagnostics(t *testing.T, source string) language.Diagnostics {
	t.Helper()
	program, parseDiagnostics := language.Parse(source)
	if len(parseDiagnostics) > 0 {
		return parseDiagnostics
	}
	_, diagnostics := Compile(program)
	return diagnostics
}

func trapState(t *testing.T, machine ir.Machine) ir.State {
	t.Helper()
	for _, state := range machine.States {
		if state.Kind == ir.StateTrap {
			return state
		}
	}
	t.Fatal("machine has no trap state")
	return ir.State{}
}

func TestCompileTrapIsRuntimeOwnedAndReDerivesOnRejection(t *testing.T) {
	program := compileSource(t, `goal {
  verify passed = bash.check("go test ./...");
  return passed;
} with flow {
  step ran = bash.run("go test ./...");
  trap shipped = user.approve("Ship it?");
}`)
	machine := program.Machines[0]
	state := trapState(t, machine)
	if state.Executor != ir.ExecutorControl {
		t.Fatalf("executor = %q, want control; a trap is never an agent action", state.Executor)
	}
	if state.Trap == nil || state.Trap.Kind != ir.TrapApproval {
		t.Fatalf("trap = %#v", state.Trap)
	}
	if state.Trap.Prompt.Value != "Ship it?" {
		t.Fatalf("prompt = %q", state.Trap.Prompt.Value)
	}
	if state.OnFailure != machine.InitialState {
		t.Fatalf("rejection target = %q, want initial state %q", state.OnFailure, machine.InitialState)
	}
}

func TestCompileSelectionTrapCapturesClosedOptionSet(t *testing.T) {
	program := compileSource(t, `goal {
  verify passed = bash.check("true");
  return passed;
} with flow {
  step ran = bash.run("true");
  trap style = user.select("Which style?", options: ["result", "sentinel"]);
}`)
	state := trapState(t, program.Machines[0])
	if state.Trap.Kind != ir.TrapSelection || len(state.Trap.Options) != 2 {
		t.Fatalf("trap = %#v", state.Trap)
	}
	if state.Trap.Options[0].Value != "result" || state.Trap.Options[1].Value != "sentinel" {
		t.Fatalf("options = %#v", state.Trap.Options)
	}
}

func TestCompileRejectsUnanswerableTraps(t *testing.T) {
	cases := []struct {
		name    string
		flow    string
		message string
	}{
		{
			name:    "unknown request",
			flow:    `trap t = model.assess("Is this good?");`,
			message: "supported requests are user.approve and user.select",
		},
		{
			name:    "dynamic prompt",
			flow:    `trap t = user.approve(ran);`,
			message: "must be a non-empty string literal",
		},
		{
			name:    "selection without options",
			flow:    `trap t = user.select("Which?");`,
			message: "user.select requires options",
		},
		{
			name:    "single option",
			flow:    `trap t = user.select("Which?", options: ["only"]);`,
			message: "at least two options",
		},
		{
			name:    "options on an approval",
			flow:    `trap t = user.approve("Ship it?", options: ["a", "b"]);`,
			message: "options may only be supplied to user.select",
		},
		{
			name:    "not a request at all",
			flow:    `trap t = "Ship it?";`,
			message: "trap requires user.approve",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
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

func TestCompileTrapBudgetLimit(t *testing.T) {
	program := compileSource(t, `goal {
  limit traps 3;
  verify passed = bash.check("true");
  return passed;
} with flow {
  step ran = bash.run("true");
  trap shipped = user.approve("Ship it?");
}`)
	machine := program.Machines[0]
	if machine.States[0].Kind != ir.StateLimit || machine.States[0].Limit.Kind != "traps" {
		t.Fatalf("first state = %#v", machine.States[0])
	}

	diagnostics := compileDiagnostics(t, `goal {
  limit traps 0;
  verify passed = bash.check("true");
  return passed;
}`)
	if len(diagnostics) == 0 || !strings.Contains(diagnostics.Error(), "limit traps requires a positive integer") {
		t.Fatalf("diagnostics = %v", diagnostics)
	}
}

// A goal trap is a completion gate, so it belongs on the verifier ladder. The
// ladder stops at its first failure, which means the human must be asked last:
// never spend a person's attention on work a machine check would have rejected.
func TestGoalTrapIsEmittedAfterEveryVerifier(t *testing.T) {
	program := compileSource(t, `goal {
  trap shipped = user.approve("Ship it?");
  verify tests = bash.check("go test ./...");
  verify vet = bash.check("go vet ./...");
  return shipped;
}`)
	machine := program.Machines[0]
	kinds := []ir.StateKind{}
	for _, state := range machine.States {
		kinds = append(kinds, state.Kind)
	}
	want := []ir.StateKind{ir.StateAgentWork, ir.StateVerify, ir.StateVerify, ir.StateTrap, ir.StateBoundary, ir.StateComplete}
	if len(kinds) != len(want) {
		t.Fatalf("states = %v, want %v", kinds, want)
	}
	for index := range want {
		if kinds[index] != want[index] {
			t.Fatalf("states = %v, want %v", kinds, want)
		}
	}
	state := trapState(t, machine)
	if state.OnFailure != machine.InitialState {
		t.Fatalf("rejection target = %q, want initial state %q", state.OnFailure, machine.InitialState)
	}
}

// Some work has no machine-checkable standard of completion. A goal gated only
// by human judgement is legal; requiring a token verifier would just dress a
// subjective goal up as an objective one.
func TestGoalMayBeGatedOnlyByJudgement(t *testing.T) {
	program := compileSource(t, `goal {
  limit traps 4;
  trap reads_well = user.approve("Is this poem any good?");
  return reads_well;
}`)
	machine := program.Machines[0]
	for _, state := range machine.States {
		if state.Kind == ir.StateVerify {
			t.Fatal("a judgement-only goal must not synthesize a verifier")
		}
	}
	trapState(t, machine)
}

// A goal with no gate at all can never be gated on anything, which is always a
// mistake rather than a degenerate but meaningful case.
func TestGoalRequiresAtLeastOneGate(t *testing.T) {
	diagnostics := compileDiagnostics(t, `goal {
  return "done";
}`)
	if len(diagnostics) == 0 || !strings.Contains(diagnostics.Error(), "at least one gate") {
		t.Fatalf("diagnostics = %v", diagnostics)
	}
}
