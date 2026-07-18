package compiler

import (
	"encoding/json"
	"strings"
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
	program := compileSource(t, `goal {
  verify passed = bash.check("go test ./...");
  return passed;
} with flow {
  step ran = bash.run("go test ./...");
}`)
	machine := program.Machines[0]
	if machine.Kind != ir.LoopGoal || !machine.Imperative || len(machine.States) != 4 {
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

func TestCompileOSProcessControlStates(t *testing.T) {
	program := compileSource(t, `goal {
  verify complete = true;
  return child.result;
} with flow {
  step child = fork;
  if child.child {
    exec child-work;
  }
  wait child;
}`)
	states := program.Machines[0].States
	if states[0].Kind != ir.StateFork || states[1].Kind != ir.StateBranch || states[2].Kind != ir.StateExec || states[3].Kind != ir.StateWait {
		t.Fatalf("process states = %q, %q, %q, %q", states[0].Kind, states[1].Kind, states[2].Kind, states[3].Kind)
	}
	if states[1].OnSuccess != states[2].ID || states[1].OnFailure != states[3].ID || states[2].Script != "child-work" {
		t.Fatalf("branch/exec states = %#v, %#v", states[1], states[2])
	}
	if states[3].Executor != ir.ExecutorRuntime {
		t.Fatalf("wait executor = %q", states[3].Executor)
	}
}

func TestRejectWaitWithoutForkHandleSyntax(t *testing.T) {
	program, parseDiagnostics := language.Parse(`goal { verify done = true; } with flow { wait true; }`)
	if len(parseDiagnostics) != 0 {
		t.Fatal(parseDiagnostics)
	}
	_, diagnostics := Compile(program)
	if len(diagnostics) != 1 || !strings.Contains(diagnostics[0].Message, "fork handle") {
		t.Fatalf("diagnostics = %v", diagnostics)
	}
}

func TestCompileGoalStartsWithAgentWork(t *testing.T) {
	program := compileSource(t, `goal {
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
	program, parseDiagnostics := language.Parse(`goal {
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

func TestRejectsLimitsTheRuntimeCannotEnforce(t *testing.T) {
	for _, test := range []struct {
		source  string
		message string
	}{
		{`goal { limit cost 2; verify done = true; }`, `limit kind "cost" is not enforceable`},
		{`goal { limit attempts 0; verify done = true; }`, `positive integer`},
		{`goal { limit attempts 1.5; verify done = true; }`, `positive integer`},
		{`goal { limit time 0s; verify done = true; }`, `positive duration`},
		{`goal { limit time budget; verify done = true; }`, `static literal`},
	} {
		program, parseDiagnostics := language.Parse(test.source)
		if len(parseDiagnostics) > 0 {
			t.Fatalf("parse %q: %v", test.source, parseDiagnostics)
		}
		_, diagnostics := Compile(program)
		if len(diagnostics) == 0 || !strings.Contains(diagnostics[0].Message, test.message) {
			t.Fatalf("Compile(%q) diagnostics = %v, want %q", test.source, diagnostics, test.message)
		}
	}
	compileSource(t, `goal { limit attempts 2; limit time 30m; verify done = true; }`)
}
