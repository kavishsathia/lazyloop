package control

import (
	"context"
	"fmt"
	"os"
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
