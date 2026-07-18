package hook

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"hamster/internal/compiler"
	"hamster/internal/language"
	"hamster/internal/store"
)

func setupRun(t *testing.T, source string) (*Engine, string) {
	t.Helper()
	program, parseDiagnostics := language.Parse(source)
	if len(parseDiagnostics) > 0 {
		t.Fatal(parseDiagnostics)
	}
	compiled, compileDiagnostics := compiler.Compile(program)
	if len(compileDiagnostics) > 0 {
		t.Fatal(compileDiagnostics)
	}
	state, err := store.Open(filepath.Join(t.TempDir(), "hamster.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { state.Close() })
	run, err := state.CreateRun(context.Background(), store.CreateRunParams{
		WorkspaceRoot: "/repo", CodexSessionID: "session", Machine: compiled.Machines[0],
	})
	if err != nil {
		t.Fatal(err)
	}
	// Runtime-owned setup states are normally consumed by Runner.Start. Hook
	// unit tests position the run directly at its first agent-owned state.
	for _, machineState := range run.Machine.States {
		if machineState.Executor == "agent" {
			expected := run.StateID
			run.StateID = machineState.ID
			if err := state.UpdateRun(context.Background(), expected, run); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	return NewEngine(state), "session"
}

func toolEvent(session, tool, command string) Event {
	input, _ := json.Marshal(map[string]string{"command": command})
	event := Event{SessionID: session, EventName: "PreToolUse", ToolName: tool, ToolUseID: command, ToolInput: input}
	event.Raw = MarshalEvent(event)
	return event
}

func TestFlowAllowsOnlyCurrentExactBashStep(t *testing.T) {
	engine, session := setupRun(t, `goal {
  verify passed = bash.check("go test ./...");
  return passed;
} with flow {
  step tests = bash.run("go test ./...");
}`)
	allowed, err := engine.Decide(context.Background(), toolEvent(session, "Bash", "go test ./..."))
	if err != nil || allowed.Permission != "allow" || !allowed.Tracked {
		t.Fatalf("allowed = %#v, error = %v", allowed, err)
	}
	denied, err := engine.Decide(context.Background(), toolEvent(session, "Bash", "git status"))
	if err != nil || denied.Permission != "deny" || !strings.Contains(denied.Remediation, "go test") {
		t.Fatalf("denied = %#v, error = %v", denied, err)
	}
}

func TestForkStepAllowsOnlyCodexSpawn(t *testing.T) {
	engine, session := setupRun(t, `goal {
  verify done = true;
} with flow {
  step child = fork;
  if child.child { exec child-work; }
  wait child;
}`)
	spawn := Event{SessionID: session, EventName: "PreToolUse", ToolName: "collaborationspawn_agent", ToolUseID: "spawn", ToolInput: json.RawMessage(`{"message":"work","task_name":"child"}`)}
	allowed, err := engine.Decide(context.Background(), spawn)
	if err != nil || allowed.Permission != "allow" || !allowed.Tracked {
		t.Fatalf("spawn decision = %#v, error = %v", allowed, err)
	}
	denied, err := engine.Decide(context.Background(), toolEvent(session, "Bash", "true"))
	if err != nil || denied.Permission != "deny" || !strings.Contains(denied.Remediation, "Spawn") {
		t.Fatalf("non-spawn decision = %#v, error = %v", denied, err)
	}
}

func TestWaitingRunAllowsOnlyNativeCodexWait(t *testing.T) {
	engine, session := setupRun(t, `goal {
  verify done = true;
} with flow {
  step child = fork;
  if child.child { exec child-work; }
  wait child;
}`)
	run, err := engine.store.FindCurrentRun(context.Background(), session, "")
	if err != nil {
		t.Fatal(err)
	}
	expected := run.StateID
	run.Status = "waiting"
	if err := engine.store.UpdateRun(context.Background(), expected, run); err != nil {
		t.Fatal(err)
	}
	wait := Event{SessionID: session, EventName: "PreToolUse", ToolName: "collaborationwait_agent", ToolUseID: "wait", ToolInput: json.RawMessage(`{"timeout_ms":30000}`)}
	allowed, err := engine.Decide(context.Background(), wait)
	if err != nil || allowed.Permission != "allow" || !allowed.Tracked {
		t.Fatalf("wait decision = %#v, error = %v", allowed, err)
	}
	denied, err := engine.Decide(context.Background(), toolEvent(session, "Bash", "true"))
	if err != nil || denied.Permission != "deny" || !strings.Contains(denied.Remediation, "wait_agent") {
		t.Fatalf("non-wait decision = %#v, error = %v", denied, err)
	}
}

func TestGoalPoliciesClassifyBashCapabilities(t *testing.T) {
	engine, session := setupRun(t, `goal {
  allow git;
  disallow docker;
  verify clean = bash.check("true");
  return clean;
}`)
	gitDecision, _ := engine.Decide(context.Background(), toolEvent(session, "Bash", "git status"))
	if gitDecision.Permission != "allow" {
		t.Fatalf("git decision = %#v", gitDecision)
	}
	dockerDecision, _ := engine.Decide(context.Background(), toolEvent(session, "Bash", "docker ps"))
	if dockerDecision.Permission != "deny" {
		t.Fatalf("docker decision = %#v", dockerDecision)
	}
}

func TestGoalCodexPolicyAllowsSubagentSpawn(t *testing.T) {
	engine, session := setupRun(t, `goal {
  allow codex;
  verify done = true;
}`)
	event := Event{SessionID: session, EventName: "PreToolUse", ToolName: "collaborationspawn_agent", ToolUseID: "spawn", ToolInput: json.RawMessage(`{"message":"work","task_name":"child"}`)}
	decision, err := engine.Decide(context.Background(), event)
	if err != nil || decision.Permission != "allow" || !decision.Tracked {
		t.Fatalf("spawn decision = %#v, error = %v", decision, err)
	}
}

func TestUnboundSessionPassesThrough(t *testing.T) {
	engine, _ := setupRun(t, `goal { verify yes = true; return yes; }`)
	decision, err := engine.Decide(context.Background(), toolEvent("other-session", "Bash", "anything"))
	if err != nil || decision.Permission != "allow" || decision.Tracked {
		t.Fatalf("decision = %#v, error = %v", decision, err)
	}
}

func TestCodexDenyOutputUsesDocumentedShape(t *testing.T) {
	payload, err := CodexOutput("PreToolUse", Decision{Permission: "deny", Reason: "blocked", Remediation: "run tests"})
	if err != nil {
		t.Fatal(err)
	}
	var output map[string]any
	if err := json.Unmarshal(payload, &output); err != nil {
		t.Fatal(err)
	}
	specific := output["hookSpecificOutput"].(map[string]any)
	if specific["permissionDecision"] != "deny" || specific["hookEventName"] != "PreToolUse" {
		t.Fatalf("output = %s", payload)
	}
}

func TestCodexAllowOutputCanRewriteToolInput(t *testing.T) {
	updated := json.RawMessage(`{"message":"work\nattach token"}`)
	payload, err := CodexOutput("PreToolUse", Decision{Permission: "allow", Context: "fork prepared", UpdatedInput: updated})
	if err != nil {
		t.Fatal(err)
	}
	var output map[string]any
	if err := json.Unmarshal(payload, &output); err != nil {
		t.Fatal(err)
	}
	specific := output["hookSpecificOutput"].(map[string]any)
	if specific["permissionDecision"] != "allow" || specific["additionalContext"] != "fork prepared" {
		t.Fatalf("output = %s", payload)
	}
	input := specific["updatedInput"].(map[string]any)
	if input["message"] != "work\nattach token" {
		t.Fatalf("updated input = %#v", input)
	}
}

func TestCodexContextOutputDoesNotEmitUnsupportedPermissionDecision(t *testing.T) {
	payload, err := CodexOutput("SubagentStart", Decision{Permission: "allow", Context: "attach first"})
	if err != nil {
		t.Fatal(err)
	}
	var output map[string]any
	if err := json.Unmarshal(payload, &output); err != nil {
		t.Fatal(err)
	}
	specific := output["hookSpecificOutput"].(map[string]any)
	if _, exists := specific["permissionDecision"]; exists {
		t.Fatalf("SubagentStart output contains unsupported permissionDecision: %s", payload)
	}
}
