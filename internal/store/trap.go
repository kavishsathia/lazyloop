package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	TrapApproval  = "approval"
	TrapSelection = "selection"

	TrapOpen     = "open"
	TrapResolved = "resolved"

	TrapApprove  = "approve"
	TrapReject   = "reject"
	TrapCancel   = "cancel"
	TrapStatusID = "trapped"
)

type Trap struct {
	ID            string     `json:"id"`
	RunID         string     `json:"run_id"`
	StateID       string     `json:"state_id"`
	Iteration     int        `json:"iteration"`
	Name          string     `json:"name"`
	Kind          string     `json:"kind"`
	Prompt        string     `json:"prompt"`
	Options       []string   `json:"options,omitempty"`
	Status        string     `json:"status"`
	Resolution    string     `json:"resolution,omitempty"`
	Note          string     `json:"note,omitempty"`
	WorkspaceRoot string     `json:"workspace_root,omitempty"`
	MachineName   string     `json:"machine_name,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	ResolvedAt    *time.Time `json:"resolved_at,omitempty"`
}

type RaiseTrapParams struct {
	RunID     string
	StateID   string
	Iteration int
	Name      string
	Kind      string
	Prompt    string
	Options   []string
	Clause    string
}

// RaiseTrap records a supervisor request and suspends the run. It is keyed by
// the exact occurrence (run, state, iteration) so a replayed hook or a repeated
// pump re-attaches to the same question instead of asking it twice.
func (s *Store) RaiseTrap(ctx context.Context, params RaiseTrapParams) (*Trap, error) {
	if params.RunID == "" || params.StateID == "" || params.Name == "" {
		return nil, errors.New("trap run ID, state ID, and name are required")
	}
	if params.Kind != TrapApproval && params.Kind != TrapSelection {
		return nil, fmt.Errorf("unsupported trap kind %q", params.Kind)
	}
	if params.Kind == TrapSelection && len(params.Options) < 2 {
		return nil, errors.New("a selection trap requires at least two options")
	}
	optionsJSON, err := json.Marshal(append([]string{}, params.Options...))
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	now := formatTime(time.Now().UTC())
	trapID := newID("trap")
	if _, err := tx.ExecContext(ctx, `INSERT INTO traps(id, run_id, state_id, iteration, name, kind, prompt, options_json, clause, status, created_at)
VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, 'open', ?) ON CONFLICT(run_id, state_id, iteration) DO NOTHING`,
		trapID, params.RunID, params.StateID, params.Iteration, params.Name, params.Kind, params.Prompt, optionsJSON, params.Clause, now); err != nil {
		return nil, fmt.Errorf("raise trap: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT id FROM traps WHERE run_id=? AND state_id=? AND iteration=?`,
		params.RunID, params.StateID, params.Iteration).Scan(&trapID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetTrap(ctx, trapID)
}

// ResolveTrap records the supervisor's verdict and releases the suspended run.
// The verdict is validated against the trap's own declared answer space, so a
// selection can never resolve to an option the program did not offer.
func (s *Store) ResolveTrap(ctx context.Context, trapID, resolution, note string) (*Trap, error) {
	if trapID == "" {
		return nil, errors.New("trap ID is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var runID, kind, status, existingResolution, clause, question string
	var optionsJSON []byte
	err = tx.QueryRowContext(ctx, `SELECT run_id, kind, status, resolution, options_json, clause, prompt FROM traps WHERE id=?`, trapID).
		Scan(&runID, &kind, &status, &existingResolution, &optionsJSON, &clause, &question)
	if err != nil {
		return nil, err
	}
	if status == TrapResolved {
		if existingResolution != resolution {
			return nil, fmt.Errorf("trap %s was already resolved as %q", trapID, existingResolution)
		}
		if err := tx.Rollback(); err != nil {
			return nil, err
		}
		return s.GetTrap(ctx, trapID)
	}
	var options []string
	if err := json.Unmarshal(optionsJSON, &options); err != nil {
		return nil, err
	}
	if err := validateResolution(kind, resolution, options); err != nil {
		return nil, err
	}

	now := formatTime(time.Now().UTC())
	if _, err := tx.ExecContext(ctx, `UPDATE traps SET status='resolved', resolution=?, note=?, resolved_at=? WHERE id=? AND status='open'`,
		resolution, note, now, trapID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET status='running', updated_at=?
WHERE id=? AND status=? AND NOT EXISTS (SELECT 1 FROM traps t WHERE t.run_id=runs.id AND t.status='open')`,
		now, runID, TrapStatusID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	// A trap that stood in for a judgement is the supervisor teaching the
	// profile something it did not cover. Capturing that reasoning as a
	// proposed refinement is how the queue shrinks over time instead of asking
	// the same question every run.
	if clause != "" && strings.TrimSpace(note) != "" {
		var workspaceRoot string
		if err := s.db.QueryRowContext(ctx, `SELECT w.root FROM runs r
JOIN processes p ON p.id=r.process_id
JOIN sessions se ON se.id=p.session_id
JOIN workspaces w ON w.id=se.workspace_id WHERE r.id=?`, runID).Scan(&workspaceRoot); err == nil {
			if _, err := s.RecordTaste(ctx, RecordTasteParams{
				WorkspaceRoot: workspaceRoot, Name: clause, Question: question,
				Clause: note, Source: TasteVerdict,
			}); err != nil {
				return nil, err
			}
		}
	}
	return s.GetTrap(ctx, trapID)
}

func validateResolution(kind, resolution string, options []string) error {
	switch kind {
	case TrapApproval:
		if resolution != TrapApprove && resolution != TrapReject {
			return fmt.Errorf("an approval trap resolves to %q or %q, not %q", TrapApprove, TrapReject, resolution)
		}
	case TrapSelection:
		for _, option := range options {
			if option == resolution {
				return nil
			}
		}
		return fmt.Errorf("%q is not one of the declared options", resolution)
	default:
		return fmt.Errorf("unsupported trap kind %q", kind)
	}
	return nil
}

const trapColumns = `t.id, t.run_id, t.state_id, t.iteration, t.name, t.kind, t.prompt, t.options_json,
t.status, t.resolution, t.note, t.created_at, t.resolved_at, COALESCE(r.machine_name, ''), w.root`

const trapJoin = `FROM traps t
JOIN runs r ON r.id=t.run_id
JOIN processes p ON p.id=r.process_id
JOIN sessions s ON s.id=p.session_id
JOIN workspaces w ON w.id=s.workspace_id`

func (s *Store) GetTrap(ctx context.Context, trapID string) (*Trap, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+trapColumns+` `+trapJoin+` WHERE t.id=?`, trapID)
	return scanTrap(row)
}

// FindTrapOccurrence resolves the trap belonging to one specific execution of a
// trap state, which is what the runtime needs when it re-enters that state.
func (s *Store) FindTrapOccurrence(ctx context.Context, runID, stateID string, iteration int) (*Trap, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+trapColumns+` `+trapJoin+`
WHERE t.run_id=? AND t.state_id=? AND t.iteration=?`, runID, stateID, iteration)
	return scanTrap(row)
}

// FindOpenTrapForRun returns the question a suspended run is waiting on.
func (s *Store) FindOpenTrapForRun(ctx context.Context, runID string) (*Trap, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+trapColumns+` `+trapJoin+`
WHERE t.run_id=? AND t.status='open' ORDER BY t.created_at LIMIT 1`, runID)
	return scanTrap(row)
}

// ListOpenTraps returns the supervisor's review queue, oldest first.
func (s *Store) ListOpenTraps(ctx context.Context, workspaceRoot string) ([]Trap, error) {
	query := `SELECT ` + trapColumns + ` ` + trapJoin + ` WHERE t.status='open'`
	arguments := []any{}
	if workspaceRoot != "" {
		query += ` AND w.root=?`
		arguments = append(arguments, workspaceRoot)
	}
	query += ` ORDER BY t.created_at, t.id`
	rows, err := s.db.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	traps := []Trap{}
	for rows.Next() {
		trap, err := scanTrap(rows)
		if err != nil {
			return nil, err
		}
		traps = append(traps, *trap)
	}
	return traps, rows.Err()
}

// CountTraps reports how much supervisor attention a run has already consumed.
// It counts every raised trap, not only the resolved ones, so an unanswered
// queue cannot silently exceed the declared budget.
func (s *Store) CountTraps(ctx context.Context, runID string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM traps WHERE run_id=?`, runID).Scan(&count)
	return count, err
}

func scanTrap(row scanner) (*Trap, error) {
	var trap Trap
	var optionsJSON []byte
	var createdAt string
	var resolvedAt sql.NullString
	if err := row.Scan(&trap.ID, &trap.RunID, &trap.StateID, &trap.Iteration, &trap.Name, &trap.Kind, &trap.Prompt,
		&optionsJSON, &trap.Status, &trap.Resolution, &trap.Note, &createdAt, &resolvedAt,
		&trap.MachineName, &trap.WorkspaceRoot); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(optionsJSON, &trap.Options); err != nil {
		return nil, fmt.Errorf("decode trap options: %w", err)
	}
	var err error
	if trap.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
		return nil, err
	}
	if resolvedAt.Valid {
		value, parseErr := time.Parse(time.RFC3339Nano, resolvedAt.String)
		if parseErr != nil {
			return nil, parseErr
		}
		trap.ResolvedAt = &value
	}
	return &trap, nil
}
