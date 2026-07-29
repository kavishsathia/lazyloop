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
	// Trap resolution is the supervisor's authority, not the agent's. Denying it
	// at the hook is what keeps a trap a real transfer of control instead of a
	// suggestion the running agent can wave away.
	if command.Name == "resolve" {
		return hook.Decision{
			Permission:  "deny",
			Reason:      "trap resolution is reserved for the supervising human",
			Remediation: "A trap answers a question Codex cannot answer for itself. Leave it open; a supervisor resolves it with hamster resolve outside Codex.",
		}, nil
	}
	if command.Name == "traps" {
		traps, err := state.ListOpenTraps(ctx, event.WorkspaceRoot)
		if err != nil {
			return hook.Decision{}, err
		}
		if len(traps) == 0 {
			return hook.Decision{Permission: "allow", Context: "No Hamster trap is open in this workspace."}, nil
		}
		summaries := make([]string, 0, len(traps))
		for _, trap := range traps {
			summaries = append(summaries, fmt.Sprintf("%s (%s): %s", trap.ID, trap.Name, trap.Prompt))
		}
		return hook.Decision{
			Permission: "allow",
			Context:    fmt.Sprintf("%d Hamster trap(s) await the supervisor: %s", len(traps), strings.Join(summaries, "; ")),
		}, nil
	}
	if command.Name == "taste" {
		return handleTasteCommand(ctx, state, event, command.Args)
	}
	// Auditing questions the judge rather than the work, which makes it a
	// supervisor's job by construction: an agent grading its own grader proves
	// nothing.
	if command.Name == "audit" || command.Name == "audits" {
		return hook.Decision{
			Permission:  "deny",
			Reason:      "auditing a verdict is reserved for the supervising human",
			Remediation: "An audit checks whether the observer judged correctly. Run hamster audits outside Codex.",
		}, nil
	}
	if command.Name == "observe" {
		return handleObserveCommand(ctx, state, event)
	}
	if command.Name == "verdict" {
		return handleVerdictCommand(ctx, state, runner, event, command.Args)
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

// DenyDisguisedInvocation refuses a Hamster command that is not the whole tool
// call. Composing one with other shell work hides it from the parser that
// enforces who may run what, so the safe reading of an unparseable invocation is
// to refuse it rather than wave it through as ordinary bash.
func DenyDisguisedInvocation() hook.Decision {
	return hook.Decision{
		Permission: "deny",
		Reason:     "a Hamster command must be the entire tool call",
		Remediation: "Run the Hamster command on its own, with nothing before or after it. " +
			"Commands combined with other shell work bypass the check that decides which of them an agent may run.",
	}
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

// handleObserveCommand serves an observer its own task. Exec cleared the
// child's context on purpose, so the store — not the parent agent — is how it
// learns what it is judging and what it is allowed to look at.
func handleObserveCommand(ctx context.Context, state *store.Store, event hook.Event) (hook.Decision, error) {
	run, err := state.FindCurrentRun(ctx, event.SessionID, event.AgentID)
	if err != nil {
		return hook.Decision{Permission: "allow", Context: "No Hamster run is bound to this agent."}, nil
	}
	judgement, err := state.FindObservedJudgement(ctx, run.ID)
	if err == sql.ErrNoRows {
		return hook.Decision{Permission: "allow", Tracked: true, RunID: run.ID, Context: "This agent is not observing a Hamster judgement."}, nil
	}
	if err != nil {
		return hook.Decision{}, err
	}
	var brief strings.Builder
	fmt.Fprintf(&brief, "You are judging %q against one ratified clause, and nothing else. ", judgement.Name)
	fmt.Fprintf(&brief, "You have no aesthetic authority of your own: apply the clause or abstain.\n")
	if judgement.Question != "" {
		fmt.Fprintf(&brief, "QUESTION: %s\n", judgement.Question)
	}
	fmt.Fprintf(&brief, "CLAUSE: %s\n", judgement.Clause)
	for _, evidence := range judgement.Evidence {
		fmt.Fprintf(&brief, "EVIDENCE $ %s\n%s\n", evidence.Command, evidence.Output)
		if evidence.Error != "" {
			fmt.Fprintf(&brief, "EVIDENCE ERROR: %s\n", evidence.Error)
		}
	}
	fmt.Fprintf(&brief, "Answer once with hamster verdict --pass|--fail --cite \"the part of the clause you applied\" [--because TEXT], ")
	fmt.Fprintf(&brief, "or hamster verdict --abstain --because TEXT when the clause does not cover this. Abstaining is correct whenever you would otherwise be guessing.")
	return hook.Decision{Permission: "allow", Tracked: true, RunID: run.ID, Context: brief.String()}, nil
}

// handleVerdictCommand records the observer's single answer. A verdict without
// a citation is not a verdict; the store coerces it to an abstention so an
// unsupported opinion escalates to a human instead of passing as taste.
func handleVerdictCommand(ctx context.Context, state *store.Store, runner *hamsterruntime.Runner, event hook.Event, arguments []string) (hook.Decision, error) {
	run, err := state.FindCurrentRun(ctx, event.SessionID, event.AgentID)
	if err != nil {
		return hook.Decision{Permission: "deny", Reason: "no Hamster run is bound to this agent", Remediation: "hamster verdict is only available to an observer."}, nil
	}
	options := parseFlags(arguments, "cite", "because")
	verdict := ""
	for _, argument := range arguments {
		switch argument {
		case "--pass":
			verdict = store.VerdictPass
		case "--fail":
			verdict = store.VerdictFail
		case "--abstain":
			verdict = store.VerdictAbstain
		}
	}
	if verdict == "" {
		return hook.Decision{
			Permission: "deny", Reason: "usage: hamster verdict --pass|--fail|--abstain --cite TEXT [--because TEXT]",
			Remediation: "State one verdict and cite the part of the ratified clause you applied.",
		}, nil
	}
	recorded, err := state.RecordVerdict(ctx, run.ID, verdict, options["cite"], options["because"])
	if err != nil {
		return hook.Decision{Permission: "deny", Reason: err.Error(), Remediation: "An observer answers once. Do not revise a recorded verdict."}, nil
	}
	// An observer's whole job is this one control command, so recording the
	// verdict is also what ends it. Otherwise the parent waits on an agent that
	// has already said everything it has to say.
	if _, err := runner.AdvanceAgentWork(ctx, run.ID); err != nil {
		return hook.Decision{}, err
	}
	context := fmt.Sprintf("Hamster recorded verdict %q for %q. Your observation is complete; stop here.", recorded.Verdict, recorded.Name)
	if recorded.Verdict == store.VerdictAbstain && options["cite"] == "" && verdict != store.VerdictAbstain {
		context = fmt.Sprintf("Hamster recorded %q as an abstention because no clause was cited. A supervisor will decide.", recorded.Name)
	}
	return hook.Decision{Permission: "allow", Tracked: true, RunID: run.ID, Context: context}, nil
}

// handleTasteCommand splits authority over the taste profile. The agent runs
// the interview and reports what it heard, so recording and reading are its
// job. Adopting a clause is not: an agent that could ratify its own standard
// would be writing the rubric it is graded against.
func handleTasteCommand(ctx context.Context, state *store.Store, event hook.Event, arguments []string) (hook.Decision, error) {
	if len(arguments) == 0 {
		return hook.Decision{Permission: "allow", Context: "Use hamster taste record or hamster taste show. Ratifying is reserved for the supervisor."}, nil
	}
	switch arguments[0] {
	case "ratify", "revoke":
		return hook.Decision{
			Permission:  "deny",
			Reason:      fmt.Sprintf("taste %s is reserved for the supervising human", arguments[0]),
			Remediation: "Record the clause you heard with hamster taste record. A supervisor adopts it with hamster taste ratify outside Codex; until then it carries no authority.",
		}, nil
	case "record":
		options := parseFlags(arguments[1:], "name", "clause", "question", "source")
		if options["name"] == "" || options["clause"] == "" {
			return hook.Decision{
				Permission: "deny", Reason: "usage: hamster taste record --name NAME --clause TEXT [--question TEXT]",
				Remediation: "Name the judgement and state the standard you heard from the supervisor.",
			}, nil
		}
		clause, err := state.RecordTaste(ctx, store.RecordTasteParams{
			WorkspaceRoot: event.WorkspaceRoot, Name: options["name"], Question: options["question"],
			Clause: options["clause"], Source: options["source"],
		})
		if err != nil {
			return hook.Decision{Permission: "deny", Reason: err.Error(), Remediation: "Ratified taste is immutable; ask the supervisor to revoke it before proposing a replacement."}, nil
		}
		return hook.Decision{
			Permission: "allow",
			Context:    fmt.Sprintf("Hamster recorded taste clause %q as a draft. It carries no authority until a supervisor ratifies it with hamster taste ratify.", clause.Name),
		}, nil
	case "show", "list":
		clauses, err := state.ListTaste(ctx, event.WorkspaceRoot, true)
		if err != nil {
			return hook.Decision{}, err
		}
		if len(clauses) == 0 {
			return hook.Decision{Permission: "allow", Context: "This workspace has no ratified taste profile."}, nil
		}
		summaries := make([]string, 0, len(clauses))
		for _, clause := range clauses {
			summaries = append(summaries, fmt.Sprintf("%s: %s", clause.Name, clause.Clause))
		}
		return hook.Decision{Permission: "allow", Context: "Ratified taste profile — " + strings.Join(summaries, " | ")}, nil
	default:
		return hook.Decision{Permission: "allow", Context: "Hamster taste command accepted."}, nil
	}
}

// parseFlags reads --name VALUE and --name=VALUE pairs from an intercepted
// command line without imposing the CLI's ordering rules on the agent.
func parseFlags(arguments []string, names ...string) map[string]string {
	allowed := map[string]bool{}
	for _, name := range names {
		allowed[name] = true
	}
	options := map[string]string{}
	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		if !strings.HasPrefix(argument, "--") {
			continue
		}
		name, value, inline := strings.Cut(strings.TrimPrefix(argument, "--"), "=")
		if !allowed[name] {
			continue
		}
		if !inline {
			if index+1 >= len(arguments) {
				continue
			}
			index++
			value = arguments[index]
		}
		options[name] = value
	}
	return options
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
