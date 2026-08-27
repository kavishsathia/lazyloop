package hook

import (
	"encoding/json"
	"strings"
)

type Event struct {
	SessionID    string          `json:"session_id"`
	TurnID       string          `json:"turn_id,omitempty"`
	AgentID      string          `json:"agent_id,omitempty"`
	AgentType    string          `json:"agent_type,omitempty"`
	EventName    string          `json:"hook_event_name"`
	ToolName     string          `json:"tool_name,omitempty"`
	ToolUseID    string          `json:"tool_use_id,omitempty"`
	ToolInput    json.RawMessage `json:"tool_input,omitempty"`
	ToolResponse json.RawMessage `json:"tool_response,omitempty"`
	Prompt       string          `json:"prompt,omitempty"`
	LastMessage  string          `json:"last_assistant_message,omitempty"`
	// StopHookActive is Codex's own re-entry guard: it is true when this stop is
	// already the result of a blocked stop, so a continuation nudge is given at
	// most once rather than looping forever.
	StopHookActive bool            `json:"stop_hook_active,omitempty"`
	WorkspaceRoot  string          `json:"cwd,omitempty"`
	Raw            json.RawMessage `json:"raw,omitempty"`
}

type HookRequest struct {
	Event Event `json:"event"`
	// Console is the lazyloop screen the agent was started from, read from
	// the agent's environment by the hook process it spawned. Empty outside
	// lazyloop.
	Console string `json:"console,omitempty"`
}

// ConsoleEnvironment is the variable lazyloop sets on the agent it spawns.
// Hook processes inherit it, which is the only path by which a console token
// reaches the daemon alongside a Codex session identity.
const ConsoleEnvironment = "HAMSTER_CONSOLE"

func ParseEvent(payload []byte) (Event, error) {
	var event Event
	if err := json.Unmarshal(payload, &event); err != nil {
		return Event{}, err
	}
	event.Raw = append(json.RawMessage(nil), payload...)
	return event, nil
}

func (e Event) IdentityAgent() string {
	if e.AgentID == "" {
		return "ROOT"
	}
	return e.AgentID
}

func (e Event) RequestID() string {
	parts := []string{e.SessionID, e.EventName, e.ToolUseID}
	if e.ToolUseID == "" {
		parts = append(parts, e.TurnID, e.AgentID)
	}
	return strings.Join(parts, ":")
}

func (e Event) Command() string {
	var input struct {
		Command string `json:"command"`
	}
	_ = json.Unmarshal(e.ToolInput, &input)
	return input.Command
}
