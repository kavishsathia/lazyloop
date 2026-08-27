package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"hamster/internal/ir"
)

// A console is a lazyloop screen. It knows the directory it opened in and the
// agent it spawned, but not the Codex session that agent will create; that
// identity only exists inside hooks. The console token, carried in the
// agent's environment, is how the two meet: every hook the agent fires binds
// its session to the token, and the console reads the session back through it.

type ConsoleBinding struct {
	Console        string    `json:"console"`
	CodexSessionID string    `json:"codex_session_id"`
	WorkspaceRoot  string    `json:"workspace_root"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// A Launch is a loop armed from a console before any agent has claimed it.
// The next hook from an unbound root agent carrying the console token turns
// it into a run — the same shape as a pending fork, for the same reason: the
// person who armed it cannot know the identity that will pick it up.
type Launch struct {
	Console       string     `json:"console"`
	WorkspaceRoot string     `json:"workspace_root"`
	Machine       ir.Machine `json:"machine"`
	Source        string     `json:"source,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	ClaimedRunID  string     `json:"claimed_run_id,omitempty"`
	ClaimedAt     *time.Time `json:"claimed_at,omitempty"`
}

func (s *Store) BindConsole(ctx context.Context, console, codexSessionID, workspaceRoot string) error {
	if console == "" || codexSessionID == "" {
		return errors.New("console token and Codex session ID are required")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO consoles(console, codex_session_id, workspace_root, updated_at) VALUES(?, ?, ?, ?)
ON CONFLICT(console) DO UPDATE SET codex_session_id=excluded.codex_session_id, workspace_root=excluded.workspace_root, updated_at=excluded.updated_at`,
		console, codexSessionID, workspaceRoot, formatTime(time.Now().UTC()))
	return err
}

func (s *Store) FindConsole(ctx context.Context, console string) (*ConsoleBinding, error) {
	var binding ConsoleBinding
	var updated string
	err := s.db.QueryRowContext(ctx, `SELECT console, codex_session_id, workspace_root, updated_at FROM consoles WHERE console=?`, console).
		Scan(&binding.Console, &binding.CodexSessionID, &binding.WorkspaceRoot, &updated)
	if err != nil {
		return nil, err
	}
	if binding.UpdatedAt, err = parseStoredTime(updated); err != nil {
		return nil, err
	}
	return &binding, nil
}

// ArmLaunch replaces whatever the console had armed before; a console holds
// one next loop, not a queue.
func (s *Store) ArmLaunch(ctx context.Context, console, workspaceRoot string, machine ir.Machine, source string) (*Launch, error) {
	if console == "" || workspaceRoot == "" {
		return nil, errors.New("console token and workspace root are required")
	}
	machineJSON, err := json.Marshal(machine)
	if err != nil {
		return nil, fmt.Errorf("encode machine: %w", err)
	}
	now := formatTime(time.Now().UTC())
	if _, err := s.db.ExecContext(ctx, `INSERT INTO launches(console, workspace_root, machine_json, source, created_at) VALUES(?, ?, ?, ?, ?)
ON CONFLICT(console) DO UPDATE SET workspace_root=excluded.workspace_root, machine_json=excluded.machine_json, source=excluded.source,
created_at=excluded.created_at, claimed_run_id=NULL, claimed_at=NULL`, console, workspaceRoot, machineJSON, source, now); err != nil {
		return nil, err
	}
	return s.PendingLaunch(ctx, console)
}

// PendingLaunch is the armed loop nobody has claimed yet, or sql.ErrNoRows.
func (s *Store) PendingLaunch(ctx context.Context, console string) (*Launch, error) {
	row := s.db.QueryRowContext(ctx, `SELECT console, workspace_root, machine_json, source, created_at, COALESCE(claimed_run_id, ''), claimed_at
FROM launches WHERE console=? AND claimed_run_id IS NULL`, console)
	return scanLaunch(row)
}

func (s *Store) DisarmLaunch(ctx context.Context, console string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM launches WHERE console=? AND claimed_run_id IS NULL`, console)
	return err
}

// ClaimLaunch turns the console's armed loop into a run bound to the root
// agent of the given Codex session. The claim is taken before the run is
// created so two hooks racing for it cannot both win; the loser sees
// sql.ErrNoRows, exactly as if nothing had been armed.
func (s *Store) ClaimLaunch(ctx context.Context, console, codexSessionID string) (*Run, error) {
	launch, err := s.PendingLaunch(ctx, console)
	if err != nil {
		return nil, err
	}
	taken, err := s.db.ExecContext(ctx, `UPDATE launches SET claimed_run_id='' WHERE console=? AND claimed_run_id IS NULL`, console)
	if err != nil {
		return nil, err
	}
	if affected, _ := taken.RowsAffected(); affected == 0 {
		return nil, sql.ErrNoRows
	}
	run, err := s.CreateRun(ctx, CreateRunParams{WorkspaceRoot: launch.WorkspaceRoot, CodexSessionID: codexSessionID, Machine: launch.Machine})
	if err != nil {
		_, _ = s.db.ExecContext(ctx, `UPDATE launches SET claimed_run_id=NULL WHERE console=? AND claimed_run_id=''`, console)
		return nil, err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE launches SET claimed_run_id=?, claimed_at=? WHERE console=?`, run.ID, formatTime(time.Now().UTC()), console)
	if err != nil {
		return nil, err
	}
	return run, nil
}

func scanLaunch(row scanner) (*Launch, error) {
	var launch Launch
	var machineJSON []byte
	var created string
	var claimed sql.NullString
	if err := row.Scan(&launch.Console, &launch.WorkspaceRoot, &machineJSON, &launch.Source, &created, &launch.ClaimedRunID, &claimed); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(machineJSON, &launch.Machine); err != nil {
		return nil, fmt.Errorf("decode launch machine: %w", err)
	}
	var err error
	if launch.CreatedAt, err = parseStoredTime(created); err != nil {
		return nil, err
	}
	if claimed.Valid {
		at, err := parseStoredTime(claimed.String)
		if err != nil {
			return nil, err
		}
		launch.ClaimedAt = &at
	}
	return &launch, nil
}

// ConsoleObservation is everything a console pane shows about the agent it
// is watching: the run bound to that agent's session, what the run is waiting
// on, and the loop armed for it next.
type ConsoleObservation struct {
	Console        string                     `json:"console"`
	Bound          bool                       `json:"bound"`
	CodexSessionID string                     `json:"codex_session_id,omitempty"`
	WorkspaceRoot  string                     `json:"workspace_root,omitempty"`
	Run            *RunObservation            `json:"run,omitempty"`
	Limits         []LimitObservation         `json:"limits,omitempty"`
	Verifications  []VerificationObservation  `json:"verifications,omitempty"`
	Summary        RunObservationEventSummary `json:"summary"`
	TrapCount      int                        `json:"trap_count"`
	Traps          []Trap                     `json:"traps,omitempty"`
	Launch         *Launch                    `json:"launch,omitempty"`
}

func (s *Store) ObserveConsole(ctx context.Context, console string) (*ConsoleObservation, error) {
	view := &ConsoleObservation{Console: console}
	if launch, err := s.PendingLaunch(ctx, console); err == nil {
		view.Launch = launch
	} else if err != sql.ErrNoRows {
		return nil, err
	}
	binding, err := s.FindConsole(ctx, console)
	if err == sql.ErrNoRows {
		return view, nil
	}
	if err != nil {
		return nil, err
	}
	view.Bound, view.CodexSessionID, view.WorkspaceRoot = true, binding.CodexSessionID, binding.WorkspaceRoot
	run, err := s.FindCurrentRun(ctx, binding.CodexSessionID, RootAgent)
	if err == sql.ErrNoRows {
		return view, nil
	}
	if err != nil {
		return nil, err
	}
	detail, err := s.ObserveRun(ctx, run.ID)
	if err != nil {
		return nil, err
	}
	view.Run, view.Limits, view.Summary = &detail.Run, detail.Limits, detail.Summary
	// The most recent probe is the one the person wants: what was checked
	// last, and whether it passed.
	for index := len(detail.Events) - 1; index >= 0; index-- {
		if len(detail.Events[index].Verifications) > 0 {
			view.Verifications = detail.Events[index].Verifications
			break
		}
	}
	if view.TrapCount, err = s.CountTraps(ctx, run.ID); err != nil {
		return nil, err
	}
	traps, err := s.ListOpenTraps(ctx, "")
	if err != nil {
		return nil, err
	}
	for _, trap := range traps {
		if trap.RunID == run.ID {
			view.Traps = append(view.Traps, trap)
		}
	}
	return view, nil
}
