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
	VerdictPass    = "pass"
	VerdictFail    = "fail"
	VerdictAbstain = "abstain"

	JudgementOpen     = "open"
	JudgementRecorded = "recorded"

	AuditPending = "pending"
	AuditAgreed  = "agreed"
	AuditFlagged = "flagged"
)

// Evidence is one frozen observation the parent captured before forking. The
// observer reads it and cannot re-gather it, so it cannot choose what to look at.
type Evidence struct {
	Command string `json:"command"`
	Output  string `json:"output"`
	Error   string `json:"error,omitempty"`
}

type Judgement struct {
	ID            string     `json:"id"`
	ParentRunID   string     `json:"parent_run_id"`
	StateID       string     `json:"state_id"`
	Iteration     int        `json:"iteration"`
	Name          string     `json:"name"`
	Question      string     `json:"question,omitempty"`
	Clause        string     `json:"clause"`
	Evidence      []Evidence `json:"evidence"`
	ObserverRunID string     `json:"observer_run_id,omitempty"`
	Verdict       string     `json:"verdict,omitempty"`
	Citation      string     `json:"citation,omitempty"`
	Reasoning     string     `json:"reasoning,omitempty"`
	Status        string     `json:"status"`
	Audit         string     `json:"audit,omitempty"`
	AuditNote     string     `json:"audit_note,omitempty"`
	WorkspaceRoot string     `json:"workspace_root,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	RecordedAt    *time.Time `json:"recorded_at,omitempty"`
}

type OpenJudgementParams struct {
	ParentRunID string
	StateID     string
	Iteration   int
	Name        string
	Question    string
	Clause      string
	Evidence    []Evidence
}

// OpenJudgement records what an observer will be asked and what it is allowed
// to see. Like a trap it is keyed by occurrence, so re-entering the state
// re-attaches to the same judgement instead of paying for a second observer.
func (s *Store) OpenJudgement(ctx context.Context, params OpenJudgementParams) (*Judgement, error) {
	if params.ParentRunID == "" || params.StateID == "" || params.Name == "" {
		return nil, errors.New("judgement run ID, state ID, and name are required")
	}
	if strings.TrimSpace(params.Clause) == "" {
		return nil, errors.New("a judgement requires a ratified taste clause")
	}
	evidenceJSON, err := json.Marshal(params.Evidence)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	id := newID("judgement")
	if _, err := tx.ExecContext(ctx, `INSERT INTO judgements(id, parent_run_id, state_id, iteration, name, question, clause, evidence_json, status, created_at)
VALUES(?, ?, ?, ?, ?, ?, ?, ?, 'open', ?) ON CONFLICT(parent_run_id, state_id, iteration) DO NOTHING`,
		id, params.ParentRunID, params.StateID, params.Iteration, params.Name, params.Question,
		params.Clause, evidenceJSON, formatTime(time.Now().UTC())); err != nil {
		return nil, fmt.Errorf("open judgement: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT id FROM judgements WHERE parent_run_id=? AND state_id=? AND iteration=?`,
		params.ParentRunID, params.StateID, params.Iteration).Scan(&id); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetJudgement(ctx, id)
}

// BindObserver attaches the forked child to the judgement it was spawned to
// observe. Exec clears the child's environment, so the store is how the
// observer discovers what it is judging.
func (s *Store) BindObserver(ctx context.Context, parentRunID, stateID string, iteration int, observerRunID string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE judgements SET observer_run_id=?
WHERE parent_run_id=? AND state_id=? AND iteration=? AND status='open'`, observerRunID, parentRunID, stateID, iteration)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return errors.New("no open judgement to bind this observer to")
	}
	return nil
}

// RecordVerdict stores the observer's answer. A verdict is immutable: an
// observer speaks once, so it cannot argue itself from fail to pass. A pass
// without a citation is not a verdict at all and is coerced to an abstention,
// which is the humble answer rather than a confident guess.
func (s *Store) RecordVerdict(ctx context.Context, observerRunID, verdict, citation, reasoning string) (*Judgement, error) {
	if observerRunID == "" {
		return nil, errors.New("observer run ID is required")
	}
	switch verdict {
	case VerdictPass, VerdictFail, VerdictAbstain:
	default:
		return nil, fmt.Errorf("verdict must be %s, %s, or %s", VerdictPass, VerdictFail, VerdictAbstain)
	}
	if verdict != VerdictAbstain && strings.TrimSpace(citation) == "" {
		verdict, reasoning = VerdictAbstain, "no clause was cited, so the observer had no standing to decide"
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var id, status, existing string
	err = tx.QueryRowContext(ctx, `SELECT id, status, verdict FROM judgements WHERE observer_run_id=?`, observerRunID).
		Scan(&id, &status, &existing)
	if err == sql.ErrNoRows {
		return nil, errors.New("this agent is not observing a Hamster judgement")
	}
	if err != nil {
		return nil, err
	}
	if status == JudgementRecorded {
		if existing != verdict {
			return nil, fmt.Errorf("this judgement already returned %q; an observer speaks once", existing)
		}
		if err := tx.Rollback(); err != nil {
			return nil, err
		}
		return s.GetJudgement(ctx, id)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE judgements SET verdict=?, citation=?, reasoning=?, status='recorded', recorded_at=? WHERE id=?`,
		verdict, citation, reasoning, formatTime(time.Now().UTC()), id); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetJudgement(ctx, id)
}

func (s *Store) FindJudgement(ctx context.Context, parentRunID, stateID string, iteration int) (*Judgement, error) {
	row := s.db.QueryRowContext(ctx, judgementColumns+` WHERE j.parent_run_id=? AND j.state_id=? AND j.iteration=?`,
		parentRunID, stateID, iteration)
	return scanJudgement(row)
}

// FindObservedJudgement is what an observer calls to discover its own task.
func (s *Store) FindObservedJudgement(ctx context.Context, observerRunID string) (*Judgement, error) {
	row := s.db.QueryRowContext(ctx, judgementColumns+` WHERE j.observer_run_id=?`, observerRunID)
	return scanJudgement(row)
}

func (s *Store) GetJudgement(ctx context.Context, id string) (*Judgement, error) {
	return scanJudgement(s.db.QueryRowContext(ctx, judgementColumns+` WHERE j.id=?`, id))
}

// CountJudgements reports how many observers a run has already paid for.
func (s *Store) CountJudgements(ctx context.Context, parentRunID string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM judgements WHERE parent_run_id=?`, parentRunID).Scan(&count)
	return count, err
}

const judgementColumns = `SELECT j.id, j.parent_run_id, j.state_id, j.iteration, j.name, j.question, j.clause, j.evidence_json,
COALESCE(j.observer_run_id, ''), j.verdict, j.citation, j.reasoning, j.status, j.audit, j.audit_note, w.root,
j.created_at, j.recorded_at
FROM judgements j
JOIN runs r ON r.id=j.parent_run_id
JOIN processes p ON p.id=r.process_id
JOIN sessions s ON s.id=p.session_id
JOIN workspaces w ON w.id=s.workspace_id`

func scanJudgement(row scanner) (*Judgement, error) {
	var judgement Judgement
	var evidenceJSON []byte
	var createdAt string
	var recordedAt sql.NullString
	if err := row.Scan(&judgement.ID, &judgement.ParentRunID, &judgement.StateID, &judgement.Iteration,
		&judgement.Name, &judgement.Question, &judgement.Clause, &evidenceJSON, &judgement.ObserverRunID,
		&judgement.Verdict, &judgement.Citation, &judgement.Reasoning, &judgement.Status,
		&judgement.Audit, &judgement.AuditNote, &judgement.WorkspaceRoot, &createdAt, &recordedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(evidenceJSON, &judgement.Evidence); err != nil {
		return nil, fmt.Errorf("decode judgement evidence: %w", err)
	}
	var err error
	if judgement.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
		return nil, err
	}
	if recordedAt.Valid {
		value, parseErr := time.Parse(time.RFC3339Nano, recordedAt.String)
		if parseErr != nil {
			return nil, parseErr
		}
		judgement.RecordedAt = &value
	}
	return &judgement, nil
}

// CountConsecutiveFailures reports how many times in a row this judgement has
// failed without an intervening pass. Retrying a taste failure the agent cannot
// perceive burns attempts without converging, so the count is what decides when
// to hand the question to a human instead.
func (s *Store) CountConsecutiveFailures(ctx context.Context, parentRunID, stateID string) (int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT verdict FROM judgements
WHERE parent_run_id=? AND state_id=? AND status='recorded' ORDER BY iteration DESC`, parentRunID, stateID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var verdict string
		if err := rows.Scan(&verdict); err != nil {
			return 0, err
		}
		if verdict != VerdictFail {
			break
		}
		count++
	}
	return count, rows.Err()
}

// SampleForAudit marks one in every rate passing verdicts for human review. A
// judge is only as trustworthy as the clause behind it, and spot-checking the
// passes is the only way a rubric that has gone hollow becomes visible.
func (s *Store) SampleForAudit(ctx context.Context, judgementID string, rate int) (bool, error) {
	if rate <= 0 {
		return false, nil
	}
	var workspaceRoot, name string
	if err := s.db.QueryRowContext(ctx, `SELECT w.root, j.name FROM judgements j
JOIN runs r ON r.id=j.parent_run_id
JOIN processes p ON p.id=r.process_id
JOIN sessions s ON s.id=p.session_id
JOIN workspaces w ON w.id=s.workspace_id WHERE j.id=?`, judgementID).Scan(&workspaceRoot, &name); err != nil {
		return false, err
	}
	var passes int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM judgements j
JOIN runs r ON r.id=j.parent_run_id
JOIN processes p ON p.id=r.process_id
JOIN sessions s ON s.id=p.session_id
JOIN workspaces w ON w.id=s.workspace_id
WHERE w.root=? AND j.name=? AND j.verdict=?`, workspaceRoot, name, VerdictPass).Scan(&passes); err != nil {
		return false, err
	}
	if passes%rate != 0 {
		return false, nil
	}
	_, err := s.db.ExecContext(ctx, `UPDATE judgements SET audit=? WHERE id=? AND audit=''`, AuditPending, judgementID)
	return err == nil, err
}

// ListAudits returns sampled passes awaiting a spot-check. Unlike a trap this
// never blocks a run: the work already advanced, and the audit questions the
// judge rather than the work.
func (s *Store) ListAudits(ctx context.Context, workspaceRoot string) ([]Judgement, error) {
	query := judgementColumns + ` WHERE j.audit=?`
	arguments := []any{AuditPending}
	if workspaceRoot != "" {
		query += ` AND w.root=?`
		arguments = append(arguments, workspaceRoot)
	}
	query += ` ORDER BY j.created_at`
	rows, err := s.db.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	audits := []Judgement{}
	for rows.Next() {
		judgement, err := scanJudgement(rows)
		if err != nil {
			return nil, err
		}
		audits = append(audits, *judgement)
	}
	return audits, rows.Err()
}

// ResolveAudit records whether the supervisor agrees with a sampled verdict.
// Disagreement is the signal that the clause no longer says what it should, so
// it becomes a proposed refinement rather than a note nobody reads.
func (s *Store) ResolveAudit(ctx context.Context, judgementID, outcome, note string) (*Judgement, error) {
	if outcome != AuditAgreed && outcome != AuditFlagged {
		return nil, fmt.Errorf("an audit resolves to %s or %s", AuditAgreed, AuditFlagged)
	}
	if outcome == AuditFlagged && strings.TrimSpace(note) == "" {
		return nil, errors.New("flagging an audit requires a note; it becomes the proposed refinement")
	}
	judgement, err := s.GetJudgement(ctx, judgementID)
	if err != nil {
		return nil, err
	}
	if judgement.Audit != AuditPending {
		return nil, fmt.Errorf("judgement %s is not awaiting audit", judgementID)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE judgements SET audit=?, audit_note=? WHERE id=?`, outcome, note, judgementID); err != nil {
		return nil, err
	}
	if outcome == AuditFlagged {
		if _, err := s.RecordTaste(ctx, RecordTasteParams{
			WorkspaceRoot: judgement.WorkspaceRoot, Name: judgement.Name, Question: judgement.Question,
			Clause: note, Source: TasteVerdict,
		}); err != nil {
			return nil, err
		}
	}
	return s.GetJudgement(ctx, judgementID)
}
