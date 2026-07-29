package store

import (
	"context"
	"path/filepath"
	"testing"
)

func tasteStore(t *testing.T) (*Store, string) {
	t.Helper()
	state, err := Open(filepath.Join(t.TempDir(), "hamster.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { state.Close() })
	return state, t.TempDir()
}

// A draft is only a proposal. An automated judge reads the ratified profile, so
// an unratified clause must never reach it — otherwise the agent would be
// supplying the standard it is graded against.
func TestDraftTasteIsInvisibleToTheRatifiedProfile(t *testing.T) {
	ctx := context.Background()
	state, root := tasteStore(t)
	if _, err := state.RecordTaste(ctx, RecordTasteParams{
		WorkspaceRoot: root, Name: "error_style", Clause: "Wrap with %w.",
	}); err != nil {
		t.Fatal(err)
	}
	ratified, err := state.ListTaste(ctx, root, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(ratified) != 0 {
		t.Fatalf("ratified clauses = %d, want 0 before ratification", len(ratified))
	}
	all, err := state.ListTaste(ctx, root, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].Status != TasteDraft {
		t.Fatalf("clauses = %#v", all)
	}

	if _, err := state.RatifyTaste(ctx, root, ""); err != nil {
		t.Fatal(err)
	}
	ratified, err = state.ListTaste(ctx, root, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(ratified) != 1 || ratified[0].RatifiedAt == nil {
		t.Fatalf("ratified = %#v", ratified)
	}
}

// A proposal never changes the standing clause. It sits beside it as a draft,
// so an agent can suggest loosening the rubric it is judged by but the standard
// it is actually judged against only changes by an explicit human act.
func TestProposalNeverChangesTheStandingClause(t *testing.T) {
	ctx := context.Background()
	state, root := tasteStore(t)
	if _, err := state.RecordTaste(ctx, RecordTasteParams{
		WorkspaceRoot: root, Name: "error_style", Clause: "Wrap with %w.",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := state.RatifyTaste(ctx, root, "error_style"); err != nil {
		t.Fatal(err)
	}

	// The agent proposes a weaker standard. This must be storable — refining
	// taste is normal — but it must not take effect.
	if _, err := state.RecordTaste(ctx, RecordTasteParams{
		WorkspaceRoot: root, Name: "error_style", Clause: "Anything is fine.",
	}); err != nil {
		t.Fatal(err)
	}
	standing, err := state.ListTaste(ctx, root, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(standing) != 1 || standing[0].Clause != "Wrap with %w." {
		t.Fatalf("standing profile = %#v; a proposal changed the standard", standing)
	}

	// Adoption is the human act, and it supersedes what it refines.
	if _, err := state.RatifyTaste(ctx, root, "error_style"); err != nil {
		t.Fatal(err)
	}
	standing, err = state.ListTaste(ctx, root, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(standing) != 1 || standing[0].Clause != "Anything is fine." {
		t.Fatalf("standing profile = %#v; adoption did not supersede the old clause", standing)
	}
}

func TestDraftRefinementIsVisibleBesideTheStandingClause(t *testing.T) {
	ctx := context.Background()
	state, root := tasteStore(t)
	if _, err := state.RecordTaste(ctx, RecordTasteParams{
		WorkspaceRoot: root, Name: "error_style", Clause: "Wrap with %w.",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := state.RatifyTaste(ctx, root, "error_style"); err != nil {
		t.Fatal(err)
	}
	if _, err := state.RecordTaste(ctx, RecordTasteParams{
		WorkspaceRoot: root, Name: "error_style", Clause: "Also annotate the sentinel.", Source: TasteVerdict,
	}); err != nil {
		t.Fatal(err)
	}
	all, err := state.ListTaste(ctx, root, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("clauses = %#v; a refinement must be visible beside what it refines", all)
	}

	// Revoking withdraws the whole name, so a bad standard is correctable.
	if err := state.RevokeTaste(ctx, root, "error_style"); err != nil {
		t.Fatal(err)
	}
	remaining, err := state.ListTaste(ctx, root, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 0 {
		t.Fatalf("clauses after revoke = %#v", remaining)
	}
}

func TestTasteProfileIsScopedToItsRepository(t *testing.T) {
	ctx := context.Background()
	state, root := tasteStore(t)
	other := t.TempDir()
	for _, workspace := range []string{root, other} {
		if _, err := state.RecordTaste(ctx, RecordTasteParams{
			WorkspaceRoot: workspace, Name: "naming", Clause: "Match the package.",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := state.RatifyTaste(ctx, root, ""); err != nil {
		t.Fatal(err)
	}
	here, err := state.ListTaste(ctx, root, true)
	if err != nil {
		t.Fatal(err)
	}
	there, err := state.ListTaste(ctx, other, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(here) != 1 || len(there) != 0 {
		t.Fatalf("ratifying one repository leaked into another: %d here, %d there", len(here), len(there))
	}
}

func TestTasteRejectsEmptyAndUnknownSources(t *testing.T) {
	ctx := context.Background()
	state, root := tasteStore(t)
	if _, err := state.RecordTaste(ctx, RecordTasteParams{
		WorkspaceRoot: root, Name: "empty", Clause: "   ",
	}); err == nil {
		t.Fatal("an empty clause states no standard and must be rejected")
	}
	if _, err := state.RecordTaste(ctx, RecordTasteParams{
		WorkspaceRoot: root, Name: "bad", Clause: "text", Source: "vibes",
	}); err == nil {
		t.Fatal("an unknown provenance must be rejected")
	}
}
