package runtime

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"time"
)

type Clock interface {
	Now() time.Time
}

type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now() }

type FixedClock struct {
	mu  sync.RWMutex
	now time.Time
}

func NewFixedClock(now time.Time) *FixedClock { return &FixedClock{now: now} }

func (c *FixedClock) Now() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.now
}

func (c *FixedClock) Advance(duration time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(duration)
}

type Verifier interface {
	Check(ctx context.Context, workingDirectory, command string) (bool, error)
}

type CommandVerifier struct{}

func (CommandVerifier) Check(ctx context.Context, workingDirectory, command string) (bool, error) {
	process := exec.CommandContext(ctx, "sh", "-c", command)
	process.Dir = workingDirectory
	err := process.Run()
	if err == nil {
		return true, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return false, nil
	}
	return false, fmt.Errorf("execute verifier: %w", err)
}

// Collector captures the evidence an observer is shown. It is separate from
// Verifier because a verifier answers yes or no while evidence is the material
// a judgement is made from.
type Collector interface {
	Collect(ctx context.Context, workingDirectory, command string) (string, error)
}

const maxEvidenceBytes = 64 << 10

type CommandCollector struct{}

func (CommandCollector) Collect(ctx context.Context, workingDirectory, command string) (string, error) {
	process := exec.CommandContext(ctx, "sh", "-c", command)
	process.Dir = workingDirectory
	output, err := process.CombinedOutput()
	if len(output) > maxEvidenceBytes {
		output = append(output[:maxEvidenceBytes], []byte("\n[evidence truncated]")...)
	}
	var exitError *exec.ExitError
	if err != nil && !errors.As(err, &exitError) {
		return string(output), fmt.Errorf("collect evidence: %w", err)
	}
	return string(output), nil
}

// ReplayCollector makes judgement tests deterministic without running commands.
type ReplayCollector struct {
	mu      sync.Mutex
	Outputs map[string]string
}

func (c *ReplayCollector) Collect(_ context.Context, _ string, command string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if output, ok := c.Outputs[command]; ok {
		return output, nil
	}
	return "", nil
}

type VerificationResult struct {
	Passed bool   `json:"passed"`
	Error  string `json:"error,omitempty"`
}

type VerificationCall struct {
	WorkingDirectory string `json:"working_directory"`
	Command          string `json:"command"`
}

type VerificationObservation struct {
	Name    string `json:"name"`
	Command string `json:"command,omitempty"`
	Passed  bool   `json:"passed"`
	Error   string `json:"error,omitempty"`
}

type ReplayVerifier struct {
	mu      sync.Mutex
	results []VerificationResult
	calls   []VerificationCall
}

func NewReplayVerifier(results []VerificationResult) *ReplayVerifier {
	return &ReplayVerifier{results: append([]VerificationResult(nil), results...)}
}

func (v *ReplayVerifier) Check(_ context.Context, workingDirectory, command string) (bool, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.calls = append(v.calls, VerificationCall{WorkingDirectory: workingDirectory, Command: command})
	if len(v.results) == 0 {
		return false, errors.New("trace has no verifier result for " + command)
	}
	result := v.results[0]
	v.results = v.results[1:]
	if result.Error != "" {
		return false, errors.New(result.Error)
	}
	return result.Passed, nil
}

func (v *ReplayVerifier) Calls() []VerificationCall {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]VerificationCall(nil), v.calls...)
}
