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
	Arm(ctx context.Context, console, workspaceRoot string, machine ir.Machine, source, label string) (*store.Loop, error)
	Resolve(ctx context.Context, trapID, resolution, note string) error
	StopLoop(ctx context.Context, id string) error
	RemoveLoop(ctx context.Context, id string) error
	ListLoops(ctx context.Context, workspaceRoot string) ([]store.LoopSummary, error)
	LoadLoop(ctx context.Context, id string) (*store.LoopDetail, error)
}

// DaemonBackend is the Backend over a live daemon.
type DaemonBackend struct {
	Client daemon.Client
}

func (b DaemonBackend) Arm(ctx context.Context, console, workspaceRoot string, machine ir.Machine, source, label string) (*store.Loop, error) {
	var loop store.Loop
	if err := b.Client.Call(ctx, requestID(), "console.arm", map[string]any{
		"console": console, "workspace_root": workspaceRoot, "machine": machine, "source": source, "label": label,
	}, &loop); err != nil {
		return nil, err
	}
	return &loop, nil
}

func (b DaemonBackend) Resolve(ctx context.Context, trapID, resolution, note string) error {
	return b.Client.Call(ctx, requestID(), "trap.resolve", map[string]string{"trap_id": trapID, "resolution": resolution, "note": note}, nil)
}

func (b DaemonBackend) StopLoop(ctx context.Context, id string) error {
	return b.Client.Call(ctx, requestID(), "loops.stop", map[string]string{"id": id}, nil)
}

func (b DaemonBackend) RemoveLoop(ctx context.Context, id string) error {
	return b.Client.Call(ctx, requestID(), "loops.remove", map[string]string{"id": id}, nil)
}

func (b DaemonBackend) ListLoops(ctx context.Context, workspaceRoot string) ([]store.LoopSummary, error) {
	var loops []store.LoopSummary
	if err := b.Client.Call(ctx, requestID(), "loops.list", map[string]any{"workspace_root": workspaceRoot}, &loops); err != nil {
		return nil, err
	}
	return loops, nil
}

func (b DaemonBackend) LoadLoop(ctx context.Context, id string) (*store.LoopDetail, error) {
	var detail store.LoopDetail
	if err := b.Client.Call(ctx, requestID(), "loops.get", map[string]string{"id": id}, &detail); err != nil {
		return nil, err
	}
	return &detail, nil
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
