package trace

import (
	"encoding/json"
	"fmt"
	"strings"
)

type Redactor struct {
	aliases map[string]map[string]string
	next    map[string]int
}

func NewRedactor() *Redactor {
	return &Redactor{aliases: map[string]map[string]string{}, next: map[string]int{}}
}

func (r *Redactor) Redact(payload json.RawMessage) (json.RawMessage, error) {
	var value any
	if err := json.Unmarshal(payload, &value); err != nil {
		return nil, err
	}
	value = r.redactValue("", value)
	return json.Marshal(value)
}

func (r *Redactor) redactValue(key string, value any) any {
	if key == "tool_response" {
		switch typed := value.(type) {
		case []any:
			return "$REDACTED_OUTPUT"
		case string:
			if len(typed) > 1024 {
				return "$REDACTED_OUTPUT"
			}
		}
	}
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for childKey, child := range typed {
			result[childKey] = r.redactValue(childKey, child)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for index, child := range typed {
			result[index] = r.redactValue(key, child)
		}
		return result
	case string:
		return r.redactString(key, typed)
	default:
		return value
	}
}

func (r *Redactor) redactString(key, value string) string {
	switch key {
	case "session_id":
		return r.alias("session", value)
	case "turn_id":
		return r.alias("turn", value)
	case "agent_id":
		return r.alias("agent", value)
	case "tool_use_id":
		return r.alias("tool", value)
	case "run_id", "parent_run_id", "root_run_id":
		return r.alias("run", value)
	case "process_id", "parent_process_id":
		return r.alias("process", value)
	case "cwd":
		return "$WORKSPACE"
	case "transcript_path", "agent_transcript_path":
		return "$TRANSCRIPT"
	case "prompt", "last_assistant_message", "message":
		return "$REDACTED"
	case "token", "password", "secret", "authorization", "api_key", "access_token", "refresh_token":
		return "$REDACTED_SECRET"
	}
	for _, category := range []string{"session", "turn", "agent", "tool", "run", "process"} {
		for original, alias := range r.aliases[category] {
			if len(original) >= 8 {
				value = strings.ReplaceAll(value, original, alias)
			}
		}
	}
	return value
}

func (r *Redactor) alias(category, original string) string {
	if original == "" {
		return ""
	}
	if r.aliases[category] == nil {
		r.aliases[category] = map[string]string{}
	}
	if alias, exists := r.aliases[category][original]; exists {
		return alias
	}
	r.next[category]++
	alias := fmt.Sprintf("%s-%d", category, r.next[category])
	r.aliases[category][original] = alias
	return alias
}
