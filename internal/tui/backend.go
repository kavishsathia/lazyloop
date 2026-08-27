package tui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"hamster/internal/daemon"
	"hamster/internal/ir"
	"hamster/internal/store"
)

// Backend is what the console pane talks to. The daemon implements it over
// the control socket; tests implement it in memory.
type Backend interface {
	Observe(ctx context.Context, console string) (*store.ConsoleObservation, error)
	Arm(ctx context.Context, console, workspaceRoot string, machine ir.Machine, source, label string) error
	Disarm(ctx context.Context, console string) error
	Resolve(ctx context.Context, trapID, resolution, note string) error
	Kill(ctx context.Context, codexSessionID, reason string) error
}

// DaemonBackend is the Backend over a live daemon.
type DaemonBackend struct {
	Client daemon.Client
}

func (b DaemonBackend) Observe(ctx context.Context, console string) (*store.ConsoleObservation, error) {
	var view store.ConsoleObservation
	if err := b.Client.Call(ctx, requestID(), "console.observe", map[string]string{"console": console}, &view); err != nil {
		return nil, err
	}
	return &view, nil
}

func (b DaemonBackend) Arm(ctx context.Context, console, workspaceRoot string, machine ir.Machine, source, label string) error {
	return b.Client.Call(ctx, requestID(), "console.arm", map[string]any{
		"console": console, "workspace_root": workspaceRoot, "machine": machine, "source": source, "label": label,
	}, nil)
}

func (b DaemonBackend) Disarm(ctx context.Context, console string) error {
	return b.Client.Call(ctx, requestID(), "console.disarm", map[string]string{"console": console}, nil)
}

func (b DaemonBackend) Resolve(ctx context.Context, trapID, resolution, note string) error {
	return b.Client.Call(ctx, requestID(), "trap.resolve", map[string]string{"trap_id": trapID, "resolution": resolution, "note": note}, nil)
}

func (b DaemonBackend) Kill(ctx context.Context, codexSessionID, reason string) error {
	return b.Client.Call(ctx, requestID(), "run.kill", map[string]string{"session_id": codexSessionID, "reason": reason}, nil)
}

func requestID() string {
	return "console_" + randomHex(8)
}

// NewConsoleToken names one screen for the lifetime of one agent.
func NewConsoleToken() string {
	return "console_" + randomHex(12)
}

func randomHex(bytes int) string {
	buffer := make([]byte, bytes)
	if _, err := rand.Read(buffer); err != nil {
		panic(fmt.Sprintf("random: %v", err))
	}
	return hex.EncodeToString(buffer)
}
