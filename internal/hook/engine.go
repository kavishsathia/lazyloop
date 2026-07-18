package hook

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"hamster/internal/ir"
	"hamster/internal/store"
)

type Engine struct {
	store *store.Store
}

func NewEngine(state *store.Store) *Engine { return &Engine{store: state} }

func (e *Engine) Decide(ctx context.Context, event Event) (Decision, error) {
	if event.EventName != "PreToolUse" {
		return Decision{Permission: "allow"}, nil
	}
	run, err := e.store.FindCurrentRun(ctx, event.SessionID, event.AgentID)
	if err == sql.ErrNoRows {
		return Decision{Permission: "allow"}, nil
	}
	if err != nil {
		return Decision{}, err
	}
	decision := e.authorize(run, event)
	decision.Tracked, decision.RunID = true, run.ID
	payload := event.Raw
	if len(payload) == 0 {
		payload = MarshalEvent(event)
	}
	recorded, _, err := e.store.RecordEvent(ctx, event.RequestID(), run.ID, event.EventName, payload, store.EventDecision{
		Decision: decision.Permission, Reason: decision.Reason, Remediation: decision.Remediation,
	})
	if err != nil {
		return Decision{}, err
	}
	decision.Permission, decision.Reason, decision.Remediation = recorded.Decision, recorded.Reason, recorded.Remediation
	return decision, nil
}

func (e *Engine) authorize(run *store.Run, event Event) Decision {
	if run.Status == "complete" {
		return Decision{Permission: "allow"}
	}
	if run.Status == "waiting" {
		if isWaitTool(event.ToolName) {
			return Decision{Permission: "allow"}
		}
		return deny(fmt.Sprintf("Hamster run %s is waiting for its child runs.", run.ID), "Use Codex's native wait_agent operation; Hamster will advance when every declared child is terminal.")
	}
	if run.Status != "running" {
		return deny(fmt.Sprintf("Hamster run %s terminated with status %s.", run.ID, run.Status), "Inspect the run result and start or resume an appropriate loop.")
	}
	state, ok := findState(run.Machine, run.StateID)
	if !ok {
		return deny("Hamster's current state is invalid.", "Inspect the run and repair or terminate it.")
	}
	switch state.Kind {
	case ir.StateAgentWork:
		return authorizePolicy(run.Machine, event)
	case ir.StateStep:
		expected, detail, ok := expectedTool(state.Expression)
		if !ok {
			return deny("The current step is not an observable tool operation.", "Use a supported provider operation or place the work in a goal.")
		}
		if !strings.EqualFold(expected, event.ToolName) {
			return deny(fmt.Sprintf("The flow is waiting for step %q using %s, not %s.", state.Name, expected, event.ToolName), fmt.Sprintf("Perform step %q first.", state.Name))
		}
		if detail != "" && event.Command() != detail {
			return deny(fmt.Sprintf("The flow is waiting for the exact command declared by step %q.", state.Name), detail)
		}
		return Decision{Permission: "allow"}
	case ir.StateFork:
		if !isSpawnTool(event.ToolName) {
			return deny(fmt.Sprintf("The flow is waiting for fork step %q, not %s.", state.Name, event.ToolName), "Spawn exactly one Codex subagent for this fork.")
		}
		return Decision{Permission: "allow"}
	case ir.StateComplete:
		return Decision{Permission: "allow"}
	default:
		return deny(fmt.Sprintf("Hamster is processing runtime state %q and cannot accept an agent tool call yet.", state.Name), "Wait for Hamster to finish the current runtime transition.")
	}
}

func isSpawnTool(name string) bool {
	return strings.EqualFold(name, "collaborationspawn_agent") || strings.EqualFold(name, "spawn_agent") || strings.EqualFold(name, "Agent")
}

func isWaitTool(name string) bool {
	return strings.EqualFold(name, "collaborationwait_agent") || strings.EqualFold(name, "wait_agent")
}

func expectedTool(expression *ir.Expression) (tool, detail string, ok bool) {
	if expression == nil || expression.Kind != ir.ExpressionCall || expression.Callee == nil {
		return "", "", false
	}
	path := expressionPath(*expression.Callee)
	switch path {
	case "bash.run":
		if len(expression.Arguments) > 0 && expression.Arguments[0].Value.Kind == ir.ExpressionLiteral && expression.Arguments[0].Value.ValueKind == "string" {
			detail = expression.Arguments[0].Value.Value
		}
		return "Bash", detail, true
	default:
		return "", "", false
	}
}

func authorizePolicy(machine ir.Machine, event Event) Decision {
	var allowed []string
	var disallowed []string
	for _, state := range machine.States {
		if state.Kind != ir.StatePolicy || state.Policy == nil {
			continue
		}
		name := expressionPath(state.Policy.Value)
		if state.Policy.Allow {
			allowed = append(allowed, name)
		} else {
			disallowed = append(disallowed, name)
		}
	}
	capability := classifyTool(event)
	if contains(disallowed, capability) {
		return deny(fmt.Sprintf("The active goal disallows %s.", capability), "Use an allowed capability.")
	}
	if len(allowed) > 0 && !contains(allowed, capability) {
		return deny(fmt.Sprintf("The active goal does not allow %s.", capability), "Use one of: "+strings.Join(allowed, ", "))
	}
	return Decision{Permission: "allow"}
}

func classifyTool(event Event) string {
	if strings.EqualFold(event.ToolName, "collaborationspawn_agent") || strings.EqualFold(event.ToolName, "spawn_agent") || strings.EqualFold(event.ToolName, "Agent") {
		return "codex"
	}
	if strings.EqualFold(event.ToolName, "Bash") {
		command := strings.TrimSpace(event.Command())
		for _, capability := range []string{"git", "docker", "codex"} {
			if command == capability || strings.HasPrefix(command, capability+" ") {
				return capability
			}
		}
		return "bash"
	}
	if strings.HasPrefix(event.ToolName, "mcp__") {
		parts := strings.Split(event.ToolName, "__")
		if len(parts) > 1 {
			return parts[1]
		}
	}
	return strings.ToLower(event.ToolName)
}

func expressionPath(expression ir.Expression) string {
	switch expression.Kind {
	case ir.ExpressionIdentifier:
		return expression.Name
	case ir.ExpressionMember:
		if expression.Receiver == nil {
			return expression.Name
		}
		return expressionPath(*expression.Receiver) + "." + expression.Name
	default:
		return ""
	}
}

func findState(machine ir.Machine, stateID string) (ir.State, bool) {
	for _, state := range machine.States {
		if state.ID == stateID {
			return state, true
		}
	}
	return ir.State{}, false
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target || strings.HasPrefix(target, value+".") {
			return true
		}
	}
	return false
}

func deny(reason, remediation string) Decision {
	return Decision{Permission: "deny", Reason: reason, Remediation: remediation}
}

func MarshalEvent(event Event) json.RawMessage {
	payload, _ := json.Marshal(event)
	return payload
}
