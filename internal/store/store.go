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
  result_json BLOB,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS events (
  request_id TEXT PRIMARY KEY,
  run_id TEXT REFERENCES runs(id),
  kind TEXT NOT NULL,
  payload_json BLOB NOT NULL,
  decision_json BLOB NOT NULL,
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

CREATE INDEX IF NOT EXISTS events_run_id ON events(run_id, created_at);
CREATE INDEX IF NOT EXISTS runs_process_id ON runs(process_id, created_at);
`
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("initialize schema: %w", err)
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
	ID          string          `json:"id"`
	ProcessID   string          `json:"process_id"`
	ParentRunID string          `json:"parent_run_id,omitempty"`
	MachineName string          `json:"machine_name,omitempty"`
	Machine     ir.Machine      `json:"machine"`
	StateID     string          `json:"state_id"`
	Status      string          `json:"status"`
	Iteration   int             `json:"iteration"`
	Variables   json.RawMessage `json:"variables"`
	Result      json.RawMessage `json:"result,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
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
	row := s.db.QueryRowContext(ctx, `SELECT id, process_id, COALESCE(parent_run_id, ''), COALESCE(machine_name, ''), machine_json,
state_id, status, iteration, variables_json, result_json, created_at, updated_at FROM runs WHERE id = ?`, runID)
	return scanRun(row)
}

func (s *Store) FindCurrentRun(ctx context.Context, codexSessionID, agentID string) (*Run, error) {
	if agentID == "" {
		agentID = RootAgent
	}
	row := s.db.QueryRowContext(ctx, `SELECT r.id, r.process_id, COALESCE(r.parent_run_id, ''), COALESCE(r.machine_name, ''), r.machine_json,
r.state_id, r.status, r.iteration, r.variables_json, r.result_json, r.created_at, r.updated_at
FROM runs r
JOIN processes p ON p.current_run_id = r.id
JOIN sessions s ON s.id = p.session_id
WHERE s.codex_session_id = ? AND p.agent_id = ?`, codexSessionID, agentID)
	return scanRun(row)
}

type scanner interface {
	Scan(dest ...any) error
}

func scanRun(row scanner) (*Run, error) {
	var run Run
	var machineJSON []byte
	var variables string
	var result []byte
	var createdAt, updatedAt string
	if err := row.Scan(&run.ID, &run.ProcessID, &run.ParentRunID, &run.MachineName, &machineJSON,
		&run.StateID, &run.Status, &run.Iteration, &variables, &result, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	run.Variables = json.RawMessage(variables)
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

type EventDecision struct {
	Decision    string `json:"decision"`
	Reason      string `json:"reason,omitempty"`
	Remediation string `json:"remediation,omitempty"`
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
