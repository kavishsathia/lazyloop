package hook

import "encoding/json"

type Decision struct {
	Permission  string `json:"permission"`
	Reason      string `json:"reason,omitempty"`
	Remediation string `json:"remediation,omitempty"`
	Tracked     bool   `json:"tracked"`
	RunID       string `json:"run_id,omitempty"`
}

type codexOutput struct {
	HookSpecificOutput *hookSpecificOutput `json:"hookSpecificOutput,omitempty"`
}

type hookSpecificOutput struct {
	HookEventName            string `json:"hookEventName"`
	PermissionDecision       string `json:"permissionDecision,omitempty"`
	PermissionDecisionReason string `json:"permissionDecisionReason,omitempty"`
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
	}
	return json.Marshal(output)
}
