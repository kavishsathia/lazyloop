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

type VerificationResult struct {
	Passed bool   `json:"passed"`
	Error  string `json:"error,omitempty"`
}

type VerificationCall struct {
	WorkingDirectory string `json:"working_directory"`
	Command          string `json:"command"`
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
