package language

import "testing"

func TestParseNestedLoopsAndExpressions(t *testing.T) {
	source := `
goal {
  limit attempts 3;
  verify complete = deployments.count == services.count;
  return deployments;
} with flow {
  step deployments = foreach service in services {
    goal {
      verify healthy = docker.healthy(image) == true;
      return image;
    } with flow {
      step image = docker.build(service.path);
    }
  };
}`

	program, diagnostics := Parse(source)
	if len(diagnostics) != 0 {
		t.Fatalf("Parse() diagnostics = %v", diagnostics)
	}
	if len(program.Loops) != 1 {
		t.Fatalf("Parse() loops = %d, want 1", len(program.Loops))
	}
	goal, ok := program.Loops[0].(*Goal)
	if !ok {
		t.Fatalf("Parse() loop type = %T, want *Goal", program.Loops[0])
	}
	if goal.Flow == nil || len(goal.Statements) != 3 || len(goal.Flow.Statements) != 1 {
		t.Fatalf("goal = %#v", goal)
	}
	step := goal.Flow.Statements[0].(*Step)
	loopValue, ok := step.Value.(*LoopValue)
	if !ok {
		t.Fatalf("step value type = %T, want *LoopValue", step.Value)
	}
	foreach, ok := loopValue.Loop.(*Foreach)
	if !ok || foreach.Binding != "service" {
		t.Fatalf("nested loop = %#v", loopValue.Loop)
	}
}

func TestParseGoalPoliciesAndNamedArguments(t *testing.T) {
	source := `goal {
  allow git;
  disallow docker;
  limit time 30m;
  verify clean = git.diff(scope: "issue").empty;
  return git.current_commit();
}`
	program, diagnostics := Parse(source)
	if len(diagnostics) != 0 {
		t.Fatalf("Parse() diagnostics = %v", diagnostics)
	}
	goal := program.Loops[0].(*Goal)
	if len(goal.Statements) != 5 {
		t.Fatalf("statements = %d, want 5", len(goal.Statements))
	}
}

func TestRejectNamesAndStandaloneFlow(t *testing.T) {
	for _, source := range []string{
		`goal named { verify okay = true; }`,
		`flow { step work = bash.run("true"); }`,
	} {
		_, diagnostics := Parse(source)
		if len(diagnostics) == 0 {
			t.Fatalf("Parse(%q) unexpectedly succeeded", source)
		}
	}
}

func TestParsesOSProcessControlSyntax(t *testing.T) {
	program, diagnostics := Parse(`goal {
  verify done = true;
  return results;
} with flow {
  step alpha = fork;
  if alpha.child {
    exec child-alpha;
  }
  wait alpha;
}`)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %v", diagnostics)
	}
	goal := program.Loops[0].(*Goal)
	if len(goal.Flow.Statements) != 3 {
		t.Fatalf("flow statements = %#v", goal.Flow.Statements)
	}
	forkStep := goal.Flow.Statements[0].(*Step)
	if _, ok := forkStep.Value.(*Fork); !ok {
		t.Fatalf("fork value = %T", forkStep.Value)
	}
	branch := goal.Flow.Statements[1].(*If)
	if len(branch.Statements) != 1 || branch.Statements[0].(*Exec).Script != "child-alpha" {
		t.Fatalf("branch = %#v", branch)
	}
	if _, ok := goal.Flow.Statements[2].(*Wait); !ok {
		t.Fatalf("wait statement = %T", goal.Flow.Statements[2])
	}
}

func TestRejectStepInsideGoal(t *testing.T) {
	_, diagnostics := Parse(`goal { step nope = bash.run("false"); }`)
	if len(diagnostics) == 0 || diagnostics[0].Message != "step is only valid inside flow" {
		t.Fatalf("diagnostics = %v", diagnostics)
	}
}

func TestLexReportsSourcePosition(t *testing.T) {
	_, diagnostics := Lex("goal {\n  @\n}")
	if len(diagnostics) != 1 {
		t.Fatalf("diagnostics = %v", diagnostics)
	}
	if diagnostics[0].Span.Start.Line != 2 || diagnostics[0].Span.Start.Column != 3 {
		t.Fatalf("position = %v, want 2:3", diagnostics[0].Span.Start)
	}
}
