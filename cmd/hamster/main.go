package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"hamster/internal/control"
	"hamster/internal/daemon"
	"hamster/internal/hookclient"
	"hamster/internal/project"
	"hamster/internal/store"
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
		return usageError()
	}
	switch arguments[0] {
	case "version":
		fmt.Printf("%s %s\n", version.Name, version.Version)
		return nil
	case "init":
		return initProject(arguments[1:])
	case "daemon":
		return daemonCommand(arguments[1:])
	case "hook":
		return hookCommand(ctx)
	case "run":
		return runCommand(ctx, arguments[1:])
	case "status":
		return statusCommand(ctx, arguments[1:])
	case "inspect":
		return inspectCommand(arguments[1:])
	default:
		return usageError()
	}
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
		return errors.New("usage: hamster daemon serve [--home PATH]")
	}
	flags := flag.NewFlagSet("daemon serve", flag.ContinueOnError)
	homeFlag := flags.String("home", "", "user home directory")
	if err := flags.Parse(arguments[1:]); err != nil {
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
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return daemon.New(state).Serve(ctx, paths.Socket)
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
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	session := flags.String("session", "", "Codex session ID for an explicit run")
	agent := flags.String("agent", "", "Codex agent ID")
	homeFlag := flags.String("home", "", "user home directory")
	entry := flags.String("entry", "", "named loop to execute")
	text := flags.String("text", "", "ephemeral loop source")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if *session == "" {
		fmt.Println("Hamster run registration is handled by the Codex pre-tool hook.")
		return nil
	}
	var machinePath string
	if flags.NArg() > 0 {
		machinePath = flags.Arg(0)
	}
	var machine any
	var err error
	if *text != "" {
		machine, err = control.CompileSource(*text, *entry)
	} else if machinePath != "" {
		machine, err = control.CompileFile(machinePath, *entry)
	} else {
		return errors.New("run requires a script or --text")
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
	cwd, _ := os.Getwd()
	params := map[string]any{"workspace_root": cwd, "codex_session_id": *session, "agent_id": *agent, "machine": machine}
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

func inspectCommand(arguments []string) error {
	flags := flag.NewFlagSet("inspect", flag.ContinueOnError)
	entry := flags.String("entry", "", "named loop")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("usage: hamster inspect [--entry NAME] SCRIPT")
	}
	machine, err := control.CompileFile(flags.Arg(0), *entry)
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
	machine := run.Machine.Name
	if machine == "" {
		machine = "anonymous " + string(run.Machine.Kind)
	}
	return printJSON(map[string]any{
		"run_id": run.ID, "status": run.Status, "machine": machine,
		"state": run.StateID, "iteration": run.Iteration, "stack_depth": len(run.Stack), "result": run.Result,
	})
}

func requestID() string {
	return fmt.Sprintf("cli-%d", time.Now().UnixNano())
}

func usageError() error {
	return errors.New("usage: hamster <init|run|status|inspect|hook|daemon|version>")
}
