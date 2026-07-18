package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"

	"hamster/internal/ir"
)

const RootAgent = "ROOT"

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create state directory: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open state database: %w", err)
	}
	// The daemon is the sole database owner. A single pooled connection keeps
	// connection-local pragmas deterministic while WAL still provides durable
	// crash recovery and permits external read-only inspection.
	db.SetMaxOpenConns(1)
	store := &Store{db: db}
	if err := store.initialize(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := store.Recover(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("recover state database: %w", err)
	}
	return store, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) initialize(ctx context.Context) error {
	pragmas := []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL",
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
	}
	for _, statement := range pragmas {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("configure sqlite: %w", err)
		}
	}

	const schema = `
CREATE TABLE IF NOT EXISTS schema_meta (
  version INTEGER NOT NULL
);
INSERT INTO schema_meta(version)
SELECT 1 WHERE NOT EXISTS (SELECT 1 FROM schema_meta);

CREATE TABLE IF NOT EXISTS workspaces (
  id TEXT PRIMARY KEY,
  root TEXT NOT NULL UNIQUE,
  created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
  id TEXT PRIMARY KEY,
  codex_session_id TEXT NOT NULL UNIQUE,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS processes (
  id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL REFERENCES sessions(id),
  agent_id TEXT NOT NULL,
  parent_process_id TEXT REFERENCES processes(id),
  current_run_id TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(session_id, agent_id)
);

CREATE TABLE IF NOT EXISTS runs (
  id TEXT PRIMARY KEY,
  process_id TEXT NOT NULL REFERENCES processes(id),
  parent_run_id TEXT REFERENCES runs(id),
  machine_name TEXT,
  machine_json BLOB NOT NULL,
  state_id TEXT NOT NULL,
  status TEXT NOT NULL,
  iteration INTEGER NOT NULL DEFAULT 0,
  variables_json BLOB NOT NULL DEFAULT '{}',
  stack_json BLOB NOT NULL DEFAULT '[]',
  result_json BLOB,
  termination_reason TEXT NOT NULL DEFAULT '',
  fork_key TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS events (
  request_id TEXT PRIMARY KEY,
  run_id TEXT REFERENCES runs(id),
  kind TEXT NOT NULL,
  payload_json BLOB NOT NULL,
  decision_json BLOB NOT NULL,
  verification_json BLOB,
  created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS run_limits (
  run_id TEXT NOT NULL REFERENCES runs(id),
  state_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  value_json BLOB NOT NULL,
  activated_at TEXT NOT NULL,
  activated_iteration INTEGER NOT NULL,
  PRIMARY KEY(run_id, state_id)
);

CREATE TABLE IF NOT EXISTS process_waits (
  parent_run_id TEXT NOT NULL REFERENCES runs(id),
  child_run_id TEXT NOT NULL REFERENCES runs(id),
  created_at TEXT NOT NULL,
  completed_at TEXT,
  PRIMARY KEY(parent_run_id, child_run_id)
);

CREATE TABLE IF NOT EXISTS pending_forks (
  fork_key TEXT PRIMARY KEY,
  session_id TEXT NOT NULL REFERENCES sessions(id),
  child_run_id TEXT NOT NULL REFERENCES runs(id),
  status TEXT NOT NULL,
  created_at TEXT NOT NULL,
  claimed_at TEXT
);

CREATE INDEX IF NOT EXISTS pending_forks_session ON pending_forks(session_id, status, created_at);

CREATE TABLE IF NOT EXISTS traces (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  codex_distribution TEXT NOT NULL,
  codex_version TEXT NOT NULL,
  session_filter TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL,
  started_at TEXT NOT NULL,
  stopped_at TEXT
);

CREATE TABLE IF NOT EXISTS trace_events (
  trace_id TEXT NOT NULL REFERENCES traces(id),
  sequence INTEGER NOT NULL,
  elapsed_ms INTEGER NOT NULL,
  input_json BLOB NOT NULL,
  decision_json BLOB,
  state_before_json BLOB,
  state_after_json BLOB,
  verification_json BLOB,
  PRIMARY KEY(trace_id, sequence)
);

CREATE INDEX IF NOT EXISTS events_run_id ON events(run_id, created_at);
CREATE INDEX IF NOT EXISTS runs_process_id ON runs(process_id, created_at);
CREATE UNIQUE INDEX IF NOT EXISTS one_active_trace ON traces(status) WHERE status = 'recording';
CREATE INDEX IF NOT EXISTS trace_events_trace_id ON trace_events(trace_id, sequence);
`
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("initialize schema: %w", err)
	}
	if err := s.ensureColumn(ctx, "runs", "stack_json", `ALTER TABLE runs ADD COLUMN stack_json BLOB NOT NULL DEFAULT '[]'`); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "trace_events", "verification_json", `ALTER TABLE trace_events ADD COLUMN verification_json BLOB`); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "runs", "termination_reason", `ALTER TABLE runs ADD COLUMN termination_reason TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "runs", "fork_key", `ALTER TABLE runs ADD COLUMN fork_key TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "events", "verification_json", `ALTER TABLE events ADD COLUMN verification_json BLOB`); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS unique_run_fork_key ON runs(fork_key) WHERE fork_key <> ''`); err != nil {
		return fmt.Errorf("create fork key index: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE schema_meta SET version = 7`); err != nil {
		return fmt.Errorf("update schema version: %w", err)
	}
	return nil
}

func (s *Store) ensureColumn(ctx context.Context, table, column, alteration string) error {
	rows, err := s.db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var index, notNull, primaryKey int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&index, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return err
		}
		if name == column {
			found = true
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if found {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, alteration); err != nil {
		return fmt.Errorf("migrate %s.%s: %w", table, column, err)
	}
	return nil
}

type CreateRunParams struct {
	WorkspaceRoot   string
	CodexSessionID  string
	AgentID         string
	ParentProcessID string
	ParentRunID     string
	Machine         ir.Machine
}

type Run struct {
	ID                string          `json:"id"`
	ProcessID         string          `json:"process_id"`
	ParentRunID       string          `json:"parent_run_id,omitempty"`
	MachineName       string          `json:"machine_name,omitempty"`
	Machine           ir.Machine      `json:"machine"`
	StateID           string          `json:"state_id"`
	Status            string          `json:"status"`
	Iteration         int             `json:"iteration"`
	Variables         json.RawMessage `json:"variables"`
	Stack             []Frame         `json:"stack"`
	Result            json.RawMessage `json:"result,omitempty"`
	TerminationReason string          `json:"termination_reason,omitempty"`
	WorkspaceRoot     string          `json:"workspace_root"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
	ResetProcessState bool            `json:"-"`
}

type Frame struct {
	Machine     ir.Machine      `json:"machine"`
	ResumeState string          `json:"resume_state"`
	Kind        string          `json:"kind"`
	StepName    string          `json:"step_name,omitempty"`
	Iteration   int             `json:"iteration"`
	Variables   json.RawMessage `json:"variables"`
}

func (s *Store) CreateRun(ctx context.Context, params CreateRunParams) (*Run, error) {
	if params.WorkspaceRoot == "" || params.CodexSessionID == "" {
		return nil, errors.New("workspace root and Codex session ID are required")
	}
	if params.AgentID == "" {
		params.AgentID = RootAgent
	}
	machineJSON, err := json.Marshal(params.Machine)
	if err != nil {
		return nil, fmt.Errorf("encode machine: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	now := time.Now().UTC()
	workspaceID, sessionID, processID := newID("workspace"), newID("session"), newID("process")
	if _, err = tx.ExecContext(ctx, `INSERT INTO workspaces(id, root, created_at) VALUES(?, ?, ?)
ON CONFLICT(root) DO NOTHING`, workspaceID, params.WorkspaceRoot, formatTime(now)); err != nil {
		return nil, fmt.Errorf("upsert workspace: %w", err)
	}
	if err = tx.QueryRowContext(ctx, `SELECT id FROM workspaces WHERE root = ?`, params.WorkspaceRoot).Scan(&workspaceID); err != nil {
		return nil, fmt.Errorf("resolve workspace: %w", err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO sessions(id, codex_session_id, workspace_id, created_at, updated_at)
VALUES(?, ?, ?, ?, ?) ON CONFLICT(codex_session_id) DO UPDATE SET workspace_id=excluded.workspace_id, updated_at=excluded.updated_at`,
		sessionID, params.CodexSessionID, workspaceID, formatTime(now), formatTime(now)); err != nil {
		return nil, fmt.Errorf("upsert session: %w", err)
	}
	if err = tx.QueryRowContext(ctx, `SELECT id FROM sessions WHERE codex_session_id = ?`, params.CodexSessionID).Scan(&sessionID); err != nil {
		return nil, fmt.Errorf("resolve session: %w", err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO processes(id, session_id, agent_id, parent_process_id, created_at, updated_at)
VALUES(?, ?, ?, NULLIF(?, ''), ?, ?) ON CONFLICT(session_id, agent_id) DO UPDATE SET updated_at=excluded.updated_at`,
		processID, sessionID, params.AgentID, params.ParentProcessID, formatTime(now), formatTime(now)); err != nil {
		return nil, fmt.Errorf("upsert process: %w", err)
	}
	if err = tx.QueryRowContext(ctx, `SELECT id FROM processes WHERE session_id = ? AND agent_id = ?`, sessionID, params.AgentID).Scan(&processID); err != nil {
		return nil, fmt.Errorf("resolve process: %w", err)
	}

	runID := newID("run")
	if _, err = tx.ExecContext(ctx, `INSERT INTO runs(id, process_id, parent_run_id, machine_name, machine_json, state_id, status, created_at, updated_at)
VALUES(?, ?, NULLIF(?, ''), ?, ?, ?, 'running', ?, ?)`, runID, processID, params.ParentRunID, params.Machine.Name, machineJSON, params.Machine.InitialState, formatTime(now), formatTime(now)); err != nil {
		return nil, fmt.Errorf("create run: %w", err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE processes SET current_run_id = ?, updated_at = ? WHERE id = ?`, runID, formatTime(now), processID); err != nil {
		return nil, fmt.Errorf("bind current run: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetRun(ctx, runID)
}

func (s *Store) GetRun(ctx context.Context, runID string) (*Run, error) {
	row := s.db.QueryRowContext(ctx, `SELECT r.id, r.process_id, COALESCE(r.parent_run_id, ''), COALESCE(r.machine_name, ''), r.machine_json,
r.state_id, r.status, r.iteration, r.variables_json, r.stack_json, r.result_json, r.termination_reason, r.created_at, r.updated_at, w.root
FROM runs r JOIN processes p ON p.id=r.process_id JOIN sessions s ON s.id=p.session_id JOIN workspaces w ON w.id=s.workspace_id
WHERE r.id = ?`, runID)
	return scanRun(row)
}

func (s *Store) FindCurrentRun(ctx context.Context, codexSessionID, agentID string) (*Run, error) {
	if agentID == "" {
		agentID = RootAgent
	}
	row := s.db.QueryRowContext(ctx, `SELECT r.id, r.process_id, COALESCE(r.parent_run_id, ''), COALESCE(r.machine_name, ''), r.machine_json,
	r.state_id, r.status, r.iteration, r.variables_json, r.stack_json, r.result_json, r.termination_reason, r.created_at, r.updated_at, w.root
FROM runs r
JOIN processes p ON p.current_run_id = r.id
JOIN sessions s ON s.id = p.session_id
JOIN workspaces w ON w.id = s.workspace_id
WHERE s.codex_session_id = ? AND p.agent_id = ?`, codexSessionID, agentID)
	return scanRun(row)
}

// KillCurrentRun is the emergency escape hatch for a Codex agent. It marks a
// running machine as killed and always removes the process binding in the same
// transaction, so later tool calls are no longer governed by that machine.
func (s *Store) KillCurrentRun(ctx context.Context, codexSessionID, agentID, reason string) (*Run, error) {
	if agentID == "" {
		agentID = RootAgent
	}
	if reason == "" {
		reason = "emergency kill switch"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var runID, status string
	err = tx.QueryRowContext(ctx, `SELECT r.id, r.status
FROM runs r
JOIN processes p ON p.current_run_id = r.id
JOIN sessions s ON s.id = p.session_id
WHERE s.codex_session_id = ? AND p.agent_id = ?`, codexSessionID, agentID).Scan(&runID, &status)
	if err != nil {
		return nil, err
	}
	now := formatTime(time.Now().UTC())
	if status == "running" {
		result, updateErr := tx.ExecContext(ctx, `UPDATE runs SET status='killed', termination_reason=?, updated_at=? WHERE id=? AND status='running'`, reason, now, runID)
		if updateErr != nil {
			return nil, updateErr
		}
		changed, updateErr := result.RowsAffected()
		if updateErr != nil {
			return nil, updateErr
		}
		if changed != 1 {
			return nil, errors.New("run changed concurrently")
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE processes SET current_run_id=NULL, updated_at=? WHERE current_run_id=?`, now, runID)
	if err != nil {
		return nil, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errors.New("run binding changed concurrently")
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetRun(ctx, runID)
}

type scanner interface {
	Scan(dest ...any) error
}

func scanRun(row scanner) (*Run, error) {
	var run Run
	var machineJSON []byte
	var variables string
	var stack string
	var result []byte
	var createdAt, updatedAt string
	if err := row.Scan(&run.ID, &run.ProcessID, &run.ParentRunID, &run.MachineName, &machineJSON,
		&run.StateID, &run.Status, &run.Iteration, &variables, &stack, &result, &run.TerminationReason, &createdAt, &updatedAt, &run.WorkspaceRoot); err != nil {
		return nil, err
	}
	run.Variables = json.RawMessage(variables)
	if err := json.Unmarshal([]byte(stack), &run.Stack); err != nil {
		return nil, fmt.Errorf("decode machine stack: %w", err)
	}
	if err := json.Unmarshal(machineJSON, &run.Machine); err != nil {
		return nil, fmt.Errorf("decode machine: %w", err)
	}
	run.Result = result
	var err error
	if run.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
		return nil, err
	}
	if run.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt); err != nil {
		return nil, err
	}
	return &run, nil
}

func (s *Store) UpdateRun(ctx context.Context, expectedState string, run *Run) error {
	machineJSON, err := json.Marshal(run.Machine)
	if err != nil {
		return err
	}
	stackJSON, err := json.Marshal(run.Stack)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE runs SET machine_name=?, machine_json=?, state_id=?, status=?, iteration=?,
variables_json=?, stack_json=?, result_json=?, termination_reason=?, updated_at=? WHERE id=? AND state_id=?`,
		run.Machine.Name, machineJSON, run.StateID, run.Status, run.Iteration, []byte(run.Variables), stackJSON,
		[]byte(run.Result), run.TerminationReason, formatTime(time.Now().UTC()), run.ID, expectedState)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return errors.New("run changed concurrently")
	}
	if run.ResetProcessState {
		if _, err := tx.ExecContext(ctx, `DELETE FROM run_limits WHERE run_id=?`, run.ID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM process_waits WHERE parent_run_id=?`, run.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ApplyEvent(ctx context.Context, requestID, kind string, payload json.RawMessage, decision EventDecision, expectedState string, run *Run) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	decisionJSON, err := json.Marshal(decision)
	if err != nil {
		return false, err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO events(request_id, run_id, kind, payload_json, decision_json, created_at)
VALUES(?, ?, ?, ?, ?, ?) ON CONFLICT(request_id) DO NOTHING`, requestID, run.ID, kind, []byte(payload), decisionJSON, formatTime(time.Now().UTC()))
	if err != nil {
		return false, err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if inserted == 0 {
		return true, nil
	}
	machineJSON, err := json.Marshal(run.Machine)
	if err != nil {
		return false, err
	}
	stackJSON, err := json.Marshal(run.Stack)
	if err != nil {
		return false, err
	}
	result, err = tx.ExecContext(ctx, `UPDATE runs SET machine_name=?, machine_json=?, state_id=?, status=?, iteration=?,
variables_json=?, stack_json=?, result_json=?, termination_reason=?, updated_at=? WHERE id=? AND state_id=?`,
		run.Machine.Name, machineJSON, run.StateID, run.Status, run.Iteration, []byte(run.Variables), stackJSON,
		[]byte(run.Result), run.TerminationReason, formatTime(time.Now().UTC()), run.ID, expectedState)
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if changed != 1 {
		return false, errors.New("run changed concurrently")
	}
	if run.ResetProcessState {
		if _, err := tx.ExecContext(ctx, `DELETE FROM run_limits WHERE run_id=?`, run.ID); err != nil {
			return false, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM process_waits WHERE parent_run_id=?`, run.ID); err != nil {
			return false, err
		}
	}
	return false, tx.Commit()
}

type EventDecision struct {
	Decision    string `json:"decision"`
	Reason      string `json:"reason,omitempty"`
	Remediation string `json:"remediation,omitempty"`
	Context     string `json:"context,omitempty"`
	RunID       string `json:"run_id,omitempty"`
}

func (s *Store) RecordEvent(ctx context.Context, requestID, runID, kind string, payload json.RawMessage, decision EventDecision) (EventDecision, bool, error) {
	decisionJSON, err := json.Marshal(decision)
	if err != nil {
		return EventDecision{}, false, err
	}
	now := formatTime(time.Now().UTC())
	result, err := s.db.ExecContext(ctx, `INSERT INTO events(request_id, run_id, kind, payload_json, decision_json, created_at)
VALUES(?, NULLIF(?, ''), ?, ?, ?, ?) ON CONFLICT(request_id) DO NOTHING`, requestID, runID, kind, []byte(payload), decisionJSON, now)
	if err != nil {
		return EventDecision{}, false, err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return EventDecision{}, false, err
	}
	if inserted == 1 {
		return decision, false, nil
	}
	var existing []byte
	if err := s.db.QueryRowContext(ctx, `SELECT decision_json FROM events WHERE request_id = ?`, requestID).Scan(&existing); err != nil {
		return EventDecision{}, false, err
	}
	if err := json.Unmarshal(existing, &decision); err != nil {
		return EventDecision{}, false, err
	}
	return decision, true, nil
}

func (s *Store) GetEventDecision(ctx context.Context, requestID string) (EventDecision, error) {
	var payload []byte
	if err := s.db.QueryRowContext(ctx, `SELECT decision_json FROM events WHERE request_id = ?`, requestID).Scan(&payload); err != nil {
		return EventDecision{}, err
	}
	var decision EventDecision
	if err := json.Unmarshal(payload, &decision); err != nil {
		return EventDecision{}, err
	}
	return decision, nil
}

type Trace struct {
	ID                string     `json:"id"`
	Name              string     `json:"name"`
	CodexDistribution string     `json:"codex_distribution"`
	CodexVersion      string     `json:"codex_version"`
	SessionFilter     string     `json:"session_filter,omitempty"`
	Status            string     `json:"status"`
	StartedAt         time.Time  `json:"started_at"`
	StoppedAt         *time.Time `json:"stopped_at,omitempty"`
}

type TraceEvent struct {
	Sequence      int             `json:"sequence"`
	ElapsedMS     int64           `json:"elapsed_ms"`
	Input         json.RawMessage `json:"input"`
	Decision      json.RawMessage `json:"decision,omitempty"`
	StateBefore   json.RawMessage `json:"state_before,omitempty"`
	StateAfter    json.RawMessage `json:"state_after,omitempty"`
	Verifications json.RawMessage `json:"verifications,omitempty"`
}

type TraceData struct {
	Trace  Trace        `json:"trace"`
	Events []TraceEvent `json:"events"`
}

func (s *Store) StartTrace(ctx context.Context, name, distribution, version, sessionFilter string) (*Trace, error) {
	if name == "" || distribution == "" || version == "" {
		return nil, errors.New("trace name, Codex distribution, and Codex version are required")
	}
	trace := &Trace{
		ID: newID("trace"), Name: name, CodexDistribution: distribution, CodexVersion: version,
		SessionFilter: sessionFilter, Status: "recording", StartedAt: time.Now().UTC(),
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO traces(id, name, codex_distribution, codex_version, session_filter, status, started_at)
VALUES(?, ?, ?, ?, ?, ?, ?)`, trace.ID, trace.Name, trace.CodexDistribution, trace.CodexVersion,
		trace.SessionFilter, trace.Status, formatTime(trace.StartedAt))
	if err != nil {
		return nil, fmt.Errorf("start trace: %w", err)
	}
	return trace, nil
}

func (s *Store) StopTrace(ctx context.Context, traceID string) (*Trace, error) {
	if traceID == "" {
		if err := s.db.QueryRowContext(ctx, `SELECT id FROM traces WHERE status='recording'`).Scan(&traceID); err != nil {
			return nil, err
		}
	}
	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `UPDATE traces SET status='complete', stopped_at=? WHERE id=? AND status='recording'`, formatTime(now), traceID)
	if err != nil {
		return nil, err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return nil, errors.New("trace is not recording")
	}
	return s.GetTrace(ctx, traceID)
}

func (s *Store) AppendActiveTrace(ctx context.Context, sessionID string, input, decision, before, after, verifications json.RawMessage) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var traceID, startedAt string
	var sessionFilter string
	err = tx.QueryRowContext(ctx, `SELECT id, started_at, session_filter FROM traces WHERE status='recording'`).Scan(&traceID, &startedAt, &sessionFilter)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	if sessionFilter != "" && sessionFilter != sessionID {
		return nil
	}
	started, err := time.Parse(time.RFC3339Nano, startedAt)
	if err != nil {
		return err
	}
	var sequence int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence), 0) + 1 FROM trace_events WHERE trace_id=?`, traceID).Scan(&sequence); err != nil {
		return err
	}
	elapsed := time.Since(started).Milliseconds()
	_, err = tx.ExecContext(ctx, `INSERT INTO trace_events(trace_id, sequence, elapsed_ms, input_json, decision_json, state_before_json, state_after_json, verification_json)
VALUES(?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''))`, traceID, sequence, elapsed,
		[]byte(input), []byte(decision), []byte(before), []byte(after), []byte(verifications))
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) GetTrace(ctx context.Context, traceID string) (*Trace, error) {
	var trace Trace
	var startedAt string
	var stoppedAt sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT id, name, codex_distribution, codex_version, session_filter, status, started_at, stopped_at
FROM traces WHERE id=?`, traceID).Scan(&trace.ID, &trace.Name, &trace.CodexDistribution, &trace.CodexVersion,
		&trace.SessionFilter, &trace.Status, &startedAt, &stoppedAt)
	if err != nil {
		return nil, err
	}
	trace.StartedAt, err = time.Parse(time.RFC3339Nano, startedAt)
	if err != nil {
		return nil, err
	}
	if stoppedAt.Valid {
		value, parseErr := time.Parse(time.RFC3339Nano, stoppedAt.String)
		if parseErr != nil {
			return nil, parseErr
		}
		trace.StoppedAt = &value
	}
	return &trace, nil
}

func (s *Store) GetTraceData(ctx context.Context, traceID string) (*TraceData, error) {
	trace, err := s.GetTrace(ctx, traceID)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT sequence, elapsed_ms, input_json, decision_json, state_before_json, state_after_json, verification_json
FROM trace_events WHERE trace_id=? ORDER BY sequence`, traceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	data := &TraceData{Trace: *trace}
	for rows.Next() {
		var event TraceEvent
		var input []byte
		var decision, before, after, verifications []byte
		if err := rows.Scan(&event.Sequence, &event.ElapsedMS, &input, &decision, &before, &after, &verifications); err != nil {
			return nil, err
		}
		event.Input, event.Decision, event.StateBefore, event.StateAfter = input, decision, before, after
		event.Verifications = verifications
		data.Events = append(data.Events, event)
	}
	return data, rows.Err()
}

func (s *Store) JournalMode(ctx context.Context) (string, error) {
	var mode string
	err := s.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode)
	return mode, err
}

func newID(prefix string) string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return prefix + "_" + hex.EncodeToString(bytes[:])
}

func formatTime(value time.Time) string { return value.Format(time.RFC3339Nano) }
