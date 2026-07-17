package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	"hamster/internal/ir"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "hamster.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func testMachine() ir.Machine {
	return ir.Machine{
		ID: "machine_0", Name: "test", Kind: ir.LoopFlow, InitialState: "state_0",
		States: []ir.State{{ID: "state_0", Kind: ir.StateComplete, Executor: ir.ExecutorControl}},
	}
}

func TestOpenEnablesWAL(t *testing.T) {
	store := testStore(t)
	mode, err := store.JournalMode(context.Background())
	if err != nil {
		t.Fatalf("JournalMode() error = %v", err)
	}
	if mode != "wal" {
		t.Fatalf("journal mode = %q, want wal", mode)
	}
}

func TestCreateAndResolveRootRun(t *testing.T) {
	store := testStore(t)
	run, err := store.CreateRun(context.Background(), CreateRunParams{
		WorkspaceRoot: "/repo", CodexSessionID: "session-1", Machine: testMachine(),
	})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	resolved, err := store.FindCurrentRun(context.Background(), "session-1", "")
	if err != nil {
		t.Fatalf("FindCurrentRun() error = %v", err)
	}
	if resolved.ID != run.ID || resolved.Machine.Name != "test" {
		t.Fatalf("resolved = %#v, want run %q", resolved, run.ID)
	}
	if _, err := store.FindCurrentRun(context.Background(), "session-1", "child"); err != sql.ErrNoRows {
		t.Fatalf("missing child error = %v, want sql.ErrNoRows", err)
	}
}

func TestRecordEventIsIdempotent(t *testing.T) {
	store := testStore(t)
	first := EventDecision{Decision: "allow", Reason: "first"}
	got, replayed, err := store.RecordEvent(context.Background(), "request-1", "", "ping", json.RawMessage(`{}`), first)
	if err != nil || replayed || got != first {
		t.Fatalf("first RecordEvent() = %#v, %v, %v", got, replayed, err)
	}
	got, replayed, err = store.RecordEvent(context.Background(), "request-1", "", "ping", json.RawMessage(`{}`), EventDecision{Decision: "deny"})
	if err != nil || !replayed || got != first {
		t.Fatalf("replayed RecordEvent() = %#v, %v, %v", got, replayed, err)
	}
}
