package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestOpenRecoversTerminalBindingsWaitersAndPendingForks(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "hamster.db")
	state, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := state.CreateRun(ctx, CreateRunParams{WorkspaceRoot: "/repo", CodexSessionID: "session-recovery", Machine: testMachine()})
	if err != nil {
		t.Fatal(err)
	}
	child, err := state.ForkRun(ctx, parent.ID, "child-recovery", "completed-child")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.ForkRun(ctx, parent.ID, "PENDING:recovered-token", "recovered-token"); err != nil {
		t.Fatal(err)
	}
	if _, err := state.WaitRun(ctx, parent.ID, child.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := state.db.ExecContext(ctx, `UPDATE runs SET status='complete', result_json=? WHERE id=?`, []byte(`"done"`), child.ID); err != nil {
		t.Fatal(err)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := reopened.FindCurrentRun(ctx, "session-recovery", "child-recovery"); err != sql.ErrNoRows {
		t.Fatalf("terminal child remains bound: %v", err)
	}
	resumed, err := reopened.GetRun(ctx, parent.ID)
	if err != nil || resumed.Status != "running" {
		t.Fatalf("resumed parent = %#v, %v", resumed, err)
	}
	waited, err := reopened.WaitRun(ctx, parent.ID, child.ID)
	if err != nil || !waited.Ready || string(waited.Result) != `"done"` {
		t.Fatalf("recovered wait = %#v, %v", waited, err)
	}
	pending, err := reopened.HasPendingFork(ctx, "session-recovery")
	if err != nil || !pending {
		t.Fatalf("recovered pending fork = %v, %v", pending, err)
	}
	report, err := reopened.Recover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(report)
	quiescent, _ := json.Marshal(RecoveryReport{})
	if string(payload) != string(quiescent) {
		t.Fatalf("second recovery was not idempotent: %s", payload)
	}
}
