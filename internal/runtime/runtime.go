package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"hamster/internal/hook"
	"hamster/internal/ir"
	"hamster/internal/store"
)

type Runner struct {
	store        *store.Store
	locks        sync.Map
	clock        Clock
	verifier     Verifier
	obsMu        sync.Mutex
	observations map[string][]VerificationObservation
}

func New(state *store.Store) *Runner {
	return NewWithDependencies(state, RealClock{}, CommandVerifier{})
}

func NewWithDependencies(state *store.Store, clock Clock, verifier Verifier) *Runner {
	if clock == nil {
		clock = RealClock{}
	}
	if verifier == nil {
		verifier = CommandVerifier{}
	}
	return &Runner{store: state, clock: clock, verifier: verifier, observations: map[string][]VerificationObservation{}}
}

func (r *Runner) HandleHook(ctx context.Context, event hook.Event) (hook.Decision, error) {
	if event.EventName != "PostToolUse" {
		return hook.Decision{Permission: "allow"}, nil
	}
	run, err := r.store.FindCurrentRun(ctx, event.SessionID, event.AgentID)
	if err == sql.ErrNoRows {
		return hook.Decision{Permission: "allow"}, nil
	}
	if err != nil {
		return hook.Decision{}, err
	}
	lock := r.runLock(run.ID)
	lock.Lock()
	defer lock.Unlock()
	if existing, eventErr := r.store.GetEventDecision(ctx, event.RequestID()); eventErr == nil {
		return decisionFromEvent(existing, run.ID), nil
	} else if eventErr != sql.ErrNoRows {
		return hook.Decision{}, eventErr
	}
	run, err = r.store.GetRun(ctx, run.ID)
	if err != nil {
		return hook.Decision{}, err
	}
	expectedState := run.StateID
	environment, err := decodeEnvironment(run.Variables)
	if err != nil {
		return hook.Decision{}, err
	}
	state, ok := currentState(run)
	if !ok {
		return hook.Decision{}, fmt.Errorf("state %q does not exist", run.StateID)
	}
	if state.Kind == ir.StateStep {
		if !toolSucceeded(event) {
			decision := hook.Decision{Permission: "allow", Tracked: true, RunID: run.ID, Reason: "The step failed and remains current."}
			return decision, r.recordWithoutAdvance(ctx, event, run, decision)
		}
		environment[state.Name] = decodeToolResponse(event.ToolResponse)
		run.StateID = state.Next
	} else if state.Kind == ir.StateAgentWork {
		decision := hook.Decision{Permission: "allow", Tracked: true, RunID: run.ID}
		return decision, r.recordWithoutAdvance(ctx, event, run, decision)
	} else {
		decision := hook.Decision{Permission: "allow", Tracked: true, RunID: run.ID}
		return decision, r.recordWithoutAdvance(ctx, event, run, decision)
	}

	if err := r.pump(ctx, run, environment); err != nil {
		return hook.Decision{}, err
	}
	run.Variables, _ = json.Marshal(environment)
	decision := hook.Decision{Permission: "allow", Tracked: true, RunID: run.ID}
	payload := event.Raw
	if len(payload) == 0 {
		payload = hook.MarshalEvent(event)
	}
	_, err = r.store.ApplyEvent(ctx, event.RequestID(), event.EventName, payload,
		store.EventDecision{Decision: "allow"}, expectedState, run)
	if err != nil {
		return decision, err
	}
	if run.Status == "complete" {
		err = r.store.FinalizeRun(ctx, run.ID)
	}
	return decision, err
}

// SignalGoalAchieved is the only operation that advances declarative agent
// work. Tool calls made while working are observations, not claims of
// completion; the explicit signal moves into independent verification.
func (r *Runner) SignalGoalAchieved(ctx context.Context, event hook.Event) (hook.Decision, error) {
	if existing, err := r.store.GetEventDecision(ctx, event.RequestID()); err == nil {
		return decisionFromEvent(existing, existing.RunID), nil
	} else if err != sql.ErrNoRows {
		return hook.Decision{}, err
	}

	run, err := r.store.FindCurrentRun(ctx, event.SessionID, event.AgentID)
	if err == sql.ErrNoRows {
		decision := hook.Decision{
			Permission: "deny", Reason: "goal-achieved requires a bound Hamster run",
			Remediation: "Start a loop with hamster run, then signal only when its declarative work is ready for verification.",
		}
		return decision, r.recordWithoutAdvance(ctx, event, &store.Run{}, decision)
	}
	if err != nil {
		return hook.Decision{}, err
	}

	lock := r.runLock(run.ID)
	lock.Lock()
	defer lock.Unlock()
	if existing, eventErr := r.store.GetEventDecision(ctx, event.RequestID()); eventErr == nil {
		return decisionFromEvent(existing, run.ID), nil
	} else if eventErr != sql.ErrNoRows {
		return hook.Decision{}, eventErr
	}
	run, err = r.store.GetRun(ctx, run.ID)
	if err != nil {
		return hook.Decision{}, err
	}
	state, ok := currentState(run)
	if !ok {
		return hook.Decision{}, fmt.Errorf("state %q does not exist", run.StateID)
	}
	if state.Kind != ir.StateAgentWork {
		decision := hook.Decision{
			Permission: "deny", Tracked: true, RunID: run.ID,
			Reason:      fmt.Sprintf("run %s is at %s, not declarative agent work", run.ID, state.Kind),
			Remediation: "Complete the current imperative step before signaling goal-achieved.",
		}
		return decision, r.recordWithoutAdvance(ctx, event, run, decision)
	}

	expectedState := run.StateID
	environment, err := decodeEnvironment(run.Variables)
	if err != nil {
		return hook.Decision{}, err
	}
	observationOffset := r.observationCount(run.ID)
	run.StateID = state.Next
	if err := r.pump(ctx, run, environment); err != nil {
		return hook.Decision{}, err
	}
	run.Variables, _ = json.Marshal(environment)
	observations := r.observationsAfter(run.ID, observationOffset)
	decision := signalDecision(run, observations)
	payload := event.Raw
	if len(payload) == 0 {
		payload = hook.MarshalEvent(event)
	}
	storedDecision := store.EventDecision{
		Decision: decision.Permission, Reason: decision.Reason, Remediation: decision.Remediation,
		Context: decision.Context, RunID: run.ID,
	}
	replayed, err := r.store.ApplyEvent(ctx, event.RequestID(), "goal-achieved", payload, storedDecision, expectedState, run)
	if err != nil {
		return hook.Decision{}, err
	}
	if replayed {
		existing, getErr := r.store.GetEventDecision(ctx, event.RequestID())
		if getErr != nil {
			return hook.Decision{}, getErr
		}
		return decisionFromEvent(existing, run.ID), nil
	}
	if run.Status == "complete" {
		err = r.store.FinalizeRun(ctx, run.ID)
	}
	return decision, err
}

func signalDecision(run *store.Run, observations []VerificationObservation) hook.Decision {
	decision := hook.Decision{Permission: "allow", Tracked: true, RunID: run.ID}
	if run.Status == "complete" {
		decision.Context = fmt.Sprintf("Hamster independently verified run %s and completed it with result %s.", run.ID, string(run.Result))
		return decision
	}
	if run.Status != "running" {
		decision.Context = fmt.Sprintf("Hamster verification terminated run %s with status %s and result %s. Escalate or use the kill switch before continuing outside this loop.", run.ID, run.Status, string(run.Result))
		return decision
	}
	failed := make([]string, 0)
	for _, observation := range observations {
		if observation.Passed && observation.Error == "" {
			continue
		}
		detail := observation.Name
		if observation.Error != "" {
			detail += ": " + observation.Error
		}
		failed = append(failed, detail)
	}
	if len(failed) > 0 {
		decision.Context = fmt.Sprintf("Hamster rejected the completion claim for run %s. Failed verification: %s. Continue the declarative work at iteration %d, then run hamster signal goal-achieved again.", run.ID, strings.Join(failed, "; "), run.Iteration)
		return decision
	}
	decision.Context = fmt.Sprintf("Hamster accepted the completion signal for run %s and advanced to %s.", run.ID, run.StateID)
	return decision
}

func decisionFromEvent(existing store.EventDecision, fallbackRunID string) hook.Decision {
	runID := existing.RunID
	if runID == "" {
		runID = fallbackRunID
	}
	return hook.Decision{
		Permission: existing.Decision, Reason: existing.Reason, Remediation: existing.Remediation,
		Context: existing.Context, Tracked: runID != "", RunID: runID,
	}
}

func (r *Runner) Start(ctx context.Context, runID string) (*store.Run, error) {
	lock := r.runLock(runID)
	lock.Lock()
	defer lock.Unlock()
	run, err := r.store.GetRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	expected := run.StateID
	environment, err := decodeEnvironment(run.Variables)
	if err != nil {
		return nil, err
	}
	if err := r.pump(ctx, run, environment); err != nil {
		return nil, err
	}
	run.Variables, _ = json.Marshal(environment)
	if err := r.store.UpdateRun(ctx, expected, run); err != nil {
		return nil, err
	}
	if run.Status == "complete" {
		if err := r.store.FinalizeRun(ctx, run.ID); err != nil {
			return nil, err
		}
	}
	return run, nil
}

func (r *Runner) pump(ctx context.Context, run *store.Run, environment Environment) error {
	for transitions := 0; transitions < 10_000; transitions++ {
		state, ok := currentState(run)
		if !ok {
			return fmt.Errorf("machine %s has no state %s", run.Machine.ID, run.StateID)
		}
		switch state.Kind {
		case ir.StateAgentWork, ir.StateStep:
			_, err := enforceLimits(run, environment, r.clock.Now())
			return err
		case ir.StatePolicy:
			run.StateID = state.Next
		case ir.StateLimit:
			activateLimit(environment, run.Machine, state, run.Iteration, r.clock.Now())
			run.StateID = state.Next
		case ir.StateVerify:
			value, err := r.evaluator(run).Evaluate(ctx, *state.Expression, environment)
			if err != nil {
				r.recordVerification(run.ID, VerificationObservation{Name: state.Name, Command: verificationCommand(state.Expression), Error: err.Error()})
				environment[state.Name] = false
				run.StateID = state.Next
				continue
			}
			passed := truthy(value)
			r.recordVerification(run.ID, VerificationObservation{Name: state.Name, Command: verificationCommand(state.Expression), Passed: passed})
			environment[state.Name] = passed
			run.StateID = state.Next
		case ir.StateNested:
			parentVariables, _ := json.Marshal(environment)
			run.Stack = append(run.Stack, store.Frame{Machine: run.Machine, ResumeState: state.Next, Kind: "nested", StepName: state.Name, Iteration: run.Iteration, Variables: parentVariables})
			run.Machine, run.StateID = *state.Nested, state.Nested.InitialState
			run.Iteration = 0
		case ir.StateSequence:
			value, err := r.evaluator(run).Evaluate(ctx, run.Machine.Foreach.Sequence, environment)
			if err != nil {
				return err
			}
			items, ok := value.([]any)
			if !ok {
				return fmt.Errorf("foreach sequence is %T, want list", value)
			}
			environment[foreachKey(run.Machine, "items")] = items
			environment[foreachKey(run.Machine, "index")] = float64(0)
			environment[foreachKey(run.Machine, "results")] = []any{}
			if len(items) == 0 {
				environment[foreachKey(run.Machine, "result")] = []any{}
				run.StateID = completeState(run.Machine)
			} else {
				run.StateID = state.Next
			}
		case ir.StateElement:
			items, index, err := foreachPosition(run.Machine, environment)
			if err != nil {
				return err
			}
			environment[run.Machine.Foreach.Binding] = items[index]
			parentVariables, _ := json.Marshal(environment)
			run.Stack = append(run.Stack, store.Frame{Machine: run.Machine, ResumeState: state.Next, Kind: "foreach", Iteration: run.Iteration, Variables: parentVariables})
			run.Machine, run.StateID = *state.Nested, state.Nested.InitialState
			run.Iteration = 0
		case ir.StateBoundary:
			if run.Machine.Kind == ir.LoopForeach {
				items, index, err := foreachPosition(run.Machine, environment)
				if err != nil {
					return err
				}
				index++
				if index < len(items) {
					environment[foreachKey(run.Machine, "index")] = float64(index)
					run.StateID = state.OnFailure
				} else {
					environment[foreachKey(run.Machine, "result")] = environment[foreachKey(run.Machine, "results")]
					run.StateID = state.OnSuccess
				}
				continue
			}
			if verificationsPass(run.Machine, environment) {
				run.StateID = state.OnSuccess
			} else {
				run.Iteration++
				clearVerifications(run.Machine, environment)
				exceeded, err := enforceLimits(run, environment, r.clock.Now())
				if err != nil {
					return err
				}
				if exceeded {
					return nil
				}
				run.StateID = state.OnFailure
			}
		case ir.StateComplete:
			result, err := r.loopResult(ctx, run, environment)
			if err != nil {
				return err
			}
			if len(run.Stack) == 0 {
				run.Status = "complete"
				run.Result, _ = json.Marshal(result)
				return nil
			}
			frame := run.Stack[len(run.Stack)-1]
			run.Stack = run.Stack[:len(run.Stack)-1]
			run.Machine, run.StateID = frame.Machine, frame.ResumeState
			run.Iteration = frame.Iteration
			parentEnvironment, err := decodeEnvironment(frame.Variables)
			if err != nil {
				return err
			}
			for key := range environment {
				delete(environment, key)
			}
			for key, value := range parentEnvironment {
				environment[key] = value
			}
			if frame.Kind == "foreach" {
				key := foreachKey(run.Machine, "results")
				results, _ := environment[key].([]any)
				environment[key] = append(results, result)
			} else {
				environment[frame.StepName] = result
			}
		default:
			return fmt.Errorf("runtime cannot process state kind %s", state.Kind)
		}
	}
	return fmt.Errorf("machine exceeded internal transition safety limit")
}

func (r *Runner) loopResult(ctx context.Context, run *store.Run, environment Environment) (any, error) {
	if run.Machine.Kind == ir.LoopForeach {
		return environment[foreachKey(run.Machine, "result")], nil
	}
	if run.Machine.Return == nil {
		return nil, nil
	}
	value, err := r.evaluator(run).Evaluate(ctx, *run.Machine.Return, environment)
	if err != nil {
		return nil, fmt.Errorf("evaluate return: %w", err)
	}
	return value, nil
}

func enforceLimits(run *store.Run, environment Environment, now time.Time) (bool, error) {
	limits, _ := environment["__limits"].(map[string]any)
	for _, raw := range limits {
		limit, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		kind, _ := limit["kind"].(string)
		switch kind {
		case "attempts":
			maximum, err := strconv.Atoi(limit["value"].(string))
			if err != nil {
				return false, err
			}
			activated := int(limit["iteration"].(float64))
			if run.Iteration-activated >= maximum {
				setLimitExceeded(run, kind, maximum)
				return true, nil
			}
		case "time":
			duration, err := time.ParseDuration(limit["value"].(string))
			if err != nil {
				return false, err
			}
			activated, err := time.Parse(time.RFC3339Nano, limit["activated_at"].(string))
			if err != nil {
				return false, err
			}
			if now.Sub(activated) >= duration {
				setLimitExceeded(run, kind, duration.String())
				return true, nil
			}
		}
	}
	return false, nil
}

func setLimitExceeded(run *store.Run, kind string, value any) {
	run.Status = "limit_exceeded"
	run.Result, _ = json.Marshal(map[string]any{"limit": kind, "value": value})
}

func activateLimit(environment Environment, machine ir.Machine, state ir.State, iteration int, now time.Time) {
	limits, ok := environment["__limits"].(map[string]any)
	if !ok {
		limits = map[string]any{}
		environment["__limits"] = limits
	}
	key := machine.ID + ":" + state.ID
	if _, exists := limits[key]; exists {
		return
	}
	limits[key] = map[string]any{
		"kind": state.Limit.Kind, "value": state.Limit.Value.Value,
		"activated_at": now.UTC().Format(time.RFC3339Nano), "iteration": float64(iteration),
	}
}

func verificationsPass(machine ir.Machine, environment Environment) bool {
	for _, state := range machine.States {
		if state.Kind == ir.StateVerify && !truthy(environment[state.Name]) {
			return false
		}
	}
	return true
}

func clearVerifications(machine ir.Machine, environment Environment) {
	for _, state := range machine.States {
		if state.Kind == ir.StateVerify {
			delete(environment, state.Name)
		}
	}
}

func currentState(run *store.Run) (ir.State, bool) {
	for _, state := range run.Machine.States {
		if state.ID == run.StateID {
			return state, true
		}
	}
	return ir.State{}, false
}

func completeState(machine ir.Machine) string {
	for _, state := range machine.States {
		if state.Kind == ir.StateComplete {
			return state.ID
		}
	}
	return ""
}

func foreachPosition(machine ir.Machine, environment Environment) ([]any, int, error) {
	items, ok := environment[foreachKey(machine, "items")].([]any)
	if !ok {
		return nil, 0, fmt.Errorf("foreach items are unavailable")
	}
	indexValue, ok := environment[foreachKey(machine, "index")].(float64)
	if !ok || int(indexValue) >= len(items) {
		return nil, 0, fmt.Errorf("foreach index is invalid")
	}
	return items, int(indexValue), nil
}

func decodeEnvironment(payload json.RawMessage) (Environment, error) {
	environment := Environment{}
	if len(payload) == 0 {
		return environment, nil
	}
	err := json.Unmarshal(payload, &environment)
	return environment, err
}

func decodeToolResponse(payload json.RawMessage) any {
	if len(payload) == 0 {
		return nil
	}
	var value any
	if json.Unmarshal(payload, &value) == nil {
		return value
	}
	return string(payload)
}

func toolSucceeded(event hook.Event) bool {
	var response map[string]any
	if json.Unmarshal(event.ToolResponse, &response) != nil {
		return true
	}
	if exitCode, ok := response["exit_code"].(float64); ok {
		return exitCode == 0
	}
	if success, ok := response["success"].(bool); ok {
		return success
	}
	return true
}

func foreachKey(machine ir.Machine, suffix string) string {
	return strings.Join([]string{"__foreach", machine.ID, suffix}, ":")
}

func (r *Runner) runLock(runID string) *sync.Mutex {
	value, _ := r.locks.LoadOrStore(runID, &sync.Mutex{})
	return value.(*sync.Mutex)
}

func (r *Runner) evaluator(run *store.Run) Evaluator {
	return Evaluator{WorkingDirectory: run.WorkspaceRoot, Verifier: r.verifier}
}

func verificationCommand(expression *ir.Expression) string {
	if expression == nil || expression.Kind != ir.ExpressionCall || expression.Callee == nil || expressionPath(*expression.Callee) != "bash.check" || len(expression.Arguments) == 0 {
		return ""
	}
	argument := expression.Arguments[0].Value
	if argument.Kind == ir.ExpressionLiteral && argument.ValueKind == "string" {
		return argument.Value
	}
	return ""
}

func (r *Runner) recordVerification(runID string, observation VerificationObservation) {
	r.obsMu.Lock()
	defer r.obsMu.Unlock()
	r.observations[runID] = append(r.observations[runID], observation)
}

func (r *Runner) TakeObservations(runID string) []VerificationObservation {
	if runID == "" {
		return nil
	}
	r.obsMu.Lock()
	defer r.obsMu.Unlock()
	observations := append([]VerificationObservation(nil), r.observations[runID]...)
	delete(r.observations, runID)
	return observations
}

func (r *Runner) observationCount(runID string) int {
	r.obsMu.Lock()
	defer r.obsMu.Unlock()
	return len(r.observations[runID])
}

func (r *Runner) observationsAfter(runID string, offset int) []VerificationObservation {
	r.obsMu.Lock()
	defer r.obsMu.Unlock()
	observations := r.observations[runID]
	if offset < 0 || offset > len(observations) {
		offset = len(observations)
	}
	return append([]VerificationObservation(nil), observations[offset:]...)
}

func (r *Runner) recordWithoutAdvance(ctx context.Context, event hook.Event, run *store.Run, decision hook.Decision) error {
	payload := event.Raw
	if len(payload) == 0 {
		payload = hook.MarshalEvent(event)
	}
	_, _, err := r.store.RecordEvent(ctx, event.RequestID(), run.ID, event.EventName, payload,
		store.EventDecision{Decision: decision.Permission, Reason: decision.Reason, Remediation: decision.Remediation, Context: decision.Context, RunID: decision.RunID})
	return err
}
