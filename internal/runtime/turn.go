package runtime

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"hamster/internal/hook"
	"hamster/internal/ir"
	"hamster/internal/store"
)

// Ending the turn is how an interactive session hands control back to a person.
// Hamster has no way to raise a prompt of its own — Codex rejects an "ask"
// permission decision — so a trap gets its answer by letting the agent stop and
// reading the human's next message.
const (
	answerApprove = "approve"
	answerReject  = "reject"
	answerChoose  = "choose"
)

// HandleStop decides whether an agent may end its turn.
func (r *Runner) HandleStop(ctx context.Context, event hook.Event) (hook.Decision, error) {
	run, err := r.store.FindCurrentRun(ctx, event.SessionID, event.AgentID)
	if err == sql.ErrNoRows {
		return hook.Decision{Permission: "allow"}, nil
	}
	if err != nil {
		return hook.Decision{}, err
	}

	if run.Status == store.TrapStatusID {
		trap, trapErr := r.store.FindOpenTrapForRun(ctx, run.ID)
		if trapErr == sql.ErrNoRows {
			return hook.Decision{Permission: "allow", Tracked: true, RunID: run.ID}, nil
		}
		if trapErr != nil {
			return hook.Decision{}, trapErr
		}
		// Stopping is the point. The turn ends, the person is at the prompt, and
		// their reply resolves the trap.
		return hook.Decision{
			Permission: "allow", Tracked: true, RunID: run.ID,
			Context: "Hamster needs a decision from the person running this session. End your turn and ask them, in your own words:\n\n" +
				trap.Prompt + "\n\n" + AnswerInstructions(*trap),
		}, nil
	}

	// Codex sets stop_hook_active when this stop already followed a block, so
	// the nudge happens once rather than trapping the agent in a loop.
	if event.StopHookActive || run.Status != "running" {
		return hook.Decision{Permission: "allow", Tracked: true, RunID: run.ID}, nil
	}
	state, ok := currentState(run)
	if !ok {
		return hook.Decision{Permission: "allow", Tracked: true, RunID: run.ID}, nil
	}
	switch state.Kind {
	case ir.StateAgentWork, ir.StateStep, ir.StateFork:
		// The loop is unfinished and the next move is the agent's, so ending the
		// turn here would abandon work Hamster is still enforcing.
		return hook.Decision{
			Permission: "allow", Tracked: true, RunID: run.ID, Block: true,
			Continuation: fmt.Sprintf("Hamster run %s is still active at %s and its verifiers have not all passed. %s",
				run.ID, state.Name, nextActionAdvice(state)),
		}, nil
	default:
		return hook.Decision{Permission: "allow", Tracked: true, RunID: run.ID}, nil
	}
}

func nextActionAdvice(state ir.State) string {
	switch state.Kind {
	case ir.StateStep:
		return fmt.Sprintf("Perform step %q, or run hamster kill if this loop must be abandoned.", state.Name)
	case ir.StateFork:
		return fmt.Sprintf("Spawn exactly one subagent for fork %q, or run hamster kill if this loop must be abandoned.", state.Name)
	default:
		return "Continue the declarative work, or run hamster kill if this loop must be abandoned."
	}
}

// HandleSubagentStop closes out an observer that ended without answering. A
// silent observer would otherwise hold its parent until the observer's deadline
// expires, and silence is an abstention rather than a verdict.
func (r *Runner) HandleSubagentStop(ctx context.Context, event hook.Event) (hook.Decision, error) {
	run, err := r.store.FindCurrentRun(ctx, event.SessionID, event.AgentID)
	if err == sql.ErrNoRows {
		return hook.Decision{Permission: "allow"}, nil
	}
	if err != nil {
		return hook.Decision{}, err
	}
	judgement, err := r.store.FindObservedJudgement(ctx, run.ID)
	if err == sql.ErrNoRows {
		return r.HandleStop(ctx, event)
	}
	if err != nil {
		return hook.Decision{}, err
	}
	if judgement.Status == store.JudgementRecorded {
		return hook.Decision{Permission: "allow", Tracked: true, RunID: run.ID}, nil
	}
	if _, err := r.store.RecordVerdict(ctx, run.ID, store.VerdictAbstain, "",
		"the observer ended its turn without recording a verdict"); err != nil {
		return hook.Decision{}, err
	}
	if _, err := r.AdvanceAgentWork(ctx, run.ID); err != nil {
		return hook.Decision{}, err
	}
	return hook.Decision{
		Permission: "allow", Tracked: true, RunID: run.ID,
		Context: fmt.Sprintf("Hamster recorded %q as an abstention because the observer ended without a verdict. A supervisor will decide.", judgement.Name),
	}, nil
}

// HandleUserPrompt reads the person's reply as the answer to an open trap.
// Hamster has no model, so it never interprets: the reply must start with the
// exact word the question asked for, and anything else passes through untouched
// with the trap still open.
func (r *Runner) HandleUserPrompt(ctx context.Context, event hook.Event) (hook.Decision, error) {
	run, err := r.store.FindCurrentRun(ctx, event.SessionID, event.AgentID)
	if err == sql.ErrNoRows {
		return hook.Decision{Permission: "allow"}, nil
	}
	if err != nil {
		return hook.Decision{}, err
	}
	if run.Status != store.TrapStatusID {
		return hook.Decision{Permission: "allow", Tracked: true, RunID: run.ID}, nil
	}
	trap, err := r.store.FindOpenTrapForRun(ctx, run.ID)
	if err == sql.ErrNoRows {
		return hook.Decision{Permission: "allow", Tracked: true, RunID: run.ID}, nil
	}
	if err != nil {
		return hook.Decision{}, err
	}

	resolution, note, ok := parseAnswer(*trap, event.Prompt)
	if !ok {
		return hook.Decision{
			Permission: "allow", Tracked: true, RunID: run.ID,
			Context: "Hamster is still waiting on a decision and did not read that as an answer. " +
				"The question was: " + trap.Prompt + "\n" + AnswerInstructions(*trap),
		}, nil
	}
	if _, err := r.store.ResolveTrap(ctx, trap.ID, resolution, note); err != nil {
		return hook.Decision{
			Permission: "allow", Tracked: true, RunID: run.ID,
			Context: "Hamster could not record that answer: " + err.Error(),
		}, nil
	}
	resumed, err := r.Start(ctx, run.ID)
	if err != nil {
		return hook.Decision{}, err
	}
	return hook.Decision{
		Permission: "allow", Tracked: true, RunID: run.ID,
		Context: fmt.Sprintf("Hamster recorded the answer %q for %q and resumed run %s at %s. Continue the loop.",
			resolution, trap.Name, run.ID, resumed.StateID),
	}, nil
}

// AnswerInstructions states exactly what the person must type. Hamster matches
// the reply mechanically, so the wording it asks for is the wording it accepts.
func AnswerInstructions(trap store.Trap) string {
	if trap.Kind == store.TrapSelection {
		return "They answer by replying with: choose " + strings.Join(trap.Options, " | choose ")
	}
	return "They answer by replying with: approve   (or: reject <what is wrong>)"
}

func parseAnswer(trap store.Trap, reply string) (resolution, note string, ok bool) {
	fields := strings.Fields(strings.TrimSpace(reply))
	if len(fields) == 0 {
		return "", "", false
	}
	head := strings.ToLower(strings.Trim(fields[0], ":,."))
	rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(reply), fields[0]))
	rest = strings.TrimSpace(strings.TrimLeft(rest, ":,-"))

	switch trap.Kind {
	case store.TrapSelection:
		if head != answerChoose || len(fields) < 2 {
			return "", "", false
		}
		choice := strings.TrimSpace(rest)
		for _, option := range trap.Options {
			if strings.EqualFold(option, choice) {
				return option, "", true
			}
		}
		return "", "", false
	default:
		switch head {
		case answerApprove:
			return store.TrapApprove, rest, true
		case answerReject:
			// A rejection re-derives the work, so the agent needs to be told what
			// was wrong; a bare "reject" is not an answer it can act on.
			if rest == "" {
				return "", "", false
			}
			return store.TrapReject, rest, true
		}
	}
	return "", "", false
}
