package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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
	collector    Collector
	obsMu        sync.Mutex
	observations map[string][]VerificationObservation
	execResolver func(script, workspace string) (ir.Machine, error)
}

func New(state *store.Store) *Runner {
	return NewWithDependencies(state, RealClock{}, CommandVerifier{})
}

func (r *Runner) SetExecResolver(resolver func(script, workspace string) (ir.Machine, error)) {
	r.execResolver = resolver
}

// SetCollector replaces evidence gathering, which lets judgement tests run
// without executing captured commands.
func (r *Runner) SetCollector(collector Collector) {
	if collector != nil {
		r.collector = collector
	}
}

func NewWithDependencies(state *store.Store, clock Clock, verifier Verifier) *Runner {
	if clock == nil {
		clock = RealClock{}
	}
	if verifier == nil {
		verifier = CommandVerifier{}
	}
	return &Runner{store: state, clock: clock, verifier: verifier, collector: CommandCollector{}, observations: map[string][]VerificationObservation{}}
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
	observationOffset := r.observationCount(run.ID)
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
	} else if state.Kind == ir.StateFork {
		if !toolSucceeded(event) {
			decision := hook.Decision{Permission: "allow", Tracked: true, RunID: run.ID, Reason: "The fork failed and remains current."}
			return decision, r.recordWithoutAdvance(ctx, event, run, decision)
		}
		response, _ := decodeToolResponse(event.ToolResponse).(map[string]any)
		childRunID, _ := response["hamster_child_run_id"].(string)
		if childRunID == "" {
			return hook.Decision{}, errors.New("fork PostToolUse is missing the Hamster child run ID")
		}
		environment[state.Name] = map[string]any{
			"parent": true, "child": false, "run_id": childRunID,
			"tool": response,
		}
		run.StateID = state.Next
	} else if state.Kind == ir.StateAgentWork {
		run.StateID = state.Next
	} else {
		decision := hook.Decision{Permission: "allow", Tracked: true, RunID: run.ID}
		return decision, r.recordWithoutAdvance(ctx, event, run, decision)
	}

	if err := r.pump(ctx, run, environment); err != nil {
		return hook.Decision{}, err
	}
	run.Variables, _ = json.Marshal(environment)
	observations := r.observationsAfter(run.ID, observationOffset)
	decision := hook.Decision{Permission: "allow", Tracked: true, RunID: run.ID}
	if len(observations) > 0 || terminalStatus(run.Status) || run.Status == store.TrapStatusID {
		decision = signalDecision(run, observations)
	}
	payload := event.Raw
	if len(payload) == 0 {
		payload = hook.MarshalEvent(event)
	}
	_, err = r.store.ApplyEvent(ctx, event.RequestID(), event.EventName, payload,
		store.EventDecision{Decision: decision.Permission, Context: decision.Context, RunID: run.ID}, expectedState, run)
	if err != nil {
		return decision, err
	}
	if terminalStatus(run.Status) {
		err = r.store.FinalizeRun(ctx, run.ID)
		if err == nil && run.ParentRunID != "" {
			_, err = r.Start(ctx, run.ParentRunID)
		}
	}
	return decision, err
}

// AdvanceAgentWork moves a run past its current agent-work state and pumps.
// Control commands never produce a PostToolUse probe, so an agent whose whole
// job is a control command — an observer recording its verdict — needs an
// explicit way to finish, or its parent would wait on it forever.
func (r *Runner) AdvanceAgentWork(ctx context.Context, runID string) (*store.Run, error) {
	lock := r.runLock(runID)
	lock.Lock()
	defer lock.Unlock()
	run, err := r.store.GetRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	state, ok := currentState(run)
	if !ok || state.Kind != ir.StateAgentWork {
		return run, nil
	}
	expected := run.StateID
	environment, err := decodeEnvironment(run.Variables)
	if err != nil {
		return nil, err
	}
	run.StateID = state.Next
	if err := r.pump(ctx, run, environment); err != nil {
		return nil, err
	}
	run.Variables, _ = json.Marshal(environment)
	if err := r.store.UpdateRun(ctx, expected, run); err != nil {
		return nil, err
	}
	if terminalStatus(run.Status) {
		if err := r.store.FinalizeRun(ctx, run.ID); err != nil {
			return nil, err
		}
		if run.ParentRunID != "" {
			if _, err := r.Start(ctx, run.ParentRunID); err != nil {
				return nil, err
			}
		}
	}
	return run, nil
}

func (r *Runner) ResumeForkChild(ctx context.Context, runID string) (*store.Run, error) {
	lock := r.runLock(runID)
	lock.Lock()
	defer lock.Unlock()
	run, err := r.store.GetRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	state, ok := currentState(run)
	if !ok || state.Kind != ir.StateFork {
		return run, nil
	}
	expected := run.StateID
	environment, err := decodeEnvironment(run.Variables)
	if err != nil {
		return nil, err
	}
	environment[state.Name] = map[string]any{
		"parent": false, "child": true, "run_id": run.ID,
	}
	run.StateID = state.Next
	if err := r.pump(ctx, run, environment); err != nil {
		return nil, err
	}
	run.Variables, _ = json.Marshal(environment)
	if err := r.store.UpdateRun(ctx, expected, run); err != nil {
		return nil, err
	}
	return run, nil
}

// SignalGoalAchieved requests an immediate declarative probe. Normal goal
// progress no longer depends on it because every PostToolUse probes as well.
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
	if terminalStatus(run.Status) {
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
	if run.Status == store.TrapStatusID {
		decision.Context = fmt.Sprintf("Hamster suspended run %s at a trap and is waiting for the supervisor. Stop working on this loop; the run resumes only when a human resolves the request with hamster resolve.", run.ID)
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
		decision.Context = fmt.Sprintf("Hamster verification failed for run %s at %s. Iteration %d is ready for the next permitted action.", run.ID, strings.Join(failed, "; "), run.Iteration)
		return decision
	}
	decision.Context = fmt.Sprintf("Hamster verified the current goal in run %s and advanced to %s.", run.ID, run.StateID)
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
	if terminalStatus(run.Status) {
		if err := r.store.FinalizeRun(ctx, run.ID); err != nil {
			return nil, err
		}
		if run.ParentRunID != "" {
			if _, err := r.Start(ctx, run.ParentRunID); err != nil {
				return nil, err
			}
		}
	}
	return run, nil
}

func terminalStatus(status string) bool {
	return status == "complete" || status == "killed" || status == "failed" || status == "limit_exceeded"
}

func (r *Runner) pump(ctx context.Context, run *store.Run, environment Environment) error {
	for transitions := 0; transitions < 10_000; transitions++ {
		state, ok := currentState(run)
		if !ok {
			return fmt.Errorf("machine %s has no state %s", run.Machine.ID, run.StateID)
		}
		switch state.Kind {
		case ir.StateAgentWork, ir.StateStep, ir.StateFork:
			_, err := enforceLimits(run, environment, r.clock.Now())
			return err
		case ir.StateBranch:
			value, err := r.evaluator(run).Evaluate(ctx, *state.Expression, environment)
			if err != nil {
				return err
			}
			if truthy(value) {
				run.StateID = state.OnSuccess
			} else {
				run.StateID = state.OnFailure
			}
		case ir.StateExec:
			var machine ir.Machine
			if state.Machine != nil {
				// A synthesized observer has no script on disk; the compiler
				// emitted the program the child is about to become.
				machine = *state.Machine
				if state.JudgeRef != "" {
					if err := r.store.BindObserver(ctx, run.ParentRunID, state.JudgeRef, run.Iteration, run.ID); err != nil {
						return fmt.Errorf("bind observer: %w", err)
					}
				}
			} else {
				if r.execResolver == nil {
					return errors.New("runtime exec resolver is unavailable")
				}
				resolved, err := r.execResolver(state.Script, run.WorkspaceRoot)
				if err != nil {
					return fmt.Errorf("exec %s: %w", state.Script, err)
				}
				machine = resolved
			}
			for key := range environment {
				delete(environment, key)
			}
			run.Machine = machine
			run.StateID = machine.InitialState
			run.Status = "running"
			run.Iteration = 0
			run.Stack = nil
			run.ResetProcessState = true
		case ir.StateWait:
			value, err := r.evaluator(run).Evaluate(ctx, *state.Expression, environment)
			if err != nil {
				return err
			}
			handles, err := forkHandles(value)
			if err != nil {
				return err
			}
			ready := true
			for _, handle := range handles {
				childRunID, _ := handle["run_id"].(string)
				waited, waitErr := r.store.WaitRun(ctx, run.ID, childRunID)
				if waitErr != nil {
					return waitErr
				}
				if !waited.Ready {
					ready = false
					continue
				}
				handle["status"] = waited.ChildStatus
				handle["result"] = decodeToolResponse(waited.Result)
			}
			if !ready {
				run.Status = "waiting"
				return nil
			}
			run.Status = "running"
			run.StateID = state.Next
		case ir.StateJudge:
			advanced, err := r.openJudgement(ctx, run, state, environment)
			if err != nil {
				return err
			}
			if !advanced {
				return nil
			}
		case ir.StateVerdict:
			advanced, err := r.applyVerdict(ctx, run, state, environment)
			if err != nil {
				return err
			}
			if !advanced {
				return nil
			}
		case ir.StateTrap:
			resolved, err := r.resolveTrap(ctx, run, state, environment)
			if err != nil {
				return err
			}
			if !resolved {
				return nil
			}
		case ir.StatePolicy:
			run.StateID = state.Next
		case ir.StateLimit:
			activateLimit(environment, run.Machine, state, run.Iteration, r.clock.Now())
			run.StateID = state.Next
		case ir.StateVerify:
			value, err := r.evaluator(run).Evaluate(ctx, *state.Expression, environment)
			if err != nil {
				r.recordVerification(run.ID, VerificationObservation{Name: state.Name, Command: verificationCommand(state.Expression), Error: err.Error()})
				if err := failVerification(run, environment, state, r.clock.Now()); err != nil {
					return err
				}
				if terminalStatus(run.Status) {
					return nil
				}
				continue
			}
			passed := truthy(value)
			r.recordVerification(run.ID, VerificationObservation{Name: state.Name, Command: verificationCommand(state.Expression), Passed: passed})
			environment[state.Name] = passed
			if !passed {
				if err := failVerification(run, environment, state, r.clock.Now()); err != nil {
					return err
				}
				if terminalStatus(run.Status) {
					return nil
				}
				continue
			}
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
			exceeded, err := enforceLimits(run, environment, r.clock.Now())
			if err != nil {
				return err
			}
			if exceeded {
				return nil
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

// openJudgement prepares an observation. It resolves the ratified clause the
// observer will apply and freezes the evidence it is allowed to see. A clause
// the supervisor has not ratified gives the observer no standing at all, so the
// judgement escalates to a human instead of being guessed at.
func (r *Runner) openJudgement(ctx context.Context, run *store.Run, state ir.State, environment Environment) (bool, error) {
	if state.Judge == nil {
		return false, fmt.Errorf("judge state %s has no request", state.ID)
	}
	_, err := r.store.FindJudgement(ctx, run.ID, state.ID, run.Iteration)
	if err == nil {
		run.StateID = state.Next
		return true, nil
	}
	if err != sql.ErrNoRows {
		return false, err
	}

	clauses, err := r.store.ListTaste(ctx, run.WorkspaceRoot, true)
	if err != nil {
		return false, err
	}
	var clause *store.TasteClause
	for index := range clauses {
		if clauses[index].Name == state.Judge.Clause {
			clause = &clauses[index]
			break
		}
	}
	if clause == nil {
		environment[state.Name] = map[string]any{
			"verdict": store.VerdictAbstain,
			"reason":  fmt.Sprintf("no ratified taste clause named %q", state.Judge.Clause),
		}
		run.StateID = state.Judge.Escalate
		return true, nil
	}
	if exceeded, err := r.enforceJudgementLimit(ctx, run, environment); err != nil || exceeded {
		return false, err
	}

	evidence, err := r.collectEvidence(ctx, run, state, environment)
	if err != nil {
		return false, err
	}
	if _, err := r.store.OpenJudgement(ctx, store.OpenJudgementParams{
		ParentRunID: run.ID, StateID: state.ID, Iteration: run.Iteration, Name: state.Judge.Clause,
		Question: clause.Question, Clause: clause.Clause, Evidence: evidence,
	}); err != nil {
		return false, err
	}
	run.StateID = state.Next
	return true, nil
}

func (r *Runner) collectEvidence(ctx context.Context, run *store.Run, state ir.State, environment Environment) ([]store.Evidence, error) {
	evaluator := r.evaluator(run)
	collector := r.collector
	if collector == nil {
		collector = CommandCollector{}
	}
	evidence := make([]store.Evidence, 0, len(state.Judge.Evidence))
	for _, expression := range state.Judge.Evidence {
		value, err := evaluator.Evaluate(ctx, expression, environment)
		if err != nil {
			return nil, fmt.Errorf("evaluate evidence: %w", err)
		}
		command, ok := value.(string)
		if !ok {
			return nil, errors.New("evidence must evaluate to a command string")
		}
		output, err := collector.Collect(ctx, run.WorkspaceRoot, command)
		record := store.Evidence{Command: command, Output: output}
		if err != nil {
			record.Error = err.Error()
		}
		evidence = append(evidence, record)
	}
	return evidence, nil
}

// applyVerdict dispatches what the observer said. The three answers are not
// symmetric: a pass advances, a fail re-derives the work like any failed gate,
// and an abstention is not an answer at all — it is the observer admitting the
// profile did not cover this, which is exactly when a human must decide.
func (r *Runner) applyVerdict(ctx context.Context, run *store.Run, state ir.State, environment Environment) (bool, error) {
	if state.Judge == nil || state.JudgeRef == "" {
		return false, fmt.Errorf("verdict state %s has no judgement", state.ID)
	}
	judgement, err := r.store.FindJudgement(ctx, run.ID, state.JudgeRef, run.Iteration)
	if err != nil && err != sql.ErrNoRows {
		return false, err
	}

	verdict, citation, reasoning := store.VerdictAbstain, "", "the observer ended without recording a verdict"
	if err == nil && judgement.Status == store.JudgementRecorded {
		verdict, citation, reasoning = judgement.Verdict, judgement.Citation, judgement.Reasoning
	}
	environment[state.Name] = map[string]any{
		"verdict": verdict, "citation": citation, "reasoning": reasoning,
		"passed": verdict == store.VerdictPass,
	}
	r.recordVerification(run.ID, VerificationObservation{
		Name:   state.Name + " (judged)",
		Passed: verdict == store.VerdictPass,
		Error:  judgementDetail(verdict, citation, reasoning),
	})

	switch verdict {
	case store.VerdictPass:
		if judgement != nil && state.Judge.AuditRate > 0 {
			if _, err := r.store.SampleForAudit(ctx, judgement.ID, state.Judge.AuditRate); err != nil {
				return false, err
			}
		}
		run.StateID = state.Next
		return true, nil
	case store.VerdictFail:
		// Not every taste failure is one the agent can perceive. After enough
		// consecutive failures the loop is no longer converging, and repeating
		// it only spends attempts on work that will not improve.
		failures, err := r.store.CountConsecutiveFailures(ctx, run.ID, state.JudgeRef)
		if err != nil {
			return false, err
		}
		if state.Judge.EscalateAfter > 0 && failures >= state.Judge.EscalateAfter {
			run.StateID = state.Judge.Escalate
			return true, nil
		}
		if err := failVerification(run, environment, state, r.clock.Now()); err != nil {
			return false, err
		}
		return !terminalStatus(run.Status), nil
	default:
		run.StateID = state.Judge.Escalate
		return true, nil
	}
}

func judgementDetail(verdict, citation, reasoning string) string {
	if verdict == store.VerdictPass {
		return ""
	}
	detail := verdict
	if citation != "" {
		detail += " against " + citation
	}
	if reasoning != "" {
		detail += ": " + reasoning
	}
	return detail
}

// enforceJudgementLimit budgets the money an observer costs, which is a
// different resource from the attention a trap costs.
func (r *Runner) enforceJudgementLimit(ctx context.Context, run *store.Run, environment Environment) (bool, error) {
	maximum, ok := limitValue(environment, "judgements")
	if !ok {
		return false, nil
	}
	opened, err := r.store.CountJudgements(ctx, run.ID)
	if err != nil {
		return false, err
	}
	if opened < maximum {
		return false, nil
	}
	setLimitExceeded(run, "judgements", maximum)
	return true, nil
}

// resolveTrap raises the supervisor request for this exact occurrence of a trap
// state and reports whether the machine may advance. An unanswered trap
// suspends the run; Hamster never guesses a verdict on the supervisor's behalf.
func (r *Runner) resolveTrap(ctx context.Context, run *store.Run, state ir.State, environment Environment) (bool, error) {
	if state.Trap == nil {
		return false, fmt.Errorf("trap state %s has no request", state.ID)
	}
	existing, err := r.store.FindTrapOccurrence(ctx, run.ID, state.ID, run.Iteration)
	if err != nil && err != sql.ErrNoRows {
		return false, err
	}
	trap := existing
	if err == sql.ErrNoRows {
		if exceeded, limitErr := r.enforceTrapLimit(ctx, run, environment); limitErr != nil || exceeded {
			return false, limitErr
		}
		prompt, options, buildErr := r.trapRequest(ctx, run, state, environment)
		if buildErr != nil {
			return false, buildErr
		}
		trap, err = r.store.RaiseTrap(ctx, store.RaiseTrapParams{
			RunID: run.ID, StateID: state.ID, Iteration: run.Iteration, Name: state.Name,
			Kind: state.Trap.Kind, Prompt: prompt, Options: options, Clause: state.Trap.Clause,
		})
		if err != nil {
			return false, err
		}
	}

	if trap.Status == store.TrapOpen {
		run.Status = store.TrapStatusID
		return false, nil
	}

	run.Status = "running"
	switch trap.Resolution {
	case store.TrapApprove:
		environment[state.Name] = map[string]any{"approved": true, "resolution": trap.Resolution, "note": trap.Note}
		run.StateID = state.Next
	case store.TrapReject:
		environment[state.Name] = map[string]any{"approved": false, "resolution": trap.Resolution, "note": trap.Note}
		if err := failVerification(run, environment, state, r.clock.Now()); err != nil {
			return false, err
		}
		return !terminalStatus(run.Status), nil
	case store.TrapCancel:
		return false, fmt.Errorf("trap %s was cancelled", trap.ID)
	default:
		environment[state.Name] = map[string]any{"choice": trap.Resolution, "resolution": trap.Resolution, "note": trap.Note}
		run.StateID = state.Next
	}
	return true, nil
}

func (r *Runner) trapRequest(ctx context.Context, run *store.Run, state ir.State, environment Environment) (string, []string, error) {
	evaluator := r.evaluator(run)
	promptValue, err := evaluator.Evaluate(ctx, state.Trap.Prompt, environment)
	if err != nil {
		return "", nil, fmt.Errorf("evaluate trap prompt: %w", err)
	}
	prompt, ok := promptValue.(string)
	if !ok {
		return "", nil, errors.New("trap prompt must evaluate to a string")
	}
	options := make([]string, 0, len(state.Trap.Options))
	for _, expression := range state.Trap.Options {
		value, err := evaluator.Evaluate(ctx, expression, environment)
		if err != nil {
			return "", nil, fmt.Errorf("evaluate trap option: %w", err)
		}
		options = append(options, formatOption(value))
	}
	return prompt, options, nil
}

// enforceTrapLimit treats supervisor attention as a budgeted resource. A run
// that would ask more questions than it declared fails closed rather than
// quietly filling the review queue.
func (r *Runner) enforceTrapLimit(ctx context.Context, run *store.Run, environment Environment) (bool, error) {
	maximum, ok := limitValue(environment, "traps")
	if !ok {
		return false, nil
	}
	raised, err := r.store.CountTraps(ctx, run.ID)
	if err != nil {
		return false, err
	}
	if raised < maximum {
		return false, nil
	}
	setLimitExceeded(run, "traps", maximum)
	return true, nil
}

func limitValue(environment Environment, wanted string) (int, bool) {
	limits, _ := environment["__limits"].(map[string]any)
	for _, raw := range limits {
		limit, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if kind, _ := limit["kind"].(string); kind != wanted {
			continue
		}
		value, _ := limit["value"].(string)
		maximum, err := strconv.Atoi(strings.ReplaceAll(value, "_", ""))
		if err != nil {
			continue
		}
		return maximum, true
	}
	return 0, false
}

func formatOption(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(payload)
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

func failVerification(run *store.Run, environment Environment, state ir.State, now time.Time) error {
	run.Iteration++
	clearVerifications(run.Machine, environment)
	exceeded, err := enforceLimits(run, environment, now)
	if err != nil || exceeded {
		return err
	}
	run.StateID = state.OnFailure
	if run.StateID == "" {
		run.StateID = run.Machine.InitialState
	}
	return nil
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
		// A gate on a branch that was not taken says nothing about this
		// iteration. Only gates on every path can hold completion back.
		if state.Conditional {
			continue
		}
		switch state.Kind {
		case ir.StateVerify:
			if !truthy(environment[state.Name]) {
				return false
			}
		case ir.StateTrap:
			// A verdict is a structured value, so ordinary truthiness would
			// read a rejection as a pass. The gate is the verdict itself.
			if !trapSatisfied(environment[state.Name]) {
				return false
			}
		}
	}
	return true
}

func trapSatisfied(value any) bool {
	verdict, ok := value.(map[string]any)
	if !ok {
		return false
	}
	if approved, present := verdict["approved"]; present {
		return truthy(approved)
	}
	choice, _ := verdict["choice"].(string)
	return choice != ""
}

// clearVerifications resets the ladder for the next iteration. Trap verdicts are
// cleared with it: once the work is re-derived, an earlier approval described an
// artifact that no longer exists.
func clearVerifications(machine ir.Machine, environment Environment) {
	for _, state := range machine.States {
		if state.Kind == ir.StateVerify || state.Kind == ir.StateTrap {
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

func forkHandles(value any) ([]map[string]any, error) {
	switch typed := value.(type) {
	case map[string]any:
		if runID, _ := typed["run_id"].(string); runID == "" {
			return nil, errors.New("wait target is not a fork handle")
		}
		return []map[string]any{typed}, nil
	case []any:
		if len(typed) == 0 {
			return nil, errors.New("wait requires at least one fork handle")
		}
		result := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			handle, ok := item.(map[string]any)
			if !ok {
				return nil, errors.New("wait list contains a non-fork value")
			}
			if runID, _ := handle["run_id"].(string); runID == "" {
				return nil, errors.New("wait list contains an invalid fork handle")
			}
			result = append(result, handle)
		}
		return result, nil
	default:
		return nil, errors.New("wait requires a fork handle or list of fork handles")
	}
}

func foreachKey(machine ir.Machine, suffix string) string {
	return strings.Join([]string{"__foreach", machine.ID, suffix}, ":")
}

func (r *Runner) runLock(runID string) *sync.Mutex {
	value, _ := r.locks.LoadOrStore(runID, &sync.Mutex{})
	return value.(*sync.Mutex)
}

func (r *Runner) evaluator(run *store.Run) Evaluator {
	return Evaluator{
		WorkingDirectory: run.WorkspaceRoot, Verifier: r.verifier, RunID: run.ID,
		Observed: func(ctx context.Context, observerRunID string) (bool, error) {
			judgement, err := r.store.FindObservedJudgement(ctx, observerRunID)
			if err == sql.ErrNoRows {
				return false, nil
			}
			if err != nil {
				return false, err
			}
			return judgement.Status == store.JudgementRecorded, nil
		},
	}
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
