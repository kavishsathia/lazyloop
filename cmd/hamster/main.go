package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"hamster/docs"
	"hamster/internal/control"
	"hamster/internal/daemon"
	"hamster/internal/hook"
	"hamster/internal/hookclient"
	"hamster/internal/language"
	"hamster/internal/playground"
	"hamster/internal/project"
	"hamster/internal/reference"
	"hamster/internal/store"
	"hamster/internal/trace"
	"hamster/internal/tui"
	"hamster/internal/version"
)

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "hamster:", err)
		if len(os.Args) > 1 && os.Args[1] == "hook" {
			os.Exit(2)
		}
		os.Exit(1)
	}
}

func run(ctx context.Context, arguments []string) error {
	if len(arguments) == 0 {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		return tui.Run(ctx, tui.Options{Dir: cwd, Agent: agentCommand()})
	}
	switch arguments[0] {
	case "version":
		fmt.Printf("%s %s\n", version.Name, version.Version)
		return nil
	case "init":
		return initProject(arguments[1:])
	case "ui":
		return uiCommand(ctx, arguments[1:])
	case "playground":
		return playgroundCommand(arguments[1:])
	case "man":
		return manCommand(arguments[1:])
	case "daemon":
		return daemonCommand(arguments[1:])
	case "hook":
		return hookCommand(ctx)
	case "run":
		return runCommand(ctx, arguments[1:])
	case "signal":
		return signalCommand(arguments[1:])
	case "exec":
		return execCommand(ctx, arguments[1:])
	case "wait":
		return waitCommand(ctx, arguments[1:])
	case "kill":
		return killCommand(ctx, arguments[1:])
	case "status":
		return statusCommand(ctx, arguments[1:])
	case "taste":
		return tasteCommand(ctx, arguments[1:])
	case "audits":
		return auditsCommand(ctx, arguments[1:])
	case "audit":
		return auditCommand(ctx, arguments[1:])
	case "traps":
		return trapsCommand(ctx, arguments[1:])
	case "resolve":
		return resolveCommand(ctx, arguments[1:])
	case "inspect":
		return inspectCommand(arguments[1:])
	case "trace":
		return traceCommand(ctx, arguments[1:])
	default:
		return usageError()
	}
}

func signalCommand(arguments []string) error {
	if len(arguments) != 1 || arguments[0] != "goal-achieved" {
		return errors.New("usage: hamster signal goal-achieved")
	}
	fmt.Println("Hamster verification probe handled by the Codex hook.")
	return nil
}

func manCommand(arguments []string) error {
	output, err := renderMan(arguments)
	if err != nil {
		return err
	}
	fmt.Print(output)
	return nil
}

func renderMan(arguments []string) (string, error) {
	flags := flag.NewFlagSet("man", flag.ContinueOnError)
	list := flags.Bool("list", false, "list every reference symbol")
	search := flags.String("search", "", "search reference symbols")
	jsonOutput := flags.Bool("json", false, "print a reference entry as JSON")
	full := flags.Bool("full", false, "include semantics, failures, and related symbols")
	if err := flags.Parse(arguments); err != nil {
		return "", err
	}
	catalog, err := reference.Load()
	if err != nil {
		return "", err
	}
	if err := catalog.Validate(language.Keywords()); err != nil {
		return "", err
	}
	if *list {
		var output strings.Builder
		for _, entry := range catalog.List() {
			fmt.Fprintf(&output, "%-24s %-10s %s\n", entry.ID, entry.Kind, entry.Summary)
		}
		return output.String(), nil
	}
	if *search != "" {
		var output strings.Builder
		for _, entry := range catalog.Search(*search) {
			fmt.Fprintf(&output, "%-24s %s\n", entry.ID, entry.Summary)
		}
		if output.Len() == 0 {
			return "", fmt.Errorf("no reference entries match %q", *search)
		}
		return output.String(), nil
	}
	if flags.NArg() != 1 {
		return "", errors.New("usage: hamster man [--full|--json] SYMBOL | --list | --search QUERY")
	}
	symbol := flags.Arg(0)
	if symbol == "grammar" || symbol == "language.grammar" {
		grammar, err := docs.Reference.ReadFile("language/grammar.ebnf")
		if err != nil {
			return "", err
		}
		if !*jsonOutput {
			return string(grammar), nil
		}
	}
	entry, err := catalog.Resolve(symbol)
	if err != nil {
		matches := catalog.Search(symbol)
		if len(matches) > 0 {
			return "", fmt.Errorf("%w; try %q", err, matches[0].ID)
		}
		return "", err
	}
	if *jsonOutput {
		payload, err := json.MarshalIndent(entry, "", "  ")
		if err != nil {
			return "", err
		}
		return string(payload) + "\n", nil
	}
	return reference.Render(*entry, *full), nil
}

func traceCommand(ctx context.Context, arguments []string) error {
	if len(arguments) == 0 {
		return errors.New("usage: hamster trace <record|stop|export|import|replay>")
	}
	switch arguments[0] {
	case "record":
		return traceRecordCommand(ctx, arguments[1:])
	case "stop":
		return traceStopCommand(ctx, arguments[1:])
	case "export":
		return traceExportCommand(ctx, arguments[1:])
	case "import":
		return traceImportCommand(arguments[1:])
	case "replay":
		return traceReplayCommand(arguments[1:])
	default:
		return errors.New("usage: hamster trace <record|stop|export|import|replay>")
	}
}

func traceRecordCommand(ctx context.Context, arguments []string) error {
	flags := flag.NewFlagSet("trace record", flag.ContinueOnError)
	name := flags.String("name", "", "scenario name")
	distribution := flags.String("codex-distribution", "codex-cli", "Codex distribution")
	versionFlag := flags.String("codex-version", "", "exact Codex version")
	session := flags.String("session", "", "Codex session filter for explicit recording")
	homeFlag := flags.String("home", "", "user home directory")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if *session == "" {
		fmt.Println("Hamster trace recording is registered by the Codex pre-tool hook.")
		return nil
	}
	if *name == "" || *versionFlag == "" {
		return errors.New("trace record requires --name and --codex-version")
	}
	client, err := traceDaemonClient(ctx, *homeFlag)
	if err != nil {
		return err
	}
	var started store.Trace
	err = client.Call(ctx, requestID(), "trace.start", map[string]string{
		"name": *name, "codex_distribution": *distribution, "codex_version": *versionFlag, "session_filter": *session,
	}, &started)
	if err != nil {
		return err
	}
	return printJSON(started)
}

func traceStopCommand(ctx context.Context, arguments []string) error {
	flags := flag.NewFlagSet("trace stop", flag.ContinueOnError)
	traceID := flags.String("id", "", "trace ID for explicit stopping")
	homeFlag := flags.String("home", "", "user home directory")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if *traceID == "" {
		fmt.Println("Hamster trace stopping is handled by the Codex pre-tool hook. Outside Codex, pass --id.")
		return nil
	}
	client, err := traceDaemonClient(ctx, *homeFlag)
	if err != nil {
		return err
	}
	var stopped store.Trace
	if err := client.Call(ctx, requestID(), "trace.stop", map[string]string{"trace_id": *traceID}, &stopped); err != nil {
		return err
	}
	return printJSON(stopped)
}

func traceExportCommand(ctx context.Context, arguments []string) error {
	flags := flag.NewFlagSet("trace export", flag.ContinueOnError)
	traceID := flags.String("id", "", "trace ID")
	output := flags.String("output", "testdata/traces", "versioned trace root")
	homeFlag := flags.String("home", "", "user home directory")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if *traceID == "" {
		return errors.New("trace export requires --id")
	}
	client, err := traceDaemonClient(ctx, *homeFlag)
	if err != nil {
		return err
	}
	var data store.TraceData
	if err := client.Call(ctx, requestID(), "trace.get", map[string]string{"trace_id": *traceID}, &data); err != nil {
		return err
	}
	fixture, err := fixtureFromTraceData(data)
	if err != nil {
		return err
	}
	directory := filepath.Join(*output, data.Trace.CodexDistribution+"-"+data.Trace.CodexVersion, data.Trace.Name)
	if err := trace.Write(directory, fixture); err != nil {
		return err
	}
	fmt.Println(directory)
	return nil
}

func traceImportCommand(arguments []string) error {
	flags := flag.NewFlagSet("trace import", flag.ContinueOnError)
	input := flags.String("input", "", "legacy JSONL capture")
	session := flags.String("session", "", "session ID to import")
	name := flags.String("name", "", "fixture name")
	scenario := flags.String("scenario", "", "scenario description")
	distribution := flags.String("codex-distribution", "codex-cli", "Codex distribution")
	versionFlag := flags.String("codex-version", "", "exact Codex version")
	output := flags.String("output", "testdata/traces", "versioned trace root")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if *input == "" || *session == "" || *name == "" || *versionFlag == "" {
		return errors.New("trace import requires --input, --session, --name, and --codex-version")
	}
	file, err := os.Open(*input)
	if err != nil {
		return err
	}
	defer file.Close()
	fixture, err := trace.ImportLegacy(file, *session, trace.Manifest{
		Name: *name, Scenario: *scenario, CodexDistribution: *distribution, CodexVersion: *versionFlag,
	})
	if err != nil {
		return err
	}
	report, err := trace.Replay(fixture)
	if err != nil {
		return err
	}
	fixture.Manifest.Capabilities = trace.Capabilities(report)
	directory := filepath.Join(*output, *distribution+"-"+*versionFlag, *name)
	if err := trace.Write(directory, *fixture); err != nil {
		return err
	}
	fmt.Println(directory)
	return nil
}

func traceReplayCommand(arguments []string) error {
	if len(arguments) != 1 {
		return errors.New("usage: hamster trace replay FIXTURE_DIRECTORY")
	}
	fixture, err := trace.Load(arguments[0])
	if err != nil {
		return err
	}
	report, err := trace.Replay(fixture)
	if err != nil {
		return err
	}
	return printJSON(report)
}

func fixtureFromTraceData(data store.TraceData) (trace.Fixture, error) {
	redactor := trace.NewRedactor()
	fixture := trace.Fixture{Manifest: trace.Manifest{
		Schema: trace.SchemaVersion, Name: data.Trace.Name, Scenario: data.Trace.Name, Source: "live",
		CodexDistribution: data.Trace.CodexDistribution, CodexVersion: data.Trace.CodexVersion,
		RecordedAt: data.Trace.StartedAt, Sanitized: true,
	}}
	for _, row := range data.Events {
		input, err := redactor.Redact(row.Input)
		if err != nil {
			return trace.Fixture{}, err
		}
		record := trace.Record{Sequence: row.Sequence, ElapsedMS: row.ElapsedMS, Input: input}
		if len(row.Decision) > 0 {
			decisionPayload, err := redactor.Redact(row.Decision)
			if err != nil {
				return trace.Fixture{}, err
			}
			var decision hook.Decision
			if err := json.Unmarshal(decisionPayload, &decision); err != nil {
				return trace.Fixture{}, err
			}
			record.Decision = &decision
			fixture.Manifest.HasDecisions = true
		}
		if len(row.StateBefore) > 0 {
			state, err := redactSnapshot(redactor, row.StateBefore)
			if err != nil {
				return trace.Fixture{}, err
			}
			record.StateBefore = state
			fixture.Manifest.HasState = true
		}
		if len(row.StateAfter) > 0 {
			state, err := redactSnapshot(redactor, row.StateAfter)
			if err != nil {
				return trace.Fixture{}, err
			}
			record.StateAfter = state
			fixture.Manifest.HasState = true
		}
		if len(row.Verifications) > 0 {
			verificationPayload, err := redactor.Redact(row.Verifications)
			if err != nil {
				return trace.Fixture{}, err
			}
			if err := json.Unmarshal(verificationPayload, &record.Verifications); err != nil {
				return trace.Fixture{}, err
			}
		}
		fixture.Records = append(fixture.Records, record)
	}
	report, err := trace.Replay(&fixture)
	if err != nil {
		return trace.Fixture{}, err
	}
	fixture.Manifest.Capabilities = trace.Capabilities(report)
	return fixture, nil
}

func redactSnapshot(redactor *trace.Redactor, payload json.RawMessage) (*trace.StateSnapshot, error) {
	sanitized, err := redactor.Redact(payload)
	if err != nil {
		return nil, err
	}
	var snapshot trace.StateSnapshot
	err = json.Unmarshal(sanitized, &snapshot)
	return &snapshot, err
}

func traceDaemonClient(ctx context.Context, homeFlag string) (daemon.Client, error) {
	home, err := resolveHome(homeFlag)
	if err != nil {
		return daemon.Client{}, err
	}
	return daemonClient(ctx, home)
}

func initProject(arguments []string) error {
	flags := flag.NewFlagSet("init", flag.ContinueOnError)
	rootFlag := flags.String("root", "", "repository root")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	root := *rootFlag
	if root == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		root = findRepositoryRoot(cwd)
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return err
	}
	if err := project.Init(root, executable); err != nil {
		return err
	}
	fmt.Printf("Initialized Hamster in %s\nReview and trust the new project hooks with /hooks in Codex.\n", root)
	return nil
}

func daemonCommand(arguments []string) error {
	if len(arguments) == 0 || arguments[0] != "serve" {
		return errors.New("usage: hamster daemon serve [--home PATH] [--http ADDRESS]")
	}
	flags := flag.NewFlagSet("daemon serve", flag.ContinueOnError)
	homeFlag := flags.String("home", "", "user home directory")
	httpFlag := flags.String("http", "", "loopback address for the observability console")
	if err := flags.Parse(arguments[1:]); err != nil {
		return err
	}
	home, err := resolveHome(*homeFlag)
	if err != nil {
		return err
	}
	paths := daemon.DefaultPaths(home)
	if *httpFlag != "" {
		paths.DashboardAddress = *httpFlag
	}
	state, err := store.Open(paths.Database)
	if err != nil {
		return err
	}
	defer state.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return daemon.New(state).ServeWithDashboard(ctx, paths.Socket, paths.DashboardAddress, paths.DashboardFile)
}

// playgroundCommand serves the experiment workbench. It is deliberately a
// separate utility from the observability console: that one watches a session
// you are already running, this one starts sessions so you can see how a loop
// behaves before trusting it with real work.
func playgroundCommand(arguments []string) error {
	flags := flag.NewFlagSet("playground", flag.ContinueOnError)
	address := flags.String("http", "127.0.0.1:7332", "loopback address for the playground")
	homeFlag := flags.String("home", "", "user home directory")
	workRoot := flags.String("workspaces", "", "directory that holds throwaway experiment workspaces")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if err := playground.ValidateAddress(*address); err != nil {
		return err
	}
	home, err := resolveHome(*homeFlag)
	if err != nil {
		return err
	}
	paths := daemon.DefaultPaths(home)
	state, err := store.Open(paths.Database)
	if err != nil {
		return err
	}
	defer state.Close()

	executable, err := os.Executable()
	if err != nil {
		return err
	}
	if executable, err = filepath.Abs(executable); err != nil {
		return err
	}
	root := *workRoot
	if root == "" {
		root = filepath.Join(paths.Root, "playground")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}

	listener, err := net.Listen("tcp", *address)
	if err != nil {
		return err
	}
	defer listener.Close()
	server := &http.Server{
		Handler:           playground.NewHandler(state, playground.NewSupervisor(state, executable, home, root)),
		ReadHeaderTimeout: 5 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() { <-ctx.Done(); _ = server.Close() }()

	fmt.Printf("Hamster playground on http://%s\nWorkspaces: %s\n", listener.Addr(), root)
	if err := server.Serve(listener); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}

func uiCommand(ctx context.Context, arguments []string) error {
	flags := flag.NewFlagSet("ui", flag.ContinueOnError)
	homeFlag := flags.String("home", "", "user home directory")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	home, err := resolveHome(*homeFlag)
	if err != nil {
		return err
	}
	paths := daemon.DefaultPaths(home)
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	if err := daemon.Ensure(ctx, paths, executable, home); err != nil {
		return err
	}
	payload, err := os.ReadFile(paths.DashboardFile)
	if err != nil {
		return fmt.Errorf("dashboard address is unavailable; restart the Hamster daemon with this CLI version: %w", err)
	}
	fmt.Println(strings.TrimSpace(string(payload)))
	return nil
}

func hookCommand(ctx context.Context) error {
	home, err := resolveHome("")
	if err != nil {
		return err
	}
	paths := daemon.DefaultPaths(home)
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	if err := daemon.Ensure(ctx, paths, executable, home); err != nil {
		return err
	}
	return hookclient.Run(ctx, daemon.Client{SocketPath: paths.Socket}, os.Stdin, os.Stdout)
}

func runCommand(ctx context.Context, arguments []string) error {
	invocation, err := control.ParseRunArguments(arguments)
	if err != nil {
		return err
	}
	if invocation.Session == "" {
		fmt.Println("Hamster run registration is handled by the Codex pre-tool hook.")
		return nil
	}
	cwd, _ := os.Getwd()
	machine, err := control.ResolveRunMachine(invocation, cwd)
	if err != nil {
		return err
	}
	home, err := resolveHome(invocation.Home)
	if err != nil {
		return err
	}
	client, err := daemonClient(ctx, home)
	if err != nil {
		return err
	}
	params := map[string]any{"workspace_root": cwd, "codex_session_id": invocation.Session, "agent_id": invocation.Agent, "machine": machine}
	var created store.Run
	if err := client.Call(ctx, requestID(), "run.create", params, &created); err != nil {
		return err
	}
	return printRunSummary(created)
}

func statusCommand(ctx context.Context, arguments []string) error {
	flags := flag.NewFlagSet("status", flag.ContinueOnError)
	session := flags.String("session", "", "Codex session ID")
	agent := flags.String("agent", "", "Codex agent ID")
	runID := flags.String("run", "", "Hamster run ID")
	homeFlag := flags.String("home", "", "user home directory")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if *session == "" && *runID == "" {
		fmt.Println("Hamster status is supplied to Codex by the pre-tool hook. Outside Codex, pass --session or --run.")
		return nil
	}
	home, err := resolveHome(*homeFlag)
	if err != nil {
		return err
	}
	client, err := daemonClient(ctx, home)
	if err != nil {
		return err
	}
	var current store.Run
	if *runID != "" {
		err = client.Call(ctx, requestID(), "run.get", map[string]string{"run_id": *runID}, &current)
	} else {
		err = client.Call(ctx, requestID(), "run.current", map[string]string{"session_id": *session, "agent_id": *agent}, &current)
	}
	if err != nil {
		return err
	}
	return printRunSummary(current)
}

func execCommand(ctx context.Context, arguments []string) error {
	flags := flag.NewFlagSet("exec", flag.ContinueOnError)
	session := flags.String("session", "", "Codex session ID for an explicit exec")
	agent := flags.String("agent", "", "Codex agent ID")
	homeFlag := flags.String("home", "", "user home directory")
	text := flags.String("text", "", "ephemeral replacement loop source")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if *session == "" {
		fmt.Println("Language exec is runtime-owned. For administrative recovery, pass --session explicitly.")
		return nil
	}
	var machine any
	var err error
	if *text != "" {
		machine, err = control.CompileSource(*text)
	} else if flags.NArg() == 1 {
		machine, err = control.CompileFile(flags.Arg(0))
	} else {
		return errors.New("exec requires a script or --text")
	}
	if err != nil {
		return err
	}
	home, err := resolveHome(*homeFlag)
	if err != nil {
		return err
	}
	client, err := daemonClient(ctx, home)
	if err != nil {
		return err
	}
	var current store.Run
	if err := client.Call(ctx, requestID(), "run.current", map[string]string{"session_id": *session, "agent_id": *agent}, &current); err != nil {
		return err
	}
	var executed store.Run
	if err := client.Call(ctx, requestID(), "process.exec", map[string]any{"run_id": current.ID, "machine": machine}, &executed); err != nil {
		return err
	}
	return printRunSummary(executed)
}

func waitCommand(ctx context.Context, arguments []string) error {
	flags := flag.NewFlagSet("wait", flag.ContinueOnError)
	session := flags.String("session", "", "Codex session ID for an explicit wait")
	agent := flags.String("agent", "", "Codex agent ID")
	homeFlag := flags.String("home", "", "user home directory")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if *session == "" {
		fmt.Println("Language wait is runtime-owned. For administrative recovery, pass --session explicitly.")
		return nil
	}
	if flags.NArg() != 1 {
		return errors.New("usage: hamster wait --session SESSION CHILD_RUN_ID")
	}
	home, err := resolveHome(*homeFlag)
	if err != nil {
		return err
	}
	client, err := daemonClient(ctx, home)
	if err != nil {
		return err
	}
	var current store.Run
	if err := client.Call(ctx, requestID(), "run.current", map[string]string{"session_id": *session, "agent_id": *agent}, &current); err != nil {
		return err
	}
	var waited store.WaitResult
	if err := client.Call(ctx, requestID(), "process.wait", map[string]string{"parent_run_id": current.ID, "child_run_id": flags.Arg(0)}, &waited); err != nil {
		return err
	}
	return printJSON(waited)
}

func killCommand(ctx context.Context, arguments []string) error {
	flags := flag.NewFlagSet("kill", flag.ContinueOnError)
	session := flags.String("session", "", "Codex session ID for an explicit kill")
	agent := flags.String("agent", "", "Codex agent ID")
	reason := flags.String("reason", "emergency kill switch", "reason recorded with the killed run")
	homeFlag := flags.String("home", "", "user home directory")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if *session == "" {
		fmt.Println("Hamster kill is handled by the Codex pre-tool hook. Outside Codex, pass --session.")
		return nil
	}
	home, err := resolveHome(*homeFlag)
	if err != nil {
		return err
	}
	client, err := daemonClient(ctx, home)
	if err != nil {
		return err
	}
	var killed store.Run
	if err := client.Call(ctx, requestID(), "run.kill", map[string]string{
		"session_id": *session, "agent_id": *agent, "reason": *reason,
	}, &killed); err != nil {
		return err
	}
	return printRunSummary(killed)
}

func tasteCommand(ctx context.Context, arguments []string) error {
	if len(arguments) == 0 {
		return errors.New("usage: hamster taste <show|record|ratify|revoke>")
	}
	flags := flag.NewFlagSet("taste "+arguments[0], flag.ContinueOnError)
	name := flags.String("name", "", "clause name")
	clause := flags.String("clause", "", "the standard, in the supervisor's words")
	question := flags.String("question", "", "the judgement this clause answers")
	source := flags.String("source", "", "interview, induced, or verdict")
	drafts := flags.Bool("drafts", false, "include unratified proposals")
	jsonOutput := flags.Bool("json", false, "print as JSON")
	homeFlag := flags.String("home", "", "user home directory")
	if err := flags.Parse(arguments[1:]); err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	workspace := findRepositoryRoot(cwd)
	home, err := resolveHome(*homeFlag)
	if err != nil {
		return err
	}
	client, err := daemonClient(ctx, home)
	if err != nil {
		return err
	}

	switch arguments[0] {
	case "record":
		if *name == "" || *clause == "" {
			return errors.New("usage: hamster taste record --name NAME --clause TEXT [--question TEXT]")
		}
		var recorded store.TasteClause
		if err := client.Call(ctx, requestID(), "taste.record", map[string]string{
			"workspace_root": workspace, "name": *name, "clause": *clause,
			"question": *question, "source": *source,
		}, &recorded); err != nil {
			return err
		}
		return printJSON(recorded)
	case "ratify":
		var ratified []store.TasteClause
		if err := client.Call(ctx, requestID(), "taste.ratify", map[string]string{
			"workspace_root": workspace, "name": *name,
		}, &ratified); err != nil {
			return err
		}
		fmt.Printf("Ratified. %d clause(s) now stand as this repository's profile.\n", len(ratified))
		return nil
	case "revoke":
		if *name == "" {
			return errors.New("usage: hamster taste revoke --name NAME")
		}
		var result map[string]any
		if err := client.Call(ctx, requestID(), "taste.revoke", map[string]string{
			"workspace_root": workspace, "name": *name,
		}, &result); err != nil {
			return err
		}
		fmt.Printf("Revoked %s.\n", *name)
		return nil
	case "show", "list":
		var clauses []store.TasteClause
		if err := client.Call(ctx, requestID(), "taste.list", map[string]any{
			"workspace_root": workspace, "ratified_only": !*drafts,
		}, &clauses); err != nil {
			return err
		}
		if *jsonOutput {
			return printJSON(clauses)
		}
		if len(clauses) == 0 {
			fmt.Println("No taste profile yet. An agent proposes clauses with hamster taste record; you adopt them with hamster taste ratify.")
			return nil
		}
		for _, entry := range clauses {
			marker := "ratified"
			if entry.Status == store.TasteDraft {
				marker = "DRAFT — carries no authority until ratified"
			}
			fmt.Printf("%s  [%s, %s]\n", entry.Name, marker, entry.Source)
			if entry.Question != "" {
				fmt.Printf("  answers   %s\n", entry.Question)
			}
			fmt.Printf("  standard  %s\n\n", entry.Clause)
		}
		return nil
	default:
		return errors.New("usage: hamster taste <show|record|ratify|revoke>")
	}
}

// auditsCommand lists sampled passing verdicts. Unlike the trap queue this
// never blocks a run: it questions whether the observer judged correctly, which
// is the only way a clause that has gone hollow becomes visible.
func auditsCommand(ctx context.Context, arguments []string) error {
	flags := flag.NewFlagSet("audits", flag.ContinueOnError)
	all := flags.Bool("all", false, "include audits from every workspace")
	jsonOutput := flags.Bool("json", false, "print as JSON")
	homeFlag := flags.String("home", "", "user home directory")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	workspace, client, err := workspaceClient(ctx, *homeFlag, *all)
	if err != nil {
		return err
	}
	var audits []store.Judgement
	if err := client.Call(ctx, requestID(), "audit.list", map[string]string{"workspace_root": workspace}, &audits); err != nil {
		return err
	}
	if *jsonOutput {
		return printJSON(audits)
	}
	if len(audits) == 0 {
		fmt.Println("No sampled verdicts to audit.")
		return nil
	}
	for _, audit := range audits {
		fmt.Printf("%s  %s\n", audit.ID, audit.Name)
		fmt.Printf("  clause    %s\n", audit.Clause)
		fmt.Printf("  verdict   %s — cited %q\n", audit.Verdict, audit.Citation)
		if audit.Reasoning != "" {
			fmt.Printf("  because   %s\n", audit.Reasoning)
		}
		for _, evidence := range audit.Evidence {
			fmt.Printf("  saw       $ %s\n", evidence.Command)
		}
		fmt.Printf("  resolve   hamster audit %s --agree   (or --flag --note TEXT)\n\n", audit.ID)
	}
	fmt.Printf("%d sampled verdict(s) to spot-check.\n", len(audits))
	return nil
}

func auditCommand(ctx context.Context, arguments []string) error {
	flags := flag.NewFlagSet("audit", flag.ContinueOnError)
	agree := flags.Bool("agree", false, "the observer judged this correctly")
	flagIt := flags.Bool("flag", false, "the observer judged this wrongly")
	note := flags.String("note", "", "what the clause should have said")
	homeFlag := flags.String("home", "", "user home directory")
	judgementID := ""
	if len(arguments) > 0 && !strings.HasPrefix(arguments[0], "-") {
		judgementID, arguments = arguments[0], arguments[1:]
	}
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if judgementID == "" && flags.NArg() == 1 {
		judgementID = flags.Arg(0)
	}
	if judgementID == "" || *agree == *flagIt {
		return errors.New("usage: hamster audit JUDGEMENT_ID (--agree | --flag --note TEXT)")
	}
	outcome := store.AuditAgreed
	if *flagIt {
		outcome = store.AuditFlagged
		if strings.TrimSpace(*note) == "" {
			return errors.New("flagging requires --note; it becomes the proposed refinement to the clause")
		}
	}
	home, err := resolveHome(*homeFlag)
	if err != nil {
		return err
	}
	client, err := daemonClient(ctx, home)
	if err != nil {
		return err
	}
	var resolved store.Judgement
	if err := client.Call(ctx, requestID(), "audit.resolve", map[string]string{
		"judgement_id": judgementID, "outcome": outcome, "note": *note,
	}, &resolved); err != nil {
		return err
	}
	if outcome == store.AuditFlagged {
		fmt.Printf("Flagged. Your note is now a draft refinement of %q — review it with hamster taste show --drafts.\n", resolved.Name)
		return nil
	}
	fmt.Printf("Agreed with the observer on %q.\n", resolved.Name)
	return nil
}

func workspaceClient(ctx context.Context, homeFlag string, all bool) (string, daemon.Client, error) {
	workspace := ""
	if !all {
		cwd, err := os.Getwd()
		if err != nil {
			return "", daemon.Client{}, err
		}
		workspace = findRepositoryRoot(cwd)
	}
	home, err := resolveHome(homeFlag)
	if err != nil {
		return "", daemon.Client{}, err
	}
	client, err := daemonClient(ctx, home)
	return workspace, client, err
}

// trapsCommand prints the supervisor's review queue. It is deliberately a
// human-facing command: the agent may read the queue through the hook, but only
// a person outside Codex can act on it.
func trapsCommand(ctx context.Context, arguments []string) error {
	flags := flag.NewFlagSet("traps", flag.ContinueOnError)
	all := flags.Bool("all", false, "include traps from every workspace")
	jsonOutput := flags.Bool("json", false, "print the queue as JSON")
	homeFlag := flags.String("home", "", "user home directory")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	workspace := ""
	if !*all {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		workspace = findRepositoryRoot(cwd)
	}
	home, err := resolveHome(*homeFlag)
	if err != nil {
		return err
	}
	client, err := daemonClient(ctx, home)
	if err != nil {
		return err
	}
	var traps []store.Trap
	if err := client.Call(ctx, requestID(), "trap.list", map[string]string{"workspace_root": workspace}, &traps); err != nil {
		return err
	}
	if *jsonOutput {
		return printJSON(traps)
	}
	if len(traps) == 0 {
		fmt.Println("No open traps. Nothing is waiting on you.")
		return nil
	}
	for _, trap := range traps {
		fmt.Printf("%s  %s\n", trap.ID, trap.Name)
		fmt.Printf("  run       %s at %s (iteration %d)\n", trap.RunID, trap.StateID, trap.Iteration)
		fmt.Printf("  asked     %s\n", trap.CreatedAt.Local().Format(time.RFC1123))
		fmt.Printf("  question  %s\n", trap.Prompt)
		if trap.Kind == store.TrapSelection {
			fmt.Printf("  options   %s\n", strings.Join(trap.Options, " | "))
			fmt.Printf("  resolve   hamster resolve %s --choose %s\n", trap.ID, trap.Options[0])
		} else {
			fmt.Printf("  resolve   hamster resolve %s --approve   (or --reject)\n", trap.ID)
		}
		fmt.Println()
	}
	fmt.Printf("%d trap(s) waiting.\n", len(traps))
	return nil
}

func resolveCommand(ctx context.Context, arguments []string) error {
	flags := flag.NewFlagSet("resolve", flag.ContinueOnError)
	approve := flags.Bool("approve", false, "approve an approval trap")
	reject := flags.Bool("reject", false, "reject an approval trap and re-derive the work")
	choose := flags.String("choose", "", "answer a selection trap with one declared option")
	note := flags.String("note", "", "reasoning recorded with the verdict")
	homeFlag := flags.String("home", "", "user home directory")
	// The trap ID reads naturally before its verdict, which is also the form
	// hamster traps prints, so accept it on either side of the flags.
	trapID := ""
	if len(arguments) > 0 && !strings.HasPrefix(arguments[0], "-") {
		trapID, arguments = arguments[0], arguments[1:]
	}
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if trapID == "" && flags.NArg() == 1 {
		trapID = flags.Arg(0)
	} else if flags.NArg() != 0 || trapID == "" {
		return errors.New("usage: hamster resolve TRAP_ID (--approve|--reject|--choose OPTION) [--note TEXT]")
	}
	verdicts := 0
	resolution := ""
	if *approve {
		verdicts, resolution = verdicts+1, store.TrapApprove
	}
	if *reject {
		verdicts, resolution = verdicts+1, store.TrapReject
	}
	if *choose != "" {
		verdicts, resolution = verdicts+1, *choose
	}
	if verdicts != 1 {
		return errors.New("resolve requires exactly one of --approve, --reject, or --choose OPTION")
	}
	if *reject && strings.TrimSpace(*note) == "" {
		return errors.New("rejecting a trap requires --note; the agent re-derives the work from your reasoning")
	}
	home, err := resolveHome(*homeFlag)
	if err != nil {
		return err
	}
	client, err := daemonClient(ctx, home)
	if err != nil {
		return err
	}
	var resolved store.Trap
	if err := client.Call(ctx, requestID(), "trap.resolve", map[string]string{
		"trap_id": trapID, "resolution": resolution, "note": *note,
	}, &resolved); err != nil {
		return err
	}
	return printJSON(resolved)
}

func inspectCommand(arguments []string) error {
	flags := flag.NewFlagSet("inspect", flag.ContinueOnError)
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("usage: hamster inspect SCRIPT")
	}
	machine, err := control.CompileFile(flags.Arg(0))
	if err != nil {
		return err
	}
	return printJSON(machine)
}

func daemonClient(ctx context.Context, home string) (daemon.Client, error) {
	paths := daemon.DefaultPaths(home)
	executable, err := os.Executable()
	if err != nil {
		return daemon.Client{}, err
	}
	if err := daemon.Ensure(ctx, paths, executable, home); err != nil {
		return daemon.Client{}, err
	}
	return daemon.Client{SocketPath: paths.Socket}, nil
}

func resolveHome(value string) (string, error) {
	if value != "" {
		return value, nil
	}
	if configured := os.Getenv("HAMSTER_HOME"); configured != "" {
		return configured, nil
	}
	return os.UserHomeDir()
}

func findRepositoryRoot(start string) string {
	current := start
	for {
		if _, err := os.Stat(filepath.Join(current, ".git")); err == nil {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			return start
		}
		current = parent
	}
}

func printJSON(value any) error {
	payload, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(payload))
	return nil
}

func printRunSummary(run store.Run) error {
	workflow := run.Machine.Name
	if len(run.Stack) > 0 {
		workflow = run.Stack[0].Machine.Name
	}
	if workflow == "" {
		workflow = "anonymous " + string(run.Machine.Kind)
	}
	currentMachine := run.Machine.Name
	if currentMachine == "" {
		currentMachine = "anonymous " + string(run.Machine.Kind)
	}
	return printJSON(map[string]any{
		"run_id": run.ID, "status": run.Status, "workflow": workflow, "current_machine": currentMachine,
		"state": run.StateID, "iteration": run.Iteration, "stack_depth": len(run.Stack), "result": run.Result,
		"termination_reason": run.TerminationReason,
	})
}

func requestID() string {
	return fmt.Sprintf("cli-%d", time.Now().UnixNano())
}

// agentCommand is the CLI that runs on the right of the split screen.
// HAMSTER_AGENT overrides it, e.g. HAMSTER_AGENT="claude" or a whole argv.
func agentCommand() []string {
	if override := strings.TrimSpace(os.Getenv("HAMSTER_AGENT")); override != "" {
		return strings.Fields(override)
	}
	return []string{"codex"}
}

func usageError() error {
	return errors.New("usage: hamster <init|ui|playground|man|run|signal|exec|wait|taste|traps|resolve|audits|audit|kill|status|inspect|trace|hook|daemon|version>")
}
