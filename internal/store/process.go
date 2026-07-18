package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"hamster/internal/ir"
)

type WaitResult struct {
	Ready       bool            `json:"ready"`
	ChildRunID  string          `json:"child_run_id"`
	ChildStatus string          `json:"child_status"`
	Result      json.RawMessage `json:"result,omitempty"`
}

// PrepareFork snapshots the parent before Codex starts a child whose agent ID
// is not known yet. The fork key is injected into the child prompt and later
// presented to ClaimFork, avoiding any reliance on lifecycle event ordering.
func (s *Store) PrepareFork(ctx context.Context, parentRunID, forkKey string) (*Run, error) {
	child, err := s.ForkRun(ctx, parentRunID, "PENDING:"+forkKey, forkKey)
	if err != nil {
		return nil, err
	}
	now := formatTime(time.Now().UTC())
	_, err = s.db.ExecContext(ctx, `INSERT INTO pending_forks(fork_key, session_id, child_run_id, status, created_at)
SELECT ?, p.session_id, ?, 'pending', ? FROM processes p JOIN runs r ON r.process_id=p.id WHERE r.id=?
ON CONFLICT(fork_key) DO NOTHING`, forkKey, child.ID, now, child.ID)
	if err != nil {
		return nil, err
	}
	return child, nil
}

func (s *Store) FindFork(ctx context.Context, forkKey string) (*Run, error) {
	if forkKey == "" {
		return nil, errors.New("fork key is required")
	}
	var childRunID string
	if err := s.db.QueryRowContext(ctx, `SELECT child_run_id FROM pending_forks WHERE fork_key=?`, forkKey).Scan(&childRunID); err != nil {
		return nil, err
	}
	return s.GetRun(ctx, childRunID)
}

func (s *Store) ClaimFork(ctx context.Context, codexSessionID, childAgentID, forkKey string) (*Run, error) {
	if codexSessionID == "" || childAgentID == "" || forkKey == "" {
		return nil, errors.New("Codex session ID, child agent ID, and fork token are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var existingRunID, existingForkKey string
	err = tx.QueryRowContext(ctx, `SELECT r.id, r.fork_key FROM runs r
JOIN processes p ON p.current_run_id=r.id
JOIN sessions s ON s.id=p.session_id
WHERE s.codex_session_id=? AND p.agent_id=?`, codexSessionID, childAgentID).Scan(&existingRunID, &existingForkKey)
	if err == nil {
		if existingForkKey != forkKey {
			return nil, errors.New("Codex agent is already bound to a different Hamster fork")
		}
		if err := tx.Rollback(); err != nil {
			return nil, err
		}
		return s.GetRun(ctx, existingRunID)
	}
	if err != sql.ErrNoRows {
		return nil, err
	}
	var childRunID, processID, status, currentAgentID string
	err = tx.QueryRowContext(ctx, `SELECT pf.child_run_id, r.process_id, pf.status, p.agent_id
FROM pending_forks pf
JOIN sessions s ON s.id=pf.session_id
JOIN runs r ON r.id=pf.child_run_id
JOIN processes p ON p.id=r.process_id
WHERE s.codex_session_id=? AND pf.fork_key=?`, codexSessionID, forkKey).Scan(&childRunID, &processID, &status, &currentAgentID)
	if err != nil {
		return nil, err
	}
	if status == "claimed" {
		if currentAgentID != childAgentID {
			return nil, errors.New("fork token was already claimed by a different Codex agent")
		}
		if err := tx.Rollback(); err != nil {
			return nil, err
		}
		return s.GetRun(ctx, childRunID)
	}
	if status != "pending" {
		return nil, fmt.Errorf("fork token has status %s", status)
	}
	now := formatTime(time.Now().UTC())
	if _, err := tx.ExecContext(ctx, `UPDATE processes SET agent_id=?, updated_at=? WHERE id=?`, childAgentID, now, processID); err != nil {
		return nil, fmt.Errorf("bind claimed child agent: %w", err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE pending_forks SET status='claimed', claimed_at=? WHERE fork_key=? AND status='pending'`, now, forkKey)
	if err != nil {
		return nil, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errors.New("pending fork changed concurrently")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_quarantines SET cleared_at=?
WHERE session_id=(SELECT id FROM sessions WHERE codex_session_id=?) AND agent_id=? AND cleared_at IS NULL`, now, codexSessionID, childAgentID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetRun(ctx, childRunID)
}

func (s *Store) HasPendingFork(ctx context.Context, codexSessionID string) (bool, error) {
	var exists int
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(
SELECT 1 FROM pending_forks pf JOIN sessions s ON s.id=pf.session_id
WHERE s.codex_session_id=? AND pf.status='pending')`, codexSessionID).Scan(&exists)
	return exists == 1, err
}

func (s *Store) QuarantineSubagent(ctx context.Context, codexSessionID, childAgentID string) (bool, error) {
	if codexSessionID == "" || childAgentID == "" {
		return false, errors.New("Codex session ID and child agent ID are required")
	}
	var managed int
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(
SELECT 1 FROM processes p
JOIN sessions s ON s.id=p.session_id
JOIN runs r ON r.id=p.current_run_id
WHERE s.codex_session_id=? AND r.status IN ('running', 'waiting')
UNION ALL
SELECT 1 FROM pending_forks pf JOIN sessions s ON s.id=pf.session_id
WHERE s.codex_session_id=? AND pf.status='pending')`, codexSessionID, codexSessionID).Scan(&managed)
	if err != nil || managed == 0 {
		return false, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO agent_quarantines(session_id, agent_id, created_at)
SELECT id, ?, ? FROM sessions WHERE codex_session_id=?
ON CONFLICT(session_id, agent_id) DO UPDATE SET cleared_at=NULL`, childAgentID, formatTime(time.Now().UTC()), codexSessionID)
	return err == nil, err
}

func (s *Store) IsSubagentQuarantined(ctx context.Context, codexSessionID, childAgentID string) (bool, error) {
	var exists int
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(
SELECT 1 FROM agent_quarantines q JOIN sessions s ON s.id=q.session_id
WHERE s.codex_session_id=? AND q.agent_id=? AND q.cleared_at IS NULL)`, codexSessionID, childAgentID).Scan(&exists)
	return exists == 1, err
}

// ForkRun clones the complete execution state into a new child process. The
// fork key makes replayed Codex spawn events idempotent.
func (s *Store) ForkRun(ctx context.Context, parentRunID, childAgentID, forkKey string) (*Run, error) {
	if parentRunID == "" || childAgentID == "" || forkKey == "" {
		return nil, errors.New("parent run ID, child agent ID, and fork key are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var existingRunID string
	err = tx.QueryRowContext(ctx, `SELECT id FROM runs WHERE fork_key=?`, forkKey).Scan(&existingRunID)
	if err == nil {
		if err := tx.Rollback(); err != nil {
			return nil, err
		}
		return s.GetRun(ctx, existingRunID)
	}
	if err != sql.ErrNoRows {
		return nil, err
	}

	var parentProcessID, sessionID, machineName, stateID, status string
	var machineJSON, variablesJSON, stackJSON, resultJSON []byte
	var iteration int
	err = tx.QueryRowContext(ctx, `SELECT r.process_id, p.session_id, COALESCE(r.machine_name, ''), r.machine_json,
r.state_id, r.status, r.iteration, r.variables_json, r.stack_json, r.result_json
FROM runs r JOIN processes p ON p.id=r.process_id WHERE r.id=?`, parentRunID).
		Scan(&parentProcessID, &sessionID, &machineName, &machineJSON, &stateID, &status, &iteration, &variablesJSON, &stackJSON, &resultJSON)
	if err != nil {
		return nil, err
	}
	if status != "running" {
		return nil, fmt.Errorf("cannot fork run %s with status %s", parentRunID, status)
	}

	now := formatTime(time.Now().UTC())
	processID := newID("process")
	if _, err := tx.ExecContext(ctx, `INSERT INTO processes(id, session_id, agent_id, parent_process_id, created_at, updated_at)
VALUES(?, ?, ?, ?, ?, ?)`, processID, sessionID, childAgentID, parentProcessID, now, now); err != nil {
		return nil, fmt.Errorf("create child process: %w", err)
	}
	runID := newID("run")
	if _, err := tx.ExecContext(ctx, `INSERT INTO runs(id, process_id, parent_run_id, machine_name, machine_json, state_id,
status, iteration, variables_json, stack_json, result_json, fork_key, created_at, updated_at)
VALUES(?, ?, ?, ?, ?, ?, 'running', ?, ?, ?, ?, ?, ?, ?)`, runID, processID, parentRunID, machineName,
		machineJSON, stateID, iteration, variablesJSON, stackJSON, resultJSON, forkKey, now, now); err != nil {
		return nil, fmt.Errorf("clone child run: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE processes SET current_run_id=? WHERE id=?`, runID, processID); err != nil {
		return nil, fmt.Errorf("bind child process: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO run_limits(run_id, state_id, kind, value_json, activated_at, activated_iteration)
SELECT ?, state_id, kind, value_json, activated_at, activated_iteration FROM run_limits WHERE run_id=?`, runID, parentRunID); err != nil {
		return nil, fmt.Errorf("clone run limits: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetRun(ctx, runID)
}

// ExecRun replaces the machine inside an existing process without changing
// its process or run identity.
func (s *Store) ExecRun(ctx context.Context, runID string, machine ir.Machine, variables json.RawMessage) (*Run, error) {
	if runID == "" || machine.InitialState == "" {
		return nil, errors.New("run ID and executable machine are required")
	}
	if len(variables) == 0 {
		variables = json.RawMessage(`{}`)
	}
	if !json.Valid(variables) {
		return nil, errors.New("exec variables must be valid JSON")
	}
	machineJSON, err := json.Marshal(machine)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := formatTime(time.Now().UTC())
	result, err := tx.ExecContext(ctx, `UPDATE runs SET machine_name=?, machine_json=?, state_id=?, status='running', iteration=0,
variables_json=?, stack_json='[]', result_json=NULL, termination_reason='', updated_at=?
WHERE id=? AND status IN ('running', 'waiting')`, machine.Name, machineJSON, machine.InitialState, []byte(variables), now, runID)
	if err != nil {
		return nil, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errors.New("run is not executable")
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM run_limits WHERE run_id=?`, runID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM process_waits WHERE parent_run_id=?`, runID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetRun(ctx, runID)
}

// WaitRun suspends a parent until its direct child reaches a terminal state.
func (s *Store) WaitRun(ctx context.Context, parentRunID, childRunID string) (WaitResult, error) {
	result := WaitResult{ChildRunID: childRunID}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	var recordedParentID string
	var childResult []byte
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(parent_run_id, ''), status, result_json FROM runs WHERE id=?`, childRunID).
		Scan(&recordedParentID, &result.ChildStatus, &childResult)
	if err != nil {
		return result, err
	}
	if recordedParentID != parentRunID {
		return result, errors.New("wait target is not a direct child of the parent run")
	}
	if terminalProcessStatus(result.ChildStatus) {
		result.Ready = true
		result.Result = childResult
		return result, nil
	}
	var parentStatus string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM runs WHERE id=?`, parentRunID).Scan(&parentStatus); err != nil {
		return result, err
	}
	if parentStatus != "running" && parentStatus != "waiting" {
		return result, fmt.Errorf("parent run has terminal status %s", parentStatus)
	}
	now := formatTime(time.Now().UTC())
	if _, err := tx.ExecContext(ctx, `INSERT INTO process_waits(parent_run_id, child_run_id, created_at)
VALUES(?, ?, ?) ON CONFLICT(parent_run_id, child_run_id) DO NOTHING`, parentRunID, childRunID, now); err != nil {
		return result, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET status='waiting', updated_at=? WHERE id=?`, now, parentRunID); err != nil {
		return result, err
	}
	if err := tx.Commit(); err != nil {
		return result, err
	}
	return result, nil
}

// ReturnRun terminates a child with a structured value and wakes every parent
// waiting on it. Replaying the same return is safe; changing it is rejected.
func (s *Store) ReturnRun(ctx context.Context, runID string, value json.RawMessage) (*Run, error) {
	if !json.Valid(value) {
		return nil, errors.New("return value must be valid JSON")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var status, processID string
	var existing []byte
	if err := tx.QueryRowContext(ctx, `SELECT status, process_id, result_json FROM runs WHERE id=?`, runID).Scan(&status, &processID, &existing); err != nil {
		return nil, err
	}
	if status == "complete" {
		if !bytes.Equal(existing, value) {
			return nil, errors.New("run already returned a different value")
		}
		if err := tx.Rollback(); err != nil {
			return nil, err
		}
		return s.GetRun(ctx, runID)
	}
	if status != "running" && status != "waiting" {
		return nil, fmt.Errorf("cannot return from run with status %s", status)
	}
	now := formatTime(time.Now().UTC())
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET status='complete', result_json=?, updated_at=? WHERE id=?`, []byte(value), now, runID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE processes SET current_run_id=NULL, updated_at=? WHERE id=? AND current_run_id=?`, now, processID, runID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET status='running', updated_at=?
WHERE id IN (SELECT parent_run_id FROM process_waits WHERE child_run_id=? AND completed_at IS NULL) AND status='waiting'`, now, runID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE process_waits SET completed_at=? WHERE child_run_id=? AND completed_at IS NULL`, now, runID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetRun(ctx, runID)
}

// FinalizeRun releases a terminal process binding and wakes its waiters. The
// runtime calls it after atomically persisting normal loop completion.
func (s *Store) FinalizeRun(ctx context.Context, runID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status, processID string
	if err := tx.QueryRowContext(ctx, `SELECT status, process_id FROM runs WHERE id=?`, runID).Scan(&status, &processID); err != nil {
		return err
	}
	if !terminalProcessStatus(status) {
		return fmt.Errorf("cannot finalize non-terminal run with status %s", status)
	}
	now := formatTime(time.Now().UTC())
	if _, err := tx.ExecContext(ctx, `UPDATE processes SET current_run_id=NULL, updated_at=? WHERE id=? AND current_run_id=?`, now, processID, runID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET status='running', updated_at=?
WHERE id IN (SELECT parent_run_id FROM process_waits WHERE child_run_id=? AND completed_at IS NULL) AND status='waiting'`, now, runID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE process_waits SET completed_at=? WHERE child_run_id=? AND completed_at IS NULL`, now, runID); err != nil {
		return err
	}
	return tx.Commit()
}

func terminalProcessStatus(status string) bool {
	return status == "complete" || status == "killed" || status == "failed" || status == "limit_exceeded"
}
