package store

import (
	"context"
	"fmt"
	"time"
)

type RecoveryReport struct {
	PendingForksRebuilt int64 `json:"pending_forks_rebuilt"`
	WaitersWoken        int64 `json:"waiters_woken"`
	WaitsCompleted      int64 `json:"waits_completed"`
	BindingsReleased    int64 `json:"bindings_released"`
}

// Recover repairs transaction boundaries that intentionally span a runtime
// state update and process finalization. Every operation is idempotent, so the
// daemon can run recovery on every open without guessing whether it crashed.
func (s *Store) Recover(ctx context.Context) (RecoveryReport, error) {
	var report RecoveryReport
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return report, err
	}
	defer tx.Rollback()
	now := formatTime(time.Now().UTC())

	result, err := tx.ExecContext(ctx, `INSERT INTO pending_forks(fork_key, session_id, child_run_id, status, created_at)
SELECT r.fork_key, p.session_id, r.id, 'pending', r.created_at
FROM runs r JOIN processes p ON p.id=r.process_id
WHERE r.fork_key<>'' AND p.agent_id=('PENDING:' || r.fork_key)
ON CONFLICT(fork_key) DO NOTHING`)
	if err != nil {
		return report, fmt.Errorf("rebuild pending forks: %w", err)
	}
	report.PendingForksRebuilt, _ = result.RowsAffected()

	result, err = tx.ExecContext(ctx, `UPDATE runs SET status='running', updated_at=?
WHERE status='waiting' AND id IN (
  SELECT w.parent_run_id FROM process_waits w JOIN runs child ON child.id=w.child_run_id
  WHERE w.completed_at IS NULL AND child.status IN ('complete', 'killed', 'failed', 'limit_exceeded')
)`, now)
	if err != nil {
		return report, fmt.Errorf("wake recovered waiters: %w", err)
	}
	report.WaitersWoken, _ = result.RowsAffected()

	result, err = tx.ExecContext(ctx, `UPDATE process_waits SET completed_at=?
WHERE completed_at IS NULL AND child_run_id IN (
  SELECT id FROM runs WHERE status IN ('complete', 'killed', 'failed', 'limit_exceeded')
)`, now)
	if err != nil {
		return report, fmt.Errorf("complete recovered waits: %w", err)
	}
	report.WaitsCompleted, _ = result.RowsAffected()

	result, err = tx.ExecContext(ctx, `UPDATE processes SET current_run_id=NULL, updated_at=?
WHERE current_run_id IN (
  SELECT id FROM runs WHERE status IN ('complete', 'killed', 'failed', 'limit_exceeded')
)`, now)
	if err != nil {
		return report, fmt.Errorf("release terminal bindings: %w", err)
	}
	report.BindingsReleased, _ = result.RowsAffected()

	if err := tx.Commit(); err != nil {
		return report, err
	}
	return report, nil
}
