package trace

import (
	"fmt"
	"sort"

	"hamster/internal/hook"
)

type ReplayReport struct {
	Schema        string         `json:"schema"`
	CodexVersion  string         `json:"codex_version"`
	Events        int            `json:"events"`
	Sessions      int            `json:"sessions"`
	Agents        int            `json:"agents"`
	ToolCalls     int            `json:"tool_calls"`
	PairedCalls   int            `json:"paired_calls"`
	UnmatchedPre  int            `json:"unmatched_pre"`
	UnmatchedPost int            `json:"unmatched_post"`
	Decisions     int            `json:"decisions"`
	StateChanges  int            `json:"state_changes"`
	Verifications int            `json:"verifications"`
	EventKinds    map[string]int `json:"event_kinds"`
	Tools         map[string]int `json:"tools"`
}

func Replay(fixture *Fixture) (ReplayReport, error) {
	report := ReplayReport{Schema: fixture.Manifest.Schema, CodexVersion: fixture.Manifest.CodexVersion, EventKinds: map[string]int{}, Tools: map[string]int{}}
	sessions, agents := map[string]struct{}{}, map[string]struct{}{}
	pending := map[string]int{}
	lastState := map[string]StateSnapshot{}
	previousElapsed := int64(-1)
	for index, record := range fixture.Records {
		if record.Sequence != index+1 {
			return report, fmt.Errorf("event %d has sequence %d", index+1, record.Sequence)
		}
		if record.ElapsedMS < previousElapsed {
			return report, fmt.Errorf("event %d moves backwards in time", record.Sequence)
		}
		previousElapsed = record.ElapsedMS
		event, err := hook.ParseEvent(record.Input)
		if err != nil {
			return report, fmt.Errorf("event %d: %w", record.Sequence, err)
		}
		if event.EventName == "" || event.SessionID == "" {
			return report, fmt.Errorf("event %d is missing hook identity", record.Sequence)
		}
		if record.Decision != nil {
			if record.Decision.Permission != "allow" && record.Decision.Permission != "deny" {
				return report, fmt.Errorf("event %d has invalid decision %q", record.Sequence, record.Decision.Permission)
			}
			report.Decisions++
		}
		if record.StateBefore != nil {
			if previous, exists := lastState[record.StateBefore.RunID]; exists && !sameState(previous, *record.StateBefore) {
				return report, fmt.Errorf("event %d state_before does not continue run %s", record.Sequence, record.StateBefore.RunID)
			}
		}
		if record.StateAfter != nil {
			if record.StateBefore != nil && !sameState(*record.StateBefore, *record.StateAfter) {
				report.StateChanges++
			}
			lastState[record.StateAfter.RunID] = *record.StateAfter
		}
		for _, verification := range record.Verifications {
			if verification.Name == "" {
				return report, fmt.Errorf("event %d has an unnamed verification", record.Sequence)
			}
			report.Verifications++
		}
		report.Events++
		report.EventKinds[event.EventName]++
		sessions[event.SessionID] = struct{}{}
		agents[event.IdentityAgent()] = struct{}{}
		if event.ToolName != "" {
			report.Tools[event.ToolName]++
		}
		if event.EventName == "PreToolUse" {
			pending[event.ToolUseID]++
			report.ToolCalls++
		}
		if event.EventName == "PostToolUse" {
			if pending[event.ToolUseID] > 0 {
				pending[event.ToolUseID]--
				report.PairedCalls++
			} else {
				report.UnmatchedPost++
			}
		}
	}
	for _, count := range pending {
		report.UnmatchedPre += count
	}
	report.Sessions, report.Agents = len(sessions), len(agents)
	return report, nil
}

func sameState(left, right StateSnapshot) bool {
	return left.RunID == right.RunID && left.ProcessID == right.ProcessID && left.MachineID == right.MachineID &&
		left.StateID == right.StateID && left.Status == right.Status && left.Iteration == right.Iteration && left.StackDepth == right.StackDepth
}

func Capabilities(report ReplayReport) []string {
	capabilities := make([]string, 0, len(report.Tools))
	for tool := range report.Tools {
		capabilities = append(capabilities, tool)
	}
	sort.Strings(capabilities)
	return capabilities
}
