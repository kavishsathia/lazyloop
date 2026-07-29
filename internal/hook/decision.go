package hook

import "encoding/json"

type Decision struct {
	Permission string `json:"permission"`
	// Block ends a Stop hook by refusing to let the turn finish. Continuation is
	// the prompt Codex hands back to the agent.
	Block        bool            `json:"block,omitempty"`
	Continuation string          `json:"continuation,omitempty"`
	Reason       string          `json:"reason,omitempty"`
	Remediation  string          `json:"remediation,omitempty"`
	Tracked      bool            `json:"tracked"`
	RunID        string          `json:"run_id,omitempty"`
	Context      string          `json:"context,omitempty"`
	UpdatedInput json.RawMessage `json:"updated_input,omitempty"`
}

type codexOutput struct {
	Decision           string              `json:"decision,omitempty"`
	Reason             string              `json:"reason,omitempty"`
	HookSpecificOutput *hookSpecificOutput `json:"hookSpecificOutput,omitempty"`
}

type hookSpecificOutput struct {
	HookEventName            string          `json:"hookEventName"`
	PermissionDecision       string          `json:"permissionDecision,omitempty"`
	PermissionDecisionReason string          `json:"permissionDecisionReason,omitempty"`
	AdditionalContext        string          `json:"additionalContext,omitempty"`
	UpdatedInput             json.RawMessage `json:"updatedInput,omitempty"`
}

func CodexOutput(eventName string, decision Decision) ([]byte, error) {
	output := codexOutput{}
	// A Stop hook speaks a different dialect: it blocks the turn from ending and
	// supplies the prompt the agent resumes from.
	if eventName == "Stop" || eventName == "SubagentStop" {
		if decision.Block && decision.Continuation != "" {
			output.Decision, output.Reason = "block", decision.Continuation
		} else if decision.Context != "" {
			output.HookSpecificOutput = &hookSpecificOutput{HookEventName: eventName, AdditionalContext: decision.Context}
		}
		return json.Marshal(output)
	}
	if decision.Permission == "deny" {
		reason := decision.Reason
		if decision.Remediation != "" {
			reason += " Remediation: " + decision.Remediation
		}
		output.HookSpecificOutput = &hookSpecificOutput{
			HookEventName: eventName, PermissionDecision: "deny", PermissionDecisionReason: reason,
		}
	} else if decision.Context != "" || len(decision.UpdatedInput) > 0 {
		specific := &hookSpecificOutput{HookEventName: eventName, AdditionalContext: decision.Context, UpdatedInput: decision.UpdatedInput}
		if len(decision.UpdatedInput) > 0 {
			specific.PermissionDecision = "allow"
		}
		output.HookSpecificOutput = specific
	}
	return json.Marshal(output)
}
