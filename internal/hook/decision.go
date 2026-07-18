package hook

import "encoding/json"

type Decision struct {
	Permission   string          `json:"permission"`
	Reason       string          `json:"reason,omitempty"`
	Remediation  string          `json:"remediation,omitempty"`
	Tracked      bool            `json:"tracked"`
	RunID        string          `json:"run_id,omitempty"`
	Context      string          `json:"context,omitempty"`
	UpdatedInput json.RawMessage `json:"updated_input,omitempty"`
}

type codexOutput struct {
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
