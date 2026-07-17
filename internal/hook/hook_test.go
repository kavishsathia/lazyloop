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
	engine, session := setupRun(t, `flow test {
  step tests = bash.run("go test ./...");
  verify passed = bash.check("go test ./...");
  return passed;
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

func TestGoalPoliciesClassifyBashCapabilities(t *testing.T) {
	engine, session := setupRun(t, `goal fix {
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

func TestUnboundSessionPassesThrough(t *testing.T) {
	engine, _ := setupRun(t, `goal okay { verify yes = true; return yes; }`)
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
