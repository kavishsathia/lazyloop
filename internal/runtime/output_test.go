package runtime

import (
	"context"
	"strings"
	"testing"
)

// An agent that did not write the loop has nothing to go on but the hook
// context, so a failed verifier must say what ran and what it printed.
func TestFailedVerifierReportsItsCommandAndOutput(t *testing.T) {
	runner, run, _ := runtimeRun(t, `goal {
  verify passes = bash.check("cd gg");
  return passes;
}`)
	if _, err := runner.Start(context.Background(), run.ID); err != nil {
		t.Fatal(err)
	}
	decision, err := runner.HandleHook(context.Background(), postEvent("tool-1"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"passes", "`cd gg` must pass", "gg"} {
		if !strings.Contains(decision.Context, want) {
			t.Fatalf("context %q lacks %q", decision.Context, want)
		}
	}
	if !strings.Contains(strings.ToLower(decision.Context), "no such file") {
		t.Fatalf("context does not carry the shell's own error: %q", decision.Context)
	}
}

func TestVerifierCommandsListTheLadder(t *testing.T) {
	runner, run, _ := runtimeRun(t, `goal {
  limit attempts 2;
  verify tests = bash.check("go test ./...");
  verify vet = bash.check("go vet ./...");
  return tests;
}`)
	_ = runner
	commands := VerifierCommands(run.Machine)
	if len(commands) != 2 || commands[0] != "go test ./..." || commands[1] != "go vet ./..." {
		t.Fatalf("VerifierCommands = %q", commands)
	}
}

func TestTailOutputKeepsTheEnd(t *testing.T) {
	long := strings.Repeat("x", 100) + "FAIL: the summary"
	if got := tailOutput([]byte(long), 20); !strings.HasSuffix(got, "FAIL: the summary") || !strings.HasPrefix(got, "…") {
		t.Fatalf("tailOutput = %q", got)
	}
	if got := tailOutput([]byte("  short  \n"), 20); got != "short" {
		t.Fatalf("tailOutput(short) = %q", got)
	}
}
