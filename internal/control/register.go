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
	if command.Name == "kill" {
		return handleKillCommand(ctx, state, event, command.Args)
	}
	if command.Name == "exec" {
		return handleExecCommand(ctx, state, runner, event, command.Args)
	}
	if command.Name == "wait" {
		return handleWaitCommand(ctx, state, event, command.Args)
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
	source, entry, err := resolveSource(command.Args, event.WorkspaceRoot)
	if err != nil {
		return hook.Decision{Permission: "deny", Reason: err.Error(), Remediation: "Correct the Hamster run command and try again."}, nil
	}
	machine, err := compileMachine(source, entry)
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

func handleExecCommand(ctx context.Context, state *store.Store, runner *hamsterruntime.Runner, event hook.Event, arguments []string) (hook.Decision, error) {
	run, err := state.FindCurrentRun(ctx, event.SessionID, event.AgentID)
	if err == sql.ErrNoRows {
		return hook.Decision{Permission: "deny", Reason: "exec requires a bound Hamster run", Remediation: "Start a loop with hamster run before replacing it."}, nil
	}
	if err != nil {
		return hook.Decision{}, err
	}
	source, entry, err := resolveSource(arguments, event.WorkspaceRoot)
	if err != nil {
		return hook.Decision{Permission: "deny", Reason: err.Error(), Remediation: "Correct the Hamster exec command and try again."}, nil
	}
	machine, err := compileMachine(source, entry)
	if err != nil {
		return hook.Decision{Permission: "deny", Reason: err.Error(), Remediation: "Fix the replacement Hamster script diagnostics and exec it again."}, nil
	}
	if _, err := state.ExecRun(ctx, run.ID, machine, nil); err != nil {
		return hook.Decision{}, err
	}
	executed, err := runner.Start(ctx, run.ID)
	if err != nil {
		return hook.Decision{}, err
	}
	return hook.Decision{
		Permission: "allow", Tracked: true, RunID: run.ID,
		Context: fmt.Sprintf("Hamster exec replaced the current machine with %s at state %s; process and run identity were preserved.", machineLabel(machine), executed.StateID),
	}, nil
}

func handleWaitCommand(ctx context.Context, state *store.Store, event hook.Event, arguments []string) (hook.Decision, error) {
	if len(arguments) != 1 {
		return hook.Decision{Permission: "deny", Reason: "usage: hamster wait CHILD_RUN_ID", Remediation: "Pass the child run ID reported when Codex spawned the subagent."}, nil
	}
	parent, err := state.FindCurrentRun(ctx, event.SessionID, event.AgentID)
	if err == sql.ErrNoRows {
		return hook.Decision{Permission: "deny", Reason: "wait requires a bound Hamster run", Remediation: "Start or resume the parent loop first."}, nil
	}
	if err != nil {
		return hook.Decision{}, err
	}
	waited, err := state.WaitRun(ctx, parent.ID, arguments[0])
	if err != nil {
		return hook.Decision{Permission: "deny", Reason: err.Error(), Remediation: "Wait only on a direct child of the current run."}, nil
	}
	contextMessage := fmt.Sprintf("Hamster suspended run %s until child %s terminates.", parent.ID, waited.ChildRunID)
	if waited.Ready {
		contextMessage = fmt.Sprintf("Hamster child %s is already %s with result %s.", waited.ChildRunID, waited.ChildStatus, string(waited.Result))
	}
	return hook.Decision{Permission: "allow", Tracked: true, RunID: parent.ID, Context: contextMessage}, nil
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

func CompileFile(path, entry string) (ir.Machine, error) {
	source, err := os.ReadFile(path)
	if err != nil {
		return ir.Machine{}, err
	}
	return compileMachine(string(source), entry)
}

func CompileSource(source, entry string) (ir.Machine, error) {
	return compileMachine(source, entry)
}

func compileMachine(source, entry string) (ir.Machine, error) {
	program, parseDiagnostics := language.Parse(source)
	if len(parseDiagnostics) > 0 {
		return ir.Machine{}, parseDiagnostics
	}
	compiled, compileDiagnostics := compiler.Compile(program)
	if len(compileDiagnostics) > 0 {
		return ir.Machine{}, compileDiagnostics
	}
	if entry != "" {
		for _, machine := range compiled.Machines {
			if machine.Name == entry {
				return machine, nil
			}
		}
		return ir.Machine{}, fmt.Errorf("loop %q is not defined", entry)
	}
	if len(compiled.Machines) != 1 {
		return ir.Machine{}, fmt.Errorf("script defines %d loops; select one with --entry", len(compiled.Machines))
	}
	return compiled.Machines[0], nil
}

func resolveSource(arguments []string, cwd string) (source, entry string, err error) {
	var script string
	for index := 0; index < len(arguments); index++ {
		switch arguments[index] {
		case "--text":
			index++
			if index >= len(arguments) {
				return "", "", fmt.Errorf("--text requires a loop")
			}
			source = arguments[index]
		case "--entry":
			index++
			if index >= len(arguments) {
				return "", "", fmt.Errorf("--entry requires a loop name")
			}
			entry = arguments[index]
		default:
			if strings.HasPrefix(arguments[index], "-") {
				return "", "", fmt.Errorf("unknown run option %s", arguments[index])
			}
			script = arguments[index]
		}
	}
	if source != "" {
		return source, entry, nil
	}
	if script == "" {
		return "", "", fmt.Errorf("run requires a script or --text")
	}
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
			return string(contents), entry, nil
		}
	}
	return "", "", fmt.Errorf("Hamster script %q was not found", script)
}
