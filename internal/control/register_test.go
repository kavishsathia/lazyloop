package control

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"hamster/internal/hook"
	"hamster/internal/ir"
	hamsterruntime "hamster/internal/runtime"
	"hamster/internal/store"
)

func TestHookRegistrationUsesSharedRunArguments(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	script := filepath.Join(root, "workflow.hmstr")
	source := `
goal other {
  verify yes = true;
  return true;
}

flow build {
  step work = goal {
    verify yes = true;
    return "done";
  };
  verify complete = true;
  return work;
}
`
	if err := os.WriteFile(script, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := store.Open(filepath.Join(root, "hamster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()

	event := hook.Event{SessionID: "codex-session", AgentID: "ROOT", WorkspaceRoot: root}
	decision, err := RegisterFromHook(ctx, state, hamsterruntime.New(state), event, Command{
		Name: "run", Args: []string{script, "--entry", "build"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Permission != "allow" || !decision.Tracked || decision.RunID == "" {
		t.Fatalf("decision = %#v", decision)
	}
	run, err := state.GetRun(ctx, decision.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Stack) != 1 || run.Stack[0].Machine.Name != "build" || run.Stack[0].Machine.Kind != ir.LoopFlow {
		t.Fatalf("current machine = %q %q, stack = %#v", run.Machine.Name, run.Machine.Kind, run.Stack)
	}
}

func TestHookLeavesExplicitBindingToCLI(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	state, err := store.Open(filepath.Join(root, "hamster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()

	event := hook.Event{SessionID: "codex-session", AgentID: "ROOT", WorkspaceRoot: root}
	decision, err := RegisterFromHook(ctx, state, hamsterruntime.New(state), event, Command{
		Name: "run", Args: []string{"missing.hmstr", "--session", "explicit-session", "--agent", "agent"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Permission != "allow" || decision.Tracked {
		t.Fatalf("decision = %#v", decision)
	}
	if _, err := state.FindCurrentRun(ctx, event.SessionID, event.AgentID); err != sql.ErrNoRows {
		t.Fatalf("FindCurrentRun() error = %v, want sql.ErrNoRows", err)
	}
}
