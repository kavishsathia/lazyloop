package trace

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func TestRedactorKeepsIdentityRelationships(t *testing.T) {
	redactor := NewRedactor()
	first, _ := redactor.Redact(json.RawMessage(`{"session_id":"secret","agent_id":"child","cwd":"/private/repo","prompt":"private"}`))
	second, _ := redactor.Redact(json.RawMessage(`{"session_id":"secret","agent_id":"child","tool_use_id":"call"}`))
	if !bytes.Contains(first, []byte(`"session-1"`)) || !bytes.Contains(second, []byte(`"session-1"`)) {
		t.Fatalf("aliases are unstable: %s %s", first, second)
	}
	if bytes.Contains(first, []byte("private")) || !bytes.Contains(first, []byte("$WORKSPACE")) {
		t.Fatalf("payload was not sanitized: %s", first)
	}
}

func TestImportAndReplayLegacyCapture(t *testing.T) {
	legacy := `{"captured_at":"2026-01-01T00:00:00Z","input":{"session_id":"s","hook_event_name":"PreToolUse","tool_name":"Bash","tool_use_id":"t","tool_input":{"command":"true"}}}
{"captured_at":"2026-01-01T00:00:00.010Z","input":{"session_id":"s","hook_event_name":"PostToolUse","tool_name":"Bash","tool_use_id":"t","tool_response":{}}}`
	fixture, err := ImportLegacy(bytes.NewBufferString(legacy), "s", Manifest{Name: "test", Scenario: "test", CodexDistribution: "codex-cli", CodexVersion: "1.0"})
	if err != nil {
		t.Fatal(err)
	}
	if fixture.Manifest.RecordedAt != (time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("recorded at = %s", fixture.Manifest.RecordedAt)
	}
	report, err := Replay(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if report.Events != 2 || report.PairedCalls != 1 || report.UnmatchedPre != 0 {
		t.Fatalf("report = %#v", report)
	}
}

func TestRedactorRemovesDelegatedPromptsAndLargeOutputs(t *testing.T) {
	redactor := NewRedactor()
	payload, err := redactor.Redact(json.RawMessage(`{"tool_input":{"message":"private task"},"tool_response":[{"text":"large result"}],"token":"secret"}`))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(payload, []byte("private task")) || bytes.Contains(payload, []byte("large result")) || bytes.Contains(payload, []byte(`"secret"`)) {
		t.Fatalf("sensitive payload survived: %s", payload)
	}
}
