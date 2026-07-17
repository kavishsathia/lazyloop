package compiler

import (
	"encoding/json"
	"testing"

	"hamster/internal/ir"
	"hamster/internal/language"
)

func compileSource(t *testing.T, source string) *ir.Program {
	t.Helper()
	program, parseDiagnostics := language.Parse(source)
	if len(parseDiagnostics) > 0 {
		t.Fatalf("parse diagnostics: %v", parseDiagnostics)
	}
	compiled, compileDiagnostics := Compile(program)
	if len(compileDiagnostics) > 0 {
		t.Fatalf("compile diagnostics: %v", compileDiagnostics)
	}
	return compiled
}

func TestCompileFlowTransitions(t *testing.T) {
	program := compileSource(t, `flow test {
  step ran = bash.run("go test ./...");
  verify passed = bash.check("go test ./...");
  return passed;
}`)
	machine := program.Machines[0]
	if machine.Kind != ir.LoopFlow || len(machine.States) != 4 {
		t.Fatalf("machine = %#v", machine)
	}
	if machine.States[0].Executor != ir.ExecutorAgent || machine.States[1].Executor != ir.ExecutorRuntime {
		t.Fatalf("executors = %q, %q", machine.States[0].Executor, machine.States[1].Executor)
	}
	boundary := machine.States[2]
	if boundary.OnFailure != machine.InitialState || boundary.OnSuccess != machine.States[3].ID {
		t.Fatalf("boundary = %#v", boundary)
	}
}

func TestCompileGoalStartsWithAgentWork(t *testing.T) {
	program := compileSource(t, `goal fix {
  allow git;
  verify clean = git.clean();
  return git.current_commit();
}`)
	machine := program.Machines[0]
	if machine.States[0].Kind != ir.StatePolicy || machine.States[1].Kind != ir.StateAgentWork {
		t.Fatalf("initial states = %q, %q, want policy then agent_work", machine.States[0].Kind, machine.States[1].Kind)
	}
	if machine.States[2].Kind != ir.StateVerify {
		t.Fatalf("verification state = %q", machine.States[2].Kind)
	}
}

func TestCompileForeachCarriesInnerReturn(t *testing.T) {
	program := compileSource(t, `foreach item in items {
  goal {
    verify ready = item.ready;
    return item.id;
  }
}`)
	machine := program.Machines[0]
	if machine.Kind != ir.LoopForeach || machine.Foreach.Binding != "item" {
		t.Fatalf("machine = %#v", machine)
	}
	if machine.Foreach.Body.Return == nil || machine.Foreach.Body.Return.Kind != ir.ExpressionMember {
		t.Fatalf("inner return = %#v", machine.Foreach.Body.Return)
	}
	if _, err := json.Marshal(program); err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
}

func TestRequireVerificationAndUniqueReturn(t *testing.T) {
	program, parseDiagnostics := language.Parse(`flow invalid {
  return true;
  return false;
}`)
	if len(parseDiagnostics) > 0 {
		t.Fatalf("parse diagnostics: %v", parseDiagnostics)
	}
	_, diagnostics := Compile(program)
	if len(diagnostics) != 2 {
		t.Fatalf("diagnostics = %v, want 2", diagnostics)
	}
}
