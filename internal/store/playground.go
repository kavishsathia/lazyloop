package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const (
	PlaygroundStarting  = "starting"
	PlaygroundRunning   = "running"
	PlaygroundComplete  = "complete"
	PlaygroundFailed    = "failed"
	PlaygroundCancelled = "cancelled"
)

// PlaygroundRun is one experiment: a loop, a prompt, and the standards it was
// judged against, executed in a workspace of its own so two runs of the same
// task start from identical state and stay comparable.
type PlaygroundRun struct {
	ID            string            `json:"id"`
	Name          string            `json:"name,omitempty"`
	Script        string            `json:"script"`
	Prompt        string            `json:"prompt"`
	Taste         []PlaygroundTaste `json:"taste"`
	WorkspaceRoot string            `json:"workspace_root"`
	Status        string            `json:"status"`
	ExitCode      *int              `json:"exit_code,omitempty"`
	Error         string            `json:"error,omitempty"`
	CreatedAt     time.Time         `json:"created_at"`
	FinishedAt    *time.Time        `json:"finished_at,omitempty"`
}

type PlaygroundTaste struct {
	Name     string `json:"name"`
	Question string `json:"question,omitempty"`
	Clause   string `json:"clause"`
}

type PlaygroundEvent struct {
	Sequence  int             `json:"sequence"`
	Stream    string          `json:"stream"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"created_at"`
}

type CreatePlaygroundParams struct {
	Name          string
	Script        string
	Prompt        string
	Taste         []PlaygroundTaste
	WorkspaceRoot string
}

func (s *Store) CreatePlaygroundRun(ctx context.Context, params CreatePlaygroundParams) (*PlaygroundRun, error) {
	if params.Script == "" || params.Prompt == "" || params.WorkspaceRoot == "" {
		return nil, errors.New("a playground run needs a script, a prompt, and a workspace")
	}
	tasteJSON, err := json.Marshal(append([]PlaygroundTaste{}, params.Taste...))
	if err != nil {
		return nil, err
	}
	id := newID("play")
	_, err = s.db.ExecContext(ctx, `INSERT INTO playground_runs(id, name, script, prompt, taste_json, workspace_root, status, created_at)
VALUES(?, ?, ?, ?, ?, ?, ?, ?)`, id, params.Name, params.Script, params.Prompt, tasteJSON,
		params.WorkspaceRoot, PlaygroundStarting, formatTime(time.Now().UTC()))
	if err != nil {
		return nil, fmt.Errorf("create playground run: %w", err)
	}
	return s.GetPlaygroundRun(ctx, id)
}

func (s *Store) SetPlaygroundStatus(ctx context.Context, id, status, message string, exitCode *int) error {
	finished := sql.NullString{}
	if status != PlaygroundStarting && status != PlaygroundRunning {
		finished = sql.NullString{String: formatTime(time.Now().UTC()), Valid: true}
	}
	code := sql.NullInt64{}
	if exitCode != nil {
		code = sql.NullInt64{Int64: int64(*exitCode), Valid: true}
	}
	_, err := s.db.ExecContext(ctx, `UPDATE playground_runs SET status=?, error=?, exit_code=?, finished_at=COALESCE(?, finished_at) WHERE id=?`,
		status, message, code, finished, id)
	return err
}

func (s *Store) AppendPlaygroundEvent(ctx context.Context, id, stream string, payload json.RawMessage) error {
	if !json.Valid(payload) {
		payload, _ = json.Marshal(string(payload))
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO playground_events(playground_run_id, sequence, stream, payload_json, created_at)
VALUES(?, (SELECT COALESCE(MAX(sequence), 0) + 1 FROM playground_events WHERE playground_run_id=?), ?, ?, ?)`,
		id, id, stream, []byte(payload), formatTime(time.Now().UTC()))
	return err
}

func (s *Store) GetPlaygroundRun(ctx context.Context, id string) (*PlaygroundRun, error) {
	row := s.db.QueryRowContext(ctx, playgroundColumns+` WHERE id=?`, id)
	return scanPlaygroundRun(row)
}

func (s *Store) ListPlaygroundRuns(ctx context.Context, limit int) ([]PlaygroundRun, error) {
	if limit <= 0 {
		limit = 25
	}
	rows, err := s.db.QueryContext(ctx, playgroundColumns+` ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runs := []PlaygroundRun{}
	for rows.Next() {
		run, err := scanPlaygroundRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, *run)
	}
	return runs, rows.Err()
}

// PlaygroundEventsAfter returns only what the browser has not seen, so the page
// can follow a live run without re-reading the whole transcript each poll.
func (s *Store) PlaygroundEventsAfter(ctx context.Context, id string, after int) ([]PlaygroundEvent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT sequence, stream, payload_json, created_at
FROM playground_events WHERE playground_run_id=? AND sequence>? ORDER BY sequence`, id, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []PlaygroundEvent{}
	for rows.Next() {
		var event PlaygroundEvent
		var payload []byte
		var createdAt string
		if err := rows.Scan(&event.Sequence, &event.Stream, &payload, &createdAt); err != nil {
			return nil, err
		}
		event.Payload = payload
		if event.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

// PlaygroundDecisions returns what Hamster did while this experiment ran. The
// workspace is unique per run, which is what ties a Codex session back to the
// experiment that started it.
func (s *Store) PlaygroundDecisions(ctx context.Context, workspaceRoot string) ([]ObservedEvent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT e.kind, e.payload_json, e.decision_json, e.created_at
FROM events e
JOIN runs r ON r.id=e.run_id
JOIN processes p ON p.id=r.process_id
JOIN sessions s ON s.id=p.session_id
JOIN workspaces w ON w.id=s.workspace_id
WHERE w.root=? ORDER BY e.created_at`, workspaceRoot)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	observed := []ObservedEvent{}
	for rows.Next() {
		var event ObservedEvent
		var payload, decision []byte
		var createdAt string
		if err := rows.Scan(&event.Kind, &payload, &decision, &createdAt); err != nil {
			return nil, err
		}
		event.Payload, event.Decision = payload, decision
		if event.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
			return nil, err
		}
		observed = append(observed, event)
	}
	return observed, rows.Err()
}

type ObservedEvent struct {
	Kind      string          `json:"kind"`
	Payload   json.RawMessage `json:"payload"`
	Decision  json.RawMessage `json:"decision"`
	CreatedAt time.Time       `json:"created_at"`
}

// PlaygroundRunsForWorkspace reports the Hamster runs an experiment produced.
func (s *Store) PlaygroundRunsForWorkspace(ctx context.Context, workspaceRoot string) ([]Run, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT r.id, r.process_id, COALESCE(r.parent_run_id, ''), COALESCE(r.machine_name, ''), r.machine_json,
r.state_id, r.status, r.iteration, r.variables_json, r.stack_json, r.result_json, r.termination_reason, r.created_at, r.updated_at, w.root
FROM runs r
JOIN processes p ON p.id=r.process_id
JOIN sessions s ON s.id=p.session_id
JOIN workspaces w ON w.id=s.workspace_id
WHERE w.root=? ORDER BY r.created_at`, workspaceRoot)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runs := []Run{}
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, *run)
	}
	return runs, rows.Err()
}

const playgroundColumns = `SELECT id, name, script, prompt, taste_json, workspace_root, status,
exit_code, error, created_at, finished_at FROM playground_runs`

func scanPlaygroundRun(row scanner) (*PlaygroundRun, error) {
	var run PlaygroundRun
	var tasteJSON []byte
	var createdAt string
	var finishedAt sql.NullString
	var exitCode sql.NullInt64
	if err := row.Scan(&run.ID, &run.Name, &run.Script, &run.Prompt, &tasteJSON, &run.WorkspaceRoot,
		&run.Status, &exitCode, &run.Error, &createdAt, &finishedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(tasteJSON, &run.Taste); err != nil {
		return nil, fmt.Errorf("decode playground taste: %w", err)
	}
	if exitCode.Valid {
		value := int(exitCode.Int64)
		run.ExitCode = &value
	}
	var err error
	if run.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
		return nil, err
	}
	if finishedAt.Valid {
		value, parseErr := time.Parse(time.RFC3339Nano, finishedAt.String)
		if parseErr != nil {
			return nil, parseErr
		}
		run.FinishedAt = &value
	}
	return &run, nil
}
