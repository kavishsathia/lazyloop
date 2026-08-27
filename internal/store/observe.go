package store

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"hamster/internal/ir"
)

type SessionObservation struct {
	ID             string               `json:"id"`
	CodexSessionID string               `json:"codex_session_id"`
	WorkspaceRoot  string               `json:"workspace_root"`
	CreatedAt      time.Time            `json:"created_at"`
	UpdatedAt      time.Time            `json:"updated_at"`
	Processes      []ProcessObservation `json:"processes"`
}

type ProcessObservation struct {
	ID              string          `json:"id"`
	AgentID         string          `json:"agent_id"`
	ParentProcessID string          `json:"parent_process_id,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
	Run             *RunObservation `json:"run,omitempty"`
}

type RunObservation struct {
	ID                string    `json:"id"`
	ParentRunID       string    `json:"parent_run_id,omitempty"`
	MachineID         string    `json:"machine_id"`
	MachineName       string    `json:"machine_name,omitempty"`
	MachineKind       string    `json:"machine_kind"`
	StateID           string    `json:"state_id"`
	StateName         string    `json:"state_name,omitempty"`
	StateKind         string    `json:"state_kind,omitempty"`
	Executor          string    `json:"executor,omitempty"`
	Status            string    `json:"status"`
	Iteration         int       `json:"iteration"`
	StackDepth        int       `json:"stack_depth"`
	Result            any       `json:"result,omitempty"`
	TerminationReason string    `json:"termination_reason,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type RunObservationDetail struct {
	SessionID       string                     `json:"session_id"`
	CodexSessionID  string                     `json:"codex_session_id"`
	WorkspaceRoot   string                     `json:"workspace_root"`
	ProcessID       string                     `json:"process_id"`
	ParentProcessID string                     `json:"parent_process_id,omitempty"`
	AgentID         string                     `json:"agent_id"`
	Run             RunObservation             `json:"run"`
	States          []StateObservation         `json:"states"`
	Stack           []StackObservation         `json:"stack"`
	Limits          []LimitObservation         `json:"limits"`
	Events          []EventObservation         `json:"events"`
	Summary         RunObservationEventSummary `json:"summary"`
}

type StateObservation struct {
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
	Kind     string `json:"kind"`
	Executor string `json:"executor"`
	Current  bool   `json:"current"`
}

type StackObservation struct {
	Depth       int    `json:"depth"`
	MachineID   string `json:"machine_id"`
	MachineName string `json:"machine_name,omitempty"`
	MachineKind string `json:"machine_kind"`
	ResumeState string `json:"resume_state"`
	Kind        string `json:"kind"`
	StepName    string `json:"step_name,omitempty"`
	Iteration   int    `json:"iteration"`
}

type LimitObservation struct {
	Key                string `json:"key"`
	Kind               string `json:"kind"`
	Value              any    `json:"value"`
	ActivatedAt        string `json:"activated_at"`
	ActivatedIteration int    `json:"activated_iteration"`
}

type VerificationObservation struct {
	Name    string `json:"name"`
	Command string `json:"command,omitempty"`
	Passed  bool   `json:"passed"`
	Error   string `json:"error,omitempty"`
	// Output is the tail of what a failing verifier printed.
	Output string `json:"output,omitempty"`
}

type EventObservation struct {
	RequestID     string                    `json:"request_id"`
	EventName     string                    `json:"event_name"`
	ToolName      string                    `json:"tool_name,omitempty"`
	ToolUseID     string                    `json:"tool_use_id,omitempty"`
	Command       string                    `json:"command,omitempty"`
	Decision      string                    `json:"decision"`
	Reason        string                    `json:"reason,omitempty"`
	Remediation   string                    `json:"remediation,omitempty"`
	DurationMS    int64                     `json:"duration_ms,omitempty"`
	CreatedAt     time.Time                 `json:"created_at"`
	Verifications []VerificationObservation `json:"verifications,omitempty"`
}

type RunObservationEventSummary struct {
	Events        int   `json:"events"`
	ToolCalls     int   `json:"tool_calls"`
	Denied        int   `json:"denied"`
	Verifications int   `json:"verifications"`
	FailedChecks  int   `json:"failed_checks"`
	ToolTimeMS    int64 `json:"tool_time_ms"`
}

func (s *Store) ObserveSessions(ctx context.Context) ([]SessionObservation, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT s.id, s.codex_session_id, w.root, s.created_at, s.updated_at,
p.id, p.agent_id, COALESCE(p.parent_process_id, ''), p.created_at, p.updated_at,
r.id, COALESCE(r.parent_run_id, ''), r.machine_json, r.state_id, r.status, r.iteration,
r.stack_json, r.result_json, r.termination_reason, r.created_at, r.updated_at
FROM sessions s
JOIN workspaces w ON w.id=s.workspace_id
JOIN processes p ON p.session_id=s.id
JOIN runs r ON r.id=COALESCE(p.current_run_id, (SELECT latest.id FROM runs latest WHERE latest.process_id=p.id ORDER BY latest.created_at DESC LIMIT 1))
ORDER BY s.updated_at DESC, (p.parent_process_id IS NOT NULL), p.created_at, p.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sessions []SessionObservation
	index := map[string]int{}
	for rows.Next() {
		var session SessionObservation
		var process ProcessObservation
		var run RunObservation
		var machineJSON, stackJSON, resultJSON []byte
		var sessionCreated, sessionUpdated, processCreated, processUpdated, runCreated, runUpdated string
		if err := rows.Scan(&session.ID, &session.CodexSessionID, &session.WorkspaceRoot, &sessionCreated, &sessionUpdated,
			&process.ID, &process.AgentID, &process.ParentProcessID, &processCreated, &processUpdated,
			&run.ID, &run.ParentRunID, &machineJSON, &run.StateID, &run.Status, &run.Iteration,
			&stackJSON, &resultJSON, &run.TerminationReason, &runCreated, &runUpdated); err != nil {
			return nil, err
		}
		var err error
		if session.CreatedAt, err = parseStoredTime(sessionCreated); err != nil {
			return nil, err
		}
		if session.UpdatedAt, err = parseStoredTime(sessionUpdated); err != nil {
			return nil, err
		}
		if process.CreatedAt, err = parseStoredTime(processCreated); err != nil {
			return nil, err
		}
		if process.UpdatedAt, err = parseStoredTime(processUpdated); err != nil {
			return nil, err
		}
		if err := populateRunObservation(&run, machineJSON, stackJSON, resultJSON, runCreated, runUpdated); err != nil {
			return nil, err
		}
		process.Run = &run
		position, exists := index[session.ID]
		if !exists {
			position = len(sessions)
			index[session.ID] = position
			sessions = append(sessions, session)
		}
		sessions[position].Processes = append(sessions[position].Processes, process)
	}
	return sessions, rows.Err()
}

func (s *Store) ObserveRun(ctx context.Context, runID string) (*RunObservationDetail, error) {
	run, err := s.GetRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	detail := &RunObservationDetail{WorkspaceRoot: run.WorkspaceRoot, ProcessID: run.ProcessID}
	var sessionCreated string
	err = s.db.QueryRowContext(ctx, `SELECT s.id, s.codex_session_id, p.agent_id, COALESCE(p.parent_process_id, ''), s.created_at
FROM processes p JOIN sessions s ON s.id=p.session_id WHERE p.id=?`, run.ProcessID).
		Scan(&detail.SessionID, &detail.CodexSessionID, &detail.AgentID, &detail.ParentProcessID, &sessionCreated)
	if err != nil {
		return nil, err
	}
	machineJSON, _ := json.Marshal(run.Machine)
	stackJSON, _ := json.Marshal(run.Stack)
	detail.Run.ID, detail.Run.ParentRunID, detail.Run.StateID = run.ID, run.ParentRunID, run.StateID
	detail.Run.Status, detail.Run.Iteration, detail.Run.TerminationReason = run.Status, run.Iteration, run.TerminationReason
	if err := populateRunObservation(&detail.Run, machineJSON, stackJSON, run.Result, formatTime(run.CreatedAt), formatTime(run.UpdatedAt)); err != nil {
		return nil, err
	}
	for _, state := range run.Machine.States {
		detail.States = append(detail.States, StateObservation{
			ID: state.ID, Name: state.Name, Kind: string(state.Kind), Executor: string(state.Executor), Current: state.ID == run.StateID,
		})
	}
	for index, frame := range run.Stack {
		detail.Stack = append(detail.Stack, StackObservation{
			Depth: index + 1, MachineID: frame.Machine.ID, MachineName: frame.Machine.Name, MachineKind: string(frame.Machine.Kind),
			ResumeState: frame.ResumeState, Kind: frame.Kind, StepName: frame.StepName, Iteration: frame.Iteration,
		})
	}
	detail.Limits = observeLimits(run.Variables)
	detail.Events, detail.Summary, err = s.observeRunEvents(ctx, runID)
	if err != nil {
		return nil, err
	}
	return detail, nil
}

func (s *Store) SetEventVerifications(ctx context.Context, requestID string, observations json.RawMessage) error {
	if len(observations) == 0 {
		observations = json.RawMessage(`[]`)
	}
	if !json.Valid(observations) {
		return errors.New("event verifications must be valid JSON")
	}
	_, err := s.db.ExecContext(ctx, `UPDATE events SET verification_json=? WHERE request_id=?`, []byte(observations), requestID)
	return err
}

func (s *Store) observeRunEvents(ctx context.Context, runID string) ([]EventObservation, RunObservationEventSummary, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT request_id, kind, payload_json, decision_json, verification_json, created_at
FROM events WHERE run_id=? ORDER BY created_at, request_id`, runID)
	if err != nil {
		return nil, RunObservationEventSummary{}, err
	}
	defer rows.Close()
	var events []EventObservation
	var summary RunObservationEventSummary
	started := map[string]time.Time{}
	for rows.Next() {
		var event EventObservation
		var payload, decisionJSON, verificationJSON []byte
		var created string
		if err := rows.Scan(&event.RequestID, &event.EventName, &payload, &decisionJSON, &verificationJSON, &created); err != nil {
			return nil, summary, err
		}
		var wire struct {
			ToolName  string `json:"tool_name"`
			ToolUseID string `json:"tool_use_id"`
			ToolInput struct {
				Command string `json:"command"`
			} `json:"tool_input"`
		}
		_ = json.Unmarshal(payload, &wire)
		event.ToolName, event.ToolUseID, event.Command = wire.ToolName, wire.ToolUseID, wire.ToolInput.Command
		var decision EventDecision
		_ = json.Unmarshal(decisionJSON, &decision)
		event.Decision, event.Reason, event.Remediation = decision.Decision, decision.Reason, decision.Remediation
		if len(verificationJSON) > 0 {
			_ = json.Unmarshal(verificationJSON, &event.Verifications)
		}
		event.CreatedAt, err = parseStoredTime(created)
		if err != nil {
			return nil, summary, err
		}
		if event.ToolUseID != "" && event.EventName == "PreToolUse" {
			started[event.ToolUseID] = event.CreatedAt
		}
		if event.ToolUseID != "" && event.EventName == "PostToolUse" {
			if beginning, exists := started[event.ToolUseID]; exists {
				event.DurationMS = event.CreatedAt.Sub(beginning).Milliseconds()
				if event.DurationMS < 0 {
					event.DurationMS = 0
				}
				summary.ToolTimeMS += event.DurationMS
			}
			summary.ToolCalls++
		}
		if event.Decision == "deny" {
			summary.Denied++
		}
		for _, verification := range event.Verifications {
			summary.Verifications++
			if !verification.Passed {
				summary.FailedChecks++
			}
		}
		summary.Events++
		events = append(events, event)
	}
	return events, summary, rows.Err()
}

func populateRunObservation(run *RunObservation, machineJSON, stackJSON, resultJSON []byte, created, updated string) error {
	var machine ir.Machine
	if err := json.Unmarshal(machineJSON, &machine); err != nil {
		return err
	}
	var stack []Frame
	if err := json.Unmarshal(stackJSON, &stack); err != nil {
		return err
	}
	run.MachineID, run.MachineName, run.MachineKind = machine.ID, machine.Name, string(machine.Kind)
	run.StackDepth = len(stack)
	for _, state := range machine.States {
		if state.ID == run.StateID {
			run.StateName, run.StateKind, run.Executor = state.Name, string(state.Kind), string(state.Executor)
			break
		}
	}
	if len(resultJSON) > 0 {
		_ = json.Unmarshal(resultJSON, &run.Result)
	}
	var err error
	if run.CreatedAt, err = parseStoredTime(created); err != nil {
		return err
	}
	if run.UpdatedAt, err = parseStoredTime(updated); err != nil {
		return err
	}
	return nil
}

func observeLimits(variables json.RawMessage) []LimitObservation {
	var environment map[string]json.RawMessage
	if json.Unmarshal(variables, &environment) != nil {
		return nil
	}
	var values map[string]struct {
		Kind        string `json:"kind"`
		Value       any    `json:"value"`
		ActivatedAt string `json:"activated_at"`
		Iteration   int    `json:"iteration"`
	}
	if json.Unmarshal(environment["__limits"], &values) != nil {
		return nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]LimitObservation, 0, len(keys))
	for _, key := range keys {
		value := values[key]
		result = append(result, LimitObservation{Key: key, Kind: value.Kind, Value: value.Value, ActivatedAt: value.ActivatedAt, ActivatedIteration: value.Iteration})
	}
	return result
}

func parseStoredTime(value string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, value)
}
