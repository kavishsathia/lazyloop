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
	EventKinds    map[string]int `json:"event_kinds"`
	Tools         map[string]int `json:"tools"`
}

func Replay(fixture *Fixture) (ReplayReport, error) {
	report := ReplayReport{Schema: fixture.Manifest.Schema, CodexVersion: fixture.Manifest.CodexVersion, EventKinds: map[string]int{}, Tools: map[string]int{}}
	sessions, agents := map[string]struct{}{}, map[string]struct{}{}
	pending := map[string]int{}
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

func Capabilities(report ReplayReport) []string {
	capabilities := make([]string, 0, len(report.Tools))
	for tool := range report.Tools {
		capabilities = append(capabilities, tool)
	}
	sort.Strings(capabilities)
	return capabilities
}
