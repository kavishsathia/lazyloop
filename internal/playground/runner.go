package playground

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"

	"hamster/internal/control"
	"hamster/internal/store"
)

func contextTODO() context.Context { return context.Background() }

// Supervisor owns the Codex processes an experiment starts. It keeps them off
// the daemon's request path so a long agent run never blocks the page.
type Supervisor struct {
	store    *store.Store
	binary   string
	home     string
	root     string
	mu       sync.Mutex
	cancels  map[string]context.CancelFunc
	CodexBin string
}

func NewSupervisor(state *store.Store, hamsterBinary, home, root string) *Supervisor {
	return &Supervisor{
		store: state, binary: hamsterBinary, home: home, root: root,
		cancels: map[string]context.CancelFunc{}, CodexBin: "codex",
	}
}

type LaunchParams struct {
	Name   string                  `json:"name"`
	Script string                  `json:"script"`
	Prompt string                  `json:"prompt"`
	Taste  []store.PlaygroundTaste `json:"taste"`
}

// Launch validates the loop, builds its workspace, and starts Codex against it.
// Compilation happens first: an experiment that could never run is a mistake to
// report immediately, not one to discover from agent transcript noise.
func (s *Supervisor) Launch(ctx context.Context, params LaunchParams) (*store.PlaygroundRun, error) {
	if _, err := control.CompileSource(params.Script); err != nil {
		return nil, fmt.Errorf("the loop does not compile: %w", err)
	}
	workspace, err := os.MkdirTemp(s.root, "run-")
	if err != nil {
		return nil, fmt.Errorf("create workspace: %w", err)
	}
	// macOS hands out /var symlinks; the hooks report the resolved path, and the
	// workspace root is what ties a Codex session back to its experiment.
	if resolved, err := filepath.EvalSymlinks(workspace); err == nil {
		workspace = resolved
	}
	if err := prepareWorkspace(workspace, s.binary, params.Script); err != nil {
		return nil, err
	}
	if err := seedTaste(s.store, workspace, params.Taste); err != nil {
		return nil, err
	}
	run, err := s.store.CreatePlaygroundRun(ctx, store.CreatePlaygroundParams{
		Name: params.Name, Script: params.Script, Prompt: params.Prompt,
		Taste: params.Taste, WorkspaceRoot: workspace,
	})
	if err != nil {
		return nil, err
	}
	go s.execute(run.ID, workspace, params.Prompt)
	return run, nil
}

func (s *Supervisor) execute(runID, workspace, prompt string) {
	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.cancels[runID] = cancel
	s.mu.Unlock()
	defer func() {
		cancel()
		s.mu.Lock()
		delete(s.cancels, runID)
		s.mu.Unlock()
	}()

	command := exec.CommandContext(ctx, s.CodexBin, "exec",
		"--json",
		"--dangerously-bypass-hook-trust",
		"--skip-git-repo-check",
		"-C", workspace,
		StartPrompt(prompt),
	)
	command.Dir = workspace
	command.Env = append(os.Environ(), "HAMSTER_HOME="+s.home)

	stdout, err := command.StdoutPipe()
	if err != nil {
		s.fail(runID, err)
		return
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		s.fail(runID, err)
		return
	}
	if err := command.Start(); err != nil {
		s.fail(runID, fmt.Errorf("start codex: %w", err))
		return
	}
	_ = s.store.SetPlaygroundStatus(ctx, runID, store.PlaygroundRunning, "", nil)

	var waiting sync.WaitGroup
	waiting.Add(2)
	go func() {
		defer waiting.Done()
		s.drain(runID, "codex", stdout)
	}()
	go func() {
		defer waiting.Done()
		s.drain(runID, "stderr", stderr)
	}()
	waiting.Wait()

	message, status := "", store.PlaygroundComplete
	exitCode := 0
	if err := command.Wait(); err != nil {
		status, message = store.PlaygroundFailed, err.Error()
		var exitError *exec.ExitError
		if ok := asExitError(err, &exitError); ok {
			exitCode = exitError.ExitCode()
		} else {
			exitCode = -1
		}
	}
	if ctx.Err() != nil {
		status, message = store.PlaygroundCancelled, "cancelled"
	}
	_ = s.store.SetPlaygroundStatus(context.Background(), runID, status, message, &exitCode)
}

// drain records the agent's own stream. Lines that are not JSON are kept as
// text rather than dropped, because Codex reports failures that way and a
// silent experiment is worse than a noisy one.
func (s *Supervisor) drain(runID, stream string, reader interface{ Read([]byte) (int, error) }) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		payload := json.RawMessage(append([]byte(nil), line...))
		if !json.Valid(payload) {
			payload, _ = json.Marshal(string(line))
		}
		_ = s.store.AppendPlaygroundEvent(context.Background(), runID, stream, payload)
	}
}

func (s *Supervisor) Cancel(runID string) bool {
	s.mu.Lock()
	cancel, ok := s.cancels[runID]
	s.mu.Unlock()
	if ok {
		cancel()
	}
	return ok
}

func (s *Supervisor) fail(runID string, err error) {
	code := -1
	_ = s.store.SetPlaygroundStatus(context.Background(), runID, store.PlaygroundFailed, err.Error(), &code)
}

func asExitError(err error, target **exec.ExitError) bool {
	if exitError, ok := err.(*exec.ExitError); ok {
		*target = exitError
		return true
	}
	return false
}
