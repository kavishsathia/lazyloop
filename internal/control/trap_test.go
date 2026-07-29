package control

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"hamster/internal/hook"
	hamsterruntime "hamster/internal/runtime"
	"hamster/internal/store"
)

// A trap is only a real transfer of control if the agent it suspends cannot
// answer it. The hook is where that authority boundary is enforced.
func TestAgentCannotResolveItsOwnTrap(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	state, err := store.Open(filepath.Join(root, "hamster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()

	event := hook.Event{SessionID: "codex-session", AgentID: "ROOT", EventName: "PreToolUse",
		ToolName: "Bash", ToolUseID: "resolve-attempt", WorkspaceRoot: root}
	decision, err := RegisterFromHook(ctx, state, hamsterruntime.New(state), event, Command{
		Name: "resolve", Args: []string{"trap_abc", "--approve"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Permission != "deny" {
		t.Fatalf("permission = %s, want deny", decision.Permission)
	}
	if !strings.Contains(decision.Reason, "supervising human") {
		t.Fatalf("reason = %q", decision.Reason)
	}
}

// The agent runs the interview and reports what it heard, so recording is its
// job. Adopting the clause is not: an agent that could ratify its own standard
// would be authoring the rubric it is graded against.
func TestAgentMayProposeTasteButNotAdoptIt(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	state, err := store.Open(filepath.Join(root, "hamster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	runner := hamsterruntime.New(state)

	event := hook.Event{SessionID: "codex-session", AgentID: "ROOT", EventName: "PreToolUse",
		ToolName: "Bash", ToolUseID: "propose", WorkspaceRoot: root}
	proposed, err := RegisterFromHook(ctx, state, runner, event, Command{
		Name: "taste", Args: []string{"record", "--name", "error_style", "--clause", "Wrap with %w."},
	})
	if err != nil {
		t.Fatal(err)
	}
	if proposed.Permission != "allow" {
		t.Fatalf("permission = %s, want allow", proposed.Permission)
	}
	if !strings.Contains(proposed.Context, "carries no authority") {
		t.Fatalf("context = %q", proposed.Context)
	}

	event.ToolUseID = "adopt"
	adopted, err := RegisterFromHook(ctx, state, runner, event, Command{
		Name: "taste", Args: []string{"ratify"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if adopted.Permission != "deny" {
		t.Fatalf("permission = %s, want deny", adopted.Permission)
	}

	// The proposal is stored, but it is not part of the standing profile.
	ratified, err := state.ListTaste(ctx, root, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(ratified) != 0 {
		t.Fatalf("ratified clauses = %d; the agent adopted its own standard", len(ratified))
	}
	drafts, err := state.ListTaste(ctx, root, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(drafts) != 1 || drafts[0].Clause != "Wrap with %w." {
		t.Fatalf("drafts = %#v", drafts)
	}
}

func TestAgentReadsOnlyTheRatifiedProfile(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	state, err := store.Open(filepath.Join(root, "hamster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if _, err := state.RecordTaste(ctx, store.RecordTasteParams{
		WorkspaceRoot: root, Name: "naming", Clause: "Unratified proposal.",
	}); err != nil {
		t.Fatal(err)
	}

	event := hook.Event{SessionID: "codex-session", AgentID: "ROOT", EventName: "PreToolUse",
		ToolName: "Bash", ToolUseID: "show", WorkspaceRoot: root}
	decision, err := RegisterFromHook(ctx, state, hamsterruntime.New(state), event, Command{
		Name: "taste", Args: []string{"show"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(decision.Context, "Unratified proposal") {
		t.Fatalf("an unratified proposal was served as the profile: %q", decision.Context)
	}
	if !strings.Contains(decision.Context, "no ratified taste profile") {
		t.Fatalf("context = %q", decision.Context)
	}
}

func TestAgentMayReadTheReviewQueue(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	state, err := store.Open(filepath.Join(root, "hamster.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()

	event := hook.Event{SessionID: "codex-session", AgentID: "ROOT", EventName: "PreToolUse",
		ToolName: "Bash", ToolUseID: "traps-read", WorkspaceRoot: root}
	decision, err := RegisterFromHook(ctx, state, hamsterruntime.New(state), event, Command{Name: "traps"})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Permission != "allow" {
		t.Fatalf("permission = %s, want allow", decision.Permission)
	}
	if !strings.Contains(decision.Context, "No Hamster trap is open") {
		t.Fatalf("context = %q", decision.Context)
	}
}
