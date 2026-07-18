package control

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
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
	replayed, err := RegisterFromHook(ctx, state, hamsterruntime.New(state), event, Command{
		Name: "run", Args: []string{script, "--entry", "build"},
	})
	if err != nil || replayed.RunID != decision.RunID || replayed.Context != decision.Context {
		t.Fatalf("replayed registration = %#v, %v", replayed, err)
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

func TestHookGoalAchievedSignalCompletesBoundGoal(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	state, err := store.Open(filepath.Join(root, "hamster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	runner := hamsterruntime.New(state)
	event := hook.Event{SessionID: "codex-session", AgentID: "ROOT", WorkspaceRoot: root, EventName: "PreToolUse", ToolName: "Bash", ToolUseID: "run"}
	started, err := RegisterFromHook(ctx, state, runner, event, Command{
		Name: "run", Args: []string{"--text", `goal work { verify done = true; return done; }`},
	})
	if err != nil {
		t.Fatal(err)
	}
	event.ToolUseID = "signal"
	decision, err := RegisterFromHook(ctx, state, runner, event, Command{Name: "signal", Args: []string{"goal-achieved"}})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Permission != "allow" || decision.RunID != started.RunID || !decision.Tracked || !strings.Contains(decision.Context, "independently verified") {
		t.Fatalf("decision = %#v", decision)
	}
	run, err := state.GetRun(ctx, started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "complete" {
		t.Fatalf("run status = %s", run.Status)
	}
}

func TestHookRejectsMalformedGoalSignal(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	state, err := store.Open(filepath.Join(root, "hamster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	event := hook.Event{SessionID: "codex-session", WorkspaceRoot: root}
	decision, err := RegisterFromHook(ctx, state, hamsterruntime.New(state), event, Command{Name: "signal", Args: []string{"done"}})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Permission != "deny" || decision.Reason != "usage: hamster signal goal-achieved" {
		t.Fatalf("decision = %#v", decision)
	}
}
