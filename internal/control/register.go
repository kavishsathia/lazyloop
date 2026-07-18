package control

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"hamster/internal/compiler"
	"hamster/internal/hook"
	"hamster/internal/ir"
	"hamster/internal/language"
	hamsterruntime "hamster/internal/runtime"
	"hamster/internal/store"
)

func RegisterFromHook(ctx context.Context, state *store.Store, runner *hamsterruntime.Runner, event hook.Event, command Command) (hook.Decision, error) {
	if existing, err := state.GetEventDecision(ctx, event.RequestID()); err == nil {
		return controlDecision(existing), nil
	} else if err != sql.ErrNoRows {
		return hook.Decision{}, err
	}
	decision, err := registerFromHook(ctx, state, runner, event, command)
	if err != nil {
		return hook.Decision{}, err
	}
	payload := event.Raw
	if len(payload) == 0 {
		payload = hook.MarshalEvent(event)
	}
	recorded, _, err := state.RecordEvent(ctx, event.RequestID(), decision.RunID, event.EventName, payload, store.EventDecision{
		Decision: decision.Permission, Reason: decision.Reason, Remediation: decision.Remediation,
		Context: decision.Context, RunID: decision.RunID,
	})
	if err != nil {
		return hook.Decision{}, err
	}
	return controlDecision(recorded), nil
}

func registerFromHook(ctx context.Context, state *store.Store, runner *hamsterruntime.Runner, event hook.Event, command Command) (hook.Decision, error) {
	if command.Name == "kill" {
		return handleKillCommand(ctx, state, event, command.Args)
	}
	if command.Name == "signal" {
		if len(command.Args) != 1 || command.Args[0] != "goal-achieved" {
			return hook.Decision{Permission: "deny", Reason: "usage: hamster signal goal-achieved", Remediation: "Use the explicit goal-achieved signal after finishing declarative work."}, nil
		}
		return runner.SignalGoalAchieved(ctx, event)
	}
	if command.Name == "exec" {
		return hook.Decision{Permission: "deny", Reason: "language exec is runtime-owned", Remediation: "Declare exec SCRIPT inside the active flow; for administrative recovery, run hamster exec with an explicit --session outside Codex."}, nil
	}
	if command.Name == "wait" {
		return hook.Decision{Permission: "deny", Reason: "language wait is runtime-owned", Remediation: "Declare wait HANDLE inside the active flow; for administrative recovery, run hamster wait with an explicit --session outside Codex."}, nil
	}
	if command.Name == "trace" {
		return handleTraceCommand(ctx, state, event, command.Args)
	}
	if command.Name == "status" {
		run, err := state.FindCurrentRun(ctx, event.SessionID, event.AgentID)
		if err != nil {
			return hook.Decision{Permission: "allow", Context: "No Hamster run is bound to this Codex agent."}, nil
		}
		return hook.Decision{Permission: "allow", Tracked: true, RunID: run.ID, Context: fmt.Sprintf("Hamster run %s is %s at %s (iteration %d, stack depth %d).", run.ID, run.Status, run.StateID, run.Iteration, len(run.Stack))}, nil
	}
	if command.Name != "run" {
		return hook.Decision{Permission: "allow", Context: "Hamster control command accepted."}, nil
	}
	arguments, err := ParseRunArguments(command.Args)
	if err != nil {
		return hook.Decision{Permission: "deny", Reason: err.Error(), Remediation: "Correct the Hamster run command and try again."}, nil
	}
	if arguments.Session != "" {
		return hook.Decision{
			Permission: "allow",
			Context:    "Hamster left this explicitly bound run command to the CLI; the hook did not create a duplicate run.",
		}, nil
	}
	machine, err := ResolveRunMachine(arguments, event.WorkspaceRoot)
	if err != nil {
		return hook.Decision{Permission: "deny", Reason: err.Error(), Remediation: "Fix the Hamster script diagnostics and run it again."}, nil
	}
	run, err := state.CreateRun(ctx, store.CreateRunParams{
		WorkspaceRoot: event.WorkspaceRoot, CodexSessionID: event.SessionID, AgentID: event.AgentID, Machine: machine,
	})
	if err != nil {
		return hook.Decision{}, err
	}
	run, err = runner.Start(ctx, run.ID)
	if err != nil {
		return hook.Decision{}, err
	}
	return hook.Decision{
		Permission: "allow", Tracked: true, RunID: run.ID,
		Context: fmt.Sprintf("Hamster started run %s for %s at state %s. Follow this loop until Hamster reports completion or escalation.", run.ID, machineLabel(machine), run.StateID),
	}, nil
}

func controlDecision(decision store.EventDecision) hook.Decision {
	return hook.Decision{
		Permission: decision.Decision, Reason: decision.Reason, Remediation: decision.Remediation,
		Context: decision.Context, Tracked: decision.RunID != "", RunID: decision.RunID,
	}
}

func handleKillCommand(ctx context.Context, state *store.Store, event hook.Event, arguments []string) (hook.Decision, error) {
	reason := "emergency kill switch"
	for index := 0; index < len(arguments); index++ {
		if arguments[index] != "--reason" || index+1 >= len(arguments) {
			return hook.Decision{Permission: "deny", Reason: "usage: hamster kill [--reason TEXT]", Remediation: "Run the kill command by itself; do not combine it with another shell command."}, nil
		}
		index++
		reason = arguments[index]
	}
	killed, err := state.KillCurrentRun(ctx, event.SessionID, event.AgentID, reason)
	if err == sql.ErrNoRows {
		return hook.Decision{Permission: "allow", Context: "Hamster kill switch is clear; no run is bound to this Codex agent."}, nil
	}
	if err != nil {
		return hook.Decision{}, err
	}
	return hook.Decision{
		Permission: "allow", Tracked: true, RunID: killed.ID,
		Context: fmt.Sprintf("Hamster killed and unbound run %s. Tool calls are no longer governed by that run. Reason: %s", killed.ID, killed.TerminationReason),
	}, nil
}

func handleTraceCommand(ctx context.Context, state *store.Store, event hook.Event, arguments []string) (hook.Decision, error) {
	if len(arguments) == 0 {
		return hook.Decision{Permission: "allow", Context: "Use hamster trace record, stop, export, import, or replay."}, nil
	}
	switch arguments[0] {
	case "record":
		name, distribution, version := "", "", ""
		for index := 1; index < len(arguments); index++ {
			switch arguments[index] {
			case "--name":
				index++
				if index < len(arguments) {
					name = arguments[index]
				}
			case "--codex-version":
				index++
				if index < len(arguments) {
					version = arguments[index]
				}
			case "--codex-distribution":
				index++
				if index < len(arguments) {
					distribution = arguments[index]
				}
			}
		}
		if name == "" {
			return hook.Decision{Permission: "deny", Reason: "trace record requires --name", Remediation: "Choose a stable scenario name."}, nil
		}
		if distribution == "" || version == "" {
			detectedDistribution, detectedVersion, err := detectCodexVersion()
			if err != nil {
				return hook.Decision{Permission: "deny", Reason: err.Error(), Remediation: "Pass --codex-distribution and --codex-version explicitly."}, nil
			}
			if distribution == "" {
				distribution = detectedDistribution
			}
			if version == "" {
				version = detectedVersion
			}
		}
		started, err := state.StartTrace(ctx, name, distribution, version, event.SessionID)
		if err != nil {
			return hook.Decision{}, err
		}
		return hook.Decision{Permission: "allow", Context: fmt.Sprintf("Hamster trace %s is recording session %s as %s %s.", started.ID, event.SessionID, distribution, version)}, nil
	case "stop":
		stopped, err := state.StopTrace(ctx, "")
		if err != nil {
			return hook.Decision{}, err
		}
		return hook.Decision{Permission: "allow", Context: fmt.Sprintf("Hamster trace %s stopped. Export it with hamster trace export --id %s --output <directory>.", stopped.ID, stopped.ID)}, nil
	default:
		return hook.Decision{Permission: "allow", Context: "Hamster trace command accepted."}, nil
	}
}

func detectCodexVersion() (string, string, error) {
	output, err := exec.Command("codex", "--version").Output()
	if err != nil {
		return "", "", fmt.Errorf("detect Codex version: %w", err)
	}
	fields := strings.Fields(string(output))
	if len(fields) < 2 {
		return "", "", fmt.Errorf("unexpected Codex version output %q", strings.TrimSpace(string(output)))
	}
	return fields[0], fields[len(fields)-1], nil
}

func machineLabel(machine ir.Machine) string {
	if machine.Name != "" {
		return machine.Name
	}
	return "anonymous " + string(machine.Kind)
}

func CompileFile(path string) (ir.Machine, error) {
	source, err := os.ReadFile(path)
	if err != nil {
		return ir.Machine{}, err
	}
	return compileMachine(string(source))
}

func CompileSource(source string) (ir.Machine, error) {
	return compileMachine(source)
}

func ResolveScriptMachine(script, cwd string) (ir.Machine, error) {
	source, err := resolveRunSource(RunArguments{Script: script}, cwd)
	if err != nil {
		return ir.Machine{}, err
	}
	return compileMachine(source)
}

func compileMachine(source string) (ir.Machine, error) {
	program, parseDiagnostics := language.Parse(source)
	if len(parseDiagnostics) > 0 {
		return ir.Machine{}, parseDiagnostics
	}
	compiled, compileDiagnostics := compiler.Compile(program)
	if len(compileDiagnostics) > 0 {
		return ir.Machine{}, compileDiagnostics
	}
	if len(compiled.Machines) != 1 {
		return ir.Machine{}, fmt.Errorf("script defines %d root loops; exactly one is required", len(compiled.Machines))
	}
	return compiled.Machines[0], nil
}

func resolveSource(arguments []string, cwd string) (source string, err error) {
	parsed, err := ParseRunArguments(arguments)
	if err != nil {
		return "", err
	}
	source, err = resolveRunSource(parsed, cwd)
	return source, err
}

func resolveRunSource(arguments RunArguments, cwd string) (string, error) {
	if arguments.Text != "" {
		return arguments.Text, nil
	}
	script := arguments.Script
	paths := []string{script}
	if !strings.ContainsRune(script, filepath.Separator) && filepath.Ext(script) == "" {
		paths = []string{filepath.Join(cwd, ".hamster", "scripts", script+".hmstr")}
		if home, homeErr := os.UserHomeDir(); homeErr == nil {
			paths = append(paths, filepath.Join(home, ".hamster", "scripts", script+".hmstr"))
		}
	} else if !filepath.IsAbs(script) {
		paths = []string{filepath.Join(cwd, script)}
	}
	for _, path := range paths {
		contents, readErr := os.ReadFile(path)
		if readErr == nil {
			return string(contents), nil
		}
	}
	return "", fmt.Errorf("Hamster script %q was not found", script)
}
