package trace

import (
	"encoding/json"
	"time"

	"hamster/internal/hook"
)

const SchemaVersion = "hamster.trace/v1alpha1"

type Manifest struct {
	Schema            string    `json:"schema"`
	Name              string    `json:"name"`
	Scenario          string    `json:"scenario"`
	Source            string    `json:"source"`
	CodexDistribution string    `json:"codex_distribution"`
	CodexVersion      string    `json:"codex_version"`
	RecordedAt        time.Time `json:"recorded_at"`
	EventCount        int       `json:"event_count"`
	HasDecisions      bool      `json:"has_decisions"`
	HasState          bool      `json:"has_state"`
	Sanitized         bool      `json:"sanitized"`
	Capabilities      []string  `json:"capabilities,omitempty"`
}

type Record struct {
	Sequence    int             `json:"sequence"`
	ElapsedMS   int64           `json:"elapsed_ms"`
	Input       json.RawMessage `json:"input"`
	Decision    *hook.Decision  `json:"decision,omitempty"`
	StateBefore *StateSnapshot  `json:"state_before,omitempty"`
	StateAfter  *StateSnapshot  `json:"state_after,omitempty"`
}

type StateSnapshot struct {
	RunID       string `json:"run_id"`
	ProcessID   string `json:"process_id"`
	MachineID   string `json:"machine_id"`
	MachineName string `json:"machine_name,omitempty"`
	MachineKind string `json:"machine_kind"`
	StateID     string `json:"state_id"`
	Status      string `json:"status"`
	Iteration   int    `json:"iteration"`
	StackDepth  int    `json:"stack_depth"`
}

type Fixture struct {
	Manifest Manifest
	Records  []Record
}
