package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
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

// A Loop is one loop a person armed from lazyloop. It persists: the list of
// loops survives the run that executed it and the lazyloop session that armed
// it. Before an agent claims it a loop is "armed"; once claimed it carries the
// run that executes it, and its status follows that run.
type Loop struct {
	ID            string     `json:"id"`
	Console       string     `json:"console"`
	WorkspaceRoot string     `json:"workspace_root"`
	Source        string     `json:"source,omitempty"`
	Label         string     `json:"label"`
	Machine       ir.Machine `json:"machine"`
	CreatedAt     time.Time  `json:"created_at"`
	RunID         string     `json:"run_id,omitempty"`
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

const loopColumns = `id, console, workspace_root, source, label, machine_json, created_at, COALESCE(run_id, ''), claimed_at`

// ArmLoop records a newly armed loop. A console holds one armed loop at a
// time, so any earlier loop the same console armed but nobody claimed is
// dropped; claimed loops stay, because they are the history.
func (s *Store) ArmLoop(ctx context.Context, console, workspaceRoot string, machine ir.Machine, source, label string) (*Loop, error) {
	if console == "" || workspaceRoot == "" {
		return nil, errors.New("console token and workspace root are required")
	}
	machineJSON, err := json.Marshal(machine)
	if err != nil {
		return nil, fmt.Errorf("encode machine: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM loops WHERE console=? AND run_id IS NULL`, console); err != nil {
		return nil, err
	}
	id := newID("loop")
	now := formatTime(time.Now().UTC())
	if _, err := tx.ExecContext(ctx, `INSERT INTO loops(id, console, workspace_root, source, label, machine_json, created_at) VALUES(?, ?, ?, ?, ?, ?, ?)`,
		id, console, workspaceRoot, source, label, machineJSON, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetLoop(ctx, id)
}

// PendingLoop is the console's armed, unclaimed loop, or sql.ErrNoRows.
func (s *Store) PendingLoop(ctx context.Context, console string) (*Loop, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+loopColumns+` FROM loops WHERE console=? AND run_id IS NULL ORDER BY created_at DESC, id DESC LIMIT 1`, console)
	return scanLoop(row)
}

func (s *Store) DisarmLoop(ctx context.Context, console string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM loops WHERE console=? AND run_id IS NULL`, console)
	return err
}

// StopLoop ends one loop by id. An armed loop nobody claimed is removed; a
// running loop's run is marked killed and its process unbound, so the session
// that ran it is cleanly released rather than left pointing at a dead run.
// Each loop has its own session, so stopping one never disturbs another.
func (s *Store) StopLoop(ctx context.Context, loopID string) error {
	loop, err := s.GetLoop(ctx, loopID)
	if err != nil {
		return err
	}
	if loop.RunID == "" {
		_, err := s.db.ExecContext(ctx, `DELETE FROM loops WHERE id=? AND run_id IS NULL`, loopID)
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := formatTime(time.Now().UTC())
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET status='killed', termination_reason='stopped from lazyloop', updated_at=?
WHERE id=? AND status IN ('running','waiting','trapped')`, now, loop.RunID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE processes SET current_run_id=NULL, updated_at=? WHERE current_run_id=?`, now, loop.RunID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) GetLoop(ctx context.Context, id string) (*Loop, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+loopColumns+` FROM loops WHERE id=?`, id)
	return scanLoop(row)
}

// ClaimLoop turns the console's armed loop into a run bound to the root agent
// of the given Codex session, and links the run back to the loop so the list
// can follow it. The claim is taken before the run is created so two hooks
// racing for it cannot both win; the loser sees sql.ErrNoRows.
func (s *Store) ClaimLoop(ctx context.Context, console, codexSessionID string) (*Run, error) {
	loop, err := s.PendingLoop(ctx, console)
	if err != nil {
		return nil, err
	}
	taken, err := s.db.ExecContext(ctx, `UPDATE loops SET run_id='' WHERE id=? AND run_id IS NULL`, loop.ID)
	if err != nil {
		return nil, err
	}
	if affected, _ := taken.RowsAffected(); affected == 0 {
		return nil, sql.ErrNoRows
	}
	run, err := s.CreateRun(ctx, CreateRunParams{WorkspaceRoot: loop.WorkspaceRoot, CodexSessionID: codexSessionID, Machine: loop.Machine})
	if err != nil {
		_, _ = s.db.ExecContext(ctx, `UPDATE loops SET run_id=NULL WHERE id=? AND run_id=''`, loop.ID)
		return nil, err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE loops SET run_id=?, claimed_at=? WHERE id=?`, run.ID, formatTime(time.Now().UTC()), loop.ID)
	if err != nil {
		return nil, err
	}
	return run, nil
}

// LoopSummary is one row in the list of loops. Status is "armed" for a loop
// nobody has claimed, otherwise the status of the run executing it.
type LoopSummary struct {
	ID        string      `json:"id"`
	Label     string      `json:"label"`
	Status    string      `json:"status"`
	RunID     string      `json:"run_id,omitempty"`
	Traps     int         `json:"traps"`
	ToolCalls int         `json:"tool_calls"`
	Started   *time.Time  `json:"started,omitempty"`
	Ended     *time.Time  `json:"ended,omitempty"`
	CreatedAt time.Time   `json:"created_at"`
	Agents    []LoopAgent `json:"agents,omitempty"`
}

// A LoopAgent is one Codex agent working a loop: the root agent and, once a
// loop forks, its subagents. Each is a process in the loop's session.
type LoopAgent struct {
	AgentID string `json:"agent_id"`
	Root    bool   `json:"root"`
	Status  string `json:"status"`
}

// ListLoops returns every loop armed in a workspace, newest first, each with
// the live status of its run. It is the persistent history the list shows.
func (s *Store) ListLoops(ctx context.Context, workspaceRoot string, limit int) ([]LoopSummary, error) {
	if limit <= 0 {
		limit = 200
	}
	query := `SELECT l.id, l.label, COALESCE(r.status, 'armed'),
COALESCE(l.run_id, ''), l.created_at,
COALESCE(r.created_at, ''), COALESCE(r.updated_at, ''), COALESCE(r.status, ''),
(SELECT COUNT(*) FROM traps t WHERE t.run_id=l.run_id AND t.status='open'),
(SELECT COUNT(*) FROM events e WHERE e.run_id=l.run_id AND e.kind='PostToolUse')
FROM loops l LEFT JOIN runs r ON r.id=l.run_id`
	args := []any{}
	if workspaceRoot != "" {
		query += ` WHERE l.workspace_root=?`
		args = append(args, workspaceRoot)
	}
	query += ` ORDER BY l.created_at DESC, l.id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	summaries := []LoopSummary{}
	for rows.Next() {
		var summary LoopSummary
		var created, runCreated, runUpdated, runStatus string
		if err := rows.Scan(&summary.ID, &summary.Label, &summary.Status, &summary.RunID, &created,
			&runCreated, &runUpdated, &runStatus, &summary.Traps, &summary.ToolCalls); err != nil {
			rows.Close()
			return nil, err
		}
		if summary.CreatedAt, err = parseStoredTime(created); err != nil {
			rows.Close()
			return nil, err
		}
		if runCreated != "" {
			if started, err := parseStoredTime(runCreated); err == nil {
				summary.Started = &started
			}
		}
		// A finished run's clock stops; a live one is timed against now by the
		// reader, so its end is left open.
		if runUpdated != "" && isTerminalRunStatus(runStatus) {
			if ended, err := parseStoredTime(runUpdated); err == nil {
				summary.Ended = &ended
			}
		}
		summaries = append(summaries, summary)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	// The single pooled connection cannot serve a nested query, so gather the
	// agents only after the list query's rows are closed.
	rows.Close()
	for index := range summaries {
		if summaries[index].RunID == "" {
			continue
		}
		agents, err := s.loopAgents(ctx, summaries[index].RunID)
		if err != nil {
			return nil, err
		}
		summaries[index].Agents = agents
	}
	return summaries, nil
}

// loopAgents lists the agents in a loop's session — the root agent and any
// subagents it forked — with the status of the run each is on.
func (s *Store) loopAgents(ctx context.Context, runID string) ([]LoopAgent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT p.agent_id, COALESCE(r.status, '')
FROM processes p
LEFT JOIN runs r ON r.id = p.current_run_id
WHERE p.session_id = (SELECT p2.session_id FROM runs r2 JOIN processes p2 ON p2.id = r2.process_id WHERE r2.id = ?)
ORDER BY (p.agent_id != ?), p.created_at, p.id`, runID, RootAgent)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	agents := []LoopAgent{}
	for rows.Next() {
		var agent LoopAgent
		if err := rows.Scan(&agent.AgentID, &agent.Status); err != nil {
			return nil, err
		}
		agent.Root = agent.AgentID == RootAgent
		if agent.Status == "" {
			agent.Status = "idle"
		}
		agents = append(agents, agent)
	}
	return agents, rows.Err()
}

// isTerminalRunStatus reports whether a run has stopped, so its duration is
// fixed rather than still counting up.
func isTerminalRunStatus(status string) bool {
	switch status {
	case "", "running", "waiting", "trapped":
		return false
	}
	return true
}

// LogLine is one entry in a loop's log: what the agent did, and what Hamster
// decided about it. Level colours it — good for a passed check, bad for a
// failure, warn for a trap.
type LogLine struct {
	Text  string `json:"text"`
	Level string `json:"level"`
}

// LoopDetail is everything the two right panels show for a selected loop: its
// source on top, its log below.
type LoopDetail struct {
	Loop
	Status        string                    `json:"status"`
	Run           *RunObservation           `json:"run,omitempty"`
	Limits        []LimitObservation        `json:"limits,omitempty"`
	Verifications []VerificationObservation `json:"verifications,omitempty"`
	Logs          []LogLine                 `json:"logs"`
	Traps         []Trap                    `json:"traps,omitempty"`
}

// LoadLoop gathers a loop and, if it has a run, that run's activity as a log.
func (s *Store) LoadLoop(ctx context.Context, id string) (*LoopDetail, error) {
	loop, err := s.GetLoop(ctx, id)
	if err != nil {
		return nil, err
	}
	detail := &LoopDetail{Loop: *loop, Status: "armed"}
	if loop.RunID == "" {
		return detail, nil
	}
	run, err := s.ObserveRun(ctx, loop.RunID)
	if err == sql.ErrNoRows {
		detail.Status = "gone"
		return detail, nil
	}
	if err != nil {
		return nil, err
	}
	detail.Status = run.Run.Status
	detail.Run, detail.Limits = &run.Run, run.Limits
	for index := len(run.Events) - 1; index >= 0; index-- {
		if len(run.Events[index].Verifications) > 0 {
			detail.Verifications = run.Events[index].Verifications
			break
		}
	}
	detail.Logs = buildLogs(run.Events)
	traps, err := s.ListOpenTraps(ctx, "")
	if err != nil {
		return nil, err
	}
	for _, trap := range traps {
		if trap.RunID == loop.RunID {
			detail.Traps = append(detail.Traps, trap)
			detail.Logs = append(detail.Logs, LogLine{Text: "trap: " + trap.Prompt, Level: "warn"})
		}
	}
	return detail, nil
}

// buildLogs turns a run's events into readable log lines, oldest first. A tool
// call reaches the log once — on its PostToolUse event, or on a PreToolUse that
// was denied (a denied call never gets a post event). Its allowed PreToolUse is
// skipped so each call is not listed twice.
func buildLogs(events []EventObservation) []LogLine {
	logs := make([]LogLine, 0, len(events))
	for _, event := range events {
		denied := event.Decision == "deny"
		if (event.EventName == "PostToolUse" || denied) && (event.Command != "" || event.ToolName != "") {
			if denied {
				logs = append(logs, LogLine{Text: "denied: " + commandLabel(event), Level: "bad"})
			} else {
				logs = append(logs, LogLine{Text: "· " + commandLabel(event), Level: "info"})
			}
		}
		for _, check := range event.Verifications {
			mark, level := "✓", "good"
			if check.Error != "" || !check.Passed {
				mark, level = "✗", "bad"
			}
			text := mark + " " + check.Name
			if check.Command != "" {
				text += "  " + check.Command
			}
			logs = append(logs, LogLine{Text: text, Level: level})
			if !check.Passed {
				if detail := verifierDetail(check); detail != "" {
					logs = append(logs, LogLine{Text: "    " + detail, Level: "dim"})
				}
			}
		}
	}
	return logs
}

// commandLabel names a tool call in one short line. A patch is named by the
// files it touches rather than dumping its whole body; any other command shows
// its first line.
func commandLabel(event EventObservation) string {
	command := strings.TrimSpace(event.Command)
	if strings.HasPrefix(command, "*** Begin Patch") || strings.EqualFold(event.ToolName, "apply_patch") {
		if files := patchFiles(command); len(files) > 0 {
			return "apply_patch " + strings.Join(files, ", ")
		}
		return "apply_patch"
	}
	if command != "" {
		return firstTextLine(command)
	}
	return event.ToolName
}

// patchFiles pulls the paths an apply_patch touches from its header lines.
func patchFiles(patch string) []string {
	var files []string
	for _, raw := range strings.Split(patch, "\n") {
		line := strings.TrimSpace(raw)
		for _, marker := range []string{"*** Update File: ", "*** Add File: ", "*** Delete File: ", "*** Move to: "} {
			if strings.HasPrefix(line, marker) {
				files = append(files, filepath.Base(strings.TrimSpace(strings.TrimPrefix(line, marker))))
			}
		}
	}
	return files
}

func firstTextLine(text string) string {
	if index := strings.IndexByte(text, '\n'); index >= 0 {
		return strings.TrimSpace(text[:index])
	}
	return strings.TrimSpace(text)
}

func verifierDetail(check VerificationObservation) string {
	if output := strings.TrimSpace(check.Output); output != "" {
		lines := strings.Split(output, "\n")
		return strings.TrimSpace(lines[len(lines)-1])
	}
	return strings.TrimSpace(check.Error)
}

func scanLoop(row scanner) (*Loop, error) {
	var loop Loop
	var machineJSON []byte
	var created string
	var claimed sql.NullString
	if err := row.Scan(&loop.ID, &loop.Console, &loop.WorkspaceRoot, &loop.Source, &loop.Label, &machineJSON, &created, &loop.RunID, &claimed); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(machineJSON, &loop.Machine); err != nil {
		return nil, fmt.Errorf("decode loop machine: %w", err)
	}
	var err error
	if loop.CreatedAt, err = parseStoredTime(created); err != nil {
		return nil, err
	}
	if claimed.Valid {
		at, err := parseStoredTime(claimed.String)
		if err != nil {
			return nil, err
		}
		loop.ClaimedAt = &at
	}
	return &loop, nil
}

// ConsoleObservation is what the dashboard reads about the agent bound to a
// console: the run its session is on now, what that run waits on, and the
// loop armed for it next. The list of loops comes from ListLoops.
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
	Armed          *Loop                      `json:"armed,omitempty"`
}

func (s *Store) ObserveConsole(ctx context.Context, console string) (*ConsoleObservation, error) {
	view := &ConsoleObservation{Console: console}
	if loop, err := s.PendingLoop(ctx, console); err == nil {
		view.Armed = loop
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
