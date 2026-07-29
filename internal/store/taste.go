package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	TasteDraft    = "draft"
	TasteRatified = "ratified"

	TasteInterview = "interview"
	TasteInduced   = "induced"
	TasteVerdict   = "verdict"
)

// TasteClause is one ratified statement of the supervisor's standards. It is
// what gives an automated judge standing: a judge applies these clauses and has
// no aesthetic authority of its own.
type TasteClause struct {
	ID            string     `json:"id"`
	WorkspaceRoot string     `json:"workspace_root"`
	Name          string     `json:"name"`
	Question      string     `json:"question,omitempty"`
	Clause        string     `json:"clause"`
	Source        string     `json:"source"`
	Status        string     `json:"status"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	RatifiedAt    *time.Time `json:"ratified_at,omitempty"`
}

type RecordTasteParams struct {
	WorkspaceRoot string
	Name          string
	Question      string
	Clause        string
	Source        string
}

// RecordTaste stores a proposed clause. The agent conducts the interview and
// reports what it heard, so a draft is only ever a proposal — it carries no
// authority until a human ratifies it, and a ratified clause can never be
// silently rewritten by the agent it governs.
func (s *Store) RecordTaste(ctx context.Context, params RecordTasteParams) (*TasteClause, error) {
	if params.WorkspaceRoot == "" || params.Name == "" {
		return nil, errors.New("workspace root and clause name are required")
	}
	if strings.TrimSpace(params.Clause) == "" {
		return nil, errors.New("a taste clause requires text")
	}
	switch params.Source {
	case "":
		params.Source = TasteInterview
	case TasteInterview, TasteInduced, TasteVerdict:
	default:
		return nil, fmt.Errorf("unsupported taste source %q", params.Source)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	workspaceID, err := ensureWorkspace(ctx, tx, params.WorkspaceRoot)
	if err != nil {
		return nil, err
	}

	// A proposal never overwrites the standing clause. It sits beside it as a
	// draft until a supervisor adopts it, so refining taste is always visible
	// and never silent.
	var existingID string
	err = tx.QueryRowContext(ctx, `SELECT id FROM taste_clauses WHERE workspace_id=? AND name=? AND status='draft'`,
		workspaceID, params.Name).Scan(&existingID)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	now := formatTime(time.Now().UTC())
	if err == nil {
		if _, err := tx.ExecContext(ctx, `UPDATE taste_clauses SET question=?, clause=?, source=?, updated_at=? WHERE id=?`,
			params.Question, params.Clause, params.Source, now, existingID); err != nil {
			return nil, err
		}
	} else {
		existingID = newID("taste")
		if _, err := tx.ExecContext(ctx, `INSERT INTO taste_clauses(id, workspace_id, name, question, clause, source, status, created_at, updated_at)
VALUES(?, ?, ?, ?, ?, ?, 'draft', ?, ?)`, existingID, workspaceID, params.Name, params.Question, params.Clause, params.Source, now, now); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetTasteClause(ctx, existingID)
}

// RatifyTaste promotes drafts to the standing profile. It is a supervisor
// operation: the hook denies it to the agent, so an agent can propose its own
// grading standard but can never adopt it.
func (s *Store) RatifyTaste(ctx context.Context, workspaceRoot, name string) ([]TasteClause, error) {
	if workspaceRoot == "" {
		return nil, errors.New("workspace root is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	workspaceID, err := ensureWorkspace(ctx, tx, workspaceRoot)
	if err != nil {
		return nil, err
	}
	now := formatTime(time.Now().UTC())
	// Adopting a refinement supersedes the clause it refines. Replacement is an
	// explicit human act, which is the whole point of the draft.
	supersede := `DELETE FROM taste_clauses WHERE workspace_id=? AND status='ratified' AND name IN (
SELECT name FROM taste_clauses WHERE workspace_id=? AND status='draft'`
	supersedeArguments := []any{workspaceID, workspaceID}
	if name != "" {
		supersede += ` AND name=?`
		supersedeArguments = append(supersedeArguments, name)
	}
	supersede += `)`
	if _, err := tx.ExecContext(ctx, supersede, supersedeArguments...); err != nil {
		return nil, err
	}
	query := `UPDATE taste_clauses SET status='ratified', ratified_at=?, updated_at=? WHERE workspace_id=? AND status='draft'`
	arguments := []any{now, now, workspaceID}
	if name != "" {
		query += ` AND name=?`
		arguments = append(arguments, name)
	}
	result, err := tx.ExecContext(ctx, query, arguments...)
	if err != nil {
		return nil, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if changed == 0 {
		return nil, errors.New("no draft taste clauses to ratify")
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.ListTaste(ctx, workspaceRoot, true)
}

// RevokeTaste withdraws a clause. A standing profile that cannot be corrected
// would harden one bad judgement into a permanent rule.
func (s *Store) RevokeTaste(ctx context.Context, workspaceRoot, name string) error {
	if workspaceRoot == "" || name == "" {
		return errors.New("workspace root and clause name are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	workspaceID, err := ensureWorkspace(ctx, tx, workspaceRoot)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM taste_clauses WHERE workspace_id=? AND name=?`, workspaceID, name)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return fmt.Errorf("no taste clause named %q", name)
	}
	return tx.Commit()
}

// ListTaste returns the profile. ratifiedOnly is what an automated judge reads,
// so an unratified proposal can never be applied as if it were the
// supervisor's standard.
func (s *Store) ListTaste(ctx context.Context, workspaceRoot string, ratifiedOnly bool) ([]TasteClause, error) {
	query := `SELECT t.id, w.root, t.name, t.question, t.clause, t.source, t.status, t.created_at, t.updated_at, t.ratified_at
FROM taste_clauses t JOIN workspaces w ON w.id=t.workspace_id WHERE 1=1`
	arguments := []any{}
	if workspaceRoot != "" {
		query += ` AND w.root=?`
		arguments = append(arguments, workspaceRoot)
	}
	if ratifiedOnly {
		query += ` AND t.status='ratified'`
	}
	query += ` ORDER BY t.name, t.status`
	rows, err := s.db.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	clauses := []TasteClause{}
	for rows.Next() {
		clause, err := scanTasteClause(rows)
		if err != nil {
			return nil, err
		}
		clauses = append(clauses, *clause)
	}
	return clauses, rows.Err()
}

func (s *Store) GetTasteClause(ctx context.Context, id string) (*TasteClause, error) {
	row := s.db.QueryRowContext(ctx, `SELECT t.id, w.root, t.name, t.question, t.clause, t.source, t.status, t.created_at, t.updated_at, t.ratified_at
FROM taste_clauses t JOIN workspaces w ON w.id=t.workspace_id WHERE t.id=?`, id)
	return scanTasteClause(row)
}

func scanTasteClause(row scanner) (*TasteClause, error) {
	var clause TasteClause
	var createdAt, updatedAt string
	var ratifiedAt sql.NullString
	if err := row.Scan(&clause.ID, &clause.WorkspaceRoot, &clause.Name, &clause.Question, &clause.Clause,
		&clause.Source, &clause.Status, &createdAt, &updatedAt, &ratifiedAt); err != nil {
		return nil, err
	}
	var err error
	if clause.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
		return nil, err
	}
	if clause.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt); err != nil {
		return nil, err
	}
	if ratifiedAt.Valid {
		value, parseErr := time.Parse(time.RFC3339Nano, ratifiedAt.String)
		if parseErr != nil {
			return nil, parseErr
		}
		clause.RatifiedAt = &value
	}
	return &clause, nil
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func ensureWorkspace(ctx context.Context, tx execer, root string) (string, error) {
	if _, err := tx.ExecContext(ctx, `INSERT INTO workspaces(id, root, created_at) VALUES(?, ?, ?)
ON CONFLICT(root) DO NOTHING`, newID("workspace"), root, formatTime(time.Now().UTC())); err != nil {
		return "", fmt.Errorf("upsert workspace: %w", err)
	}
	var workspaceID string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM workspaces WHERE root=?`, root).Scan(&workspaceID); err != nil {
		return "", fmt.Errorf("resolve workspace: %w", err)
	}
	return workspaceID, nil
}
