package tui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"hamster/internal/ir"
	"hamster/internal/store"
)

// fakeBackend records what the console asked for and serves a fixed view.
type fakeBackend struct {
	mu       sync.Mutex
	view     store.ConsoleObservation
	armed    []string
	resolved []string
	killed   []string
	disarmed int
	fail     error
	loops    []store.LoopSummary
	details  map[string]*store.LoopDetail
}

func (f *fakeBackend) Observe(context.Context, string) (*store.ConsoleObservation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	view := f.view
	return &view, nil
}

func (f *fakeBackend) Arm(_ context.Context, _, _ string, machine ir.Machine, source, label string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return f.fail
	}
	f.armed = append(f.armed, source)
	f.view.Armed = &store.Loop{Machine: machine, Source: source, Label: label}
	id := "loop-" + label
	f.loops = append([]store.LoopSummary{{ID: id, Label: label, Status: "armed"}}, f.loops...)
	if f.details == nil {
		f.details = map[string]*store.LoopDetail{}
	}
	f.details[id] = &store.LoopDetail{Loop: store.Loop{ID: id, Label: label, Source: source}, Status: "armed"}
	return nil
}

func (f *fakeBackend) Disarm(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.disarmed++
	f.view.Armed = nil
	return nil
}

func (f *fakeBackend) Resolve(_ context.Context, trapID, resolution, note string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resolved = append(f.resolved, trapID+":"+resolution+":"+note)
	f.view.Traps = nil
	return nil
}

func (f *fakeBackend) Kill(_ context.Context, session, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.killed = append(f.killed, session)
	f.view.Run = nil
	return nil
}

func (f *fakeBackend) ListLoops(context.Context, string) ([]store.LoopSummary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]store.LoopSummary(nil), f.loops...), nil
}

func (f *fakeBackend) LoadLoop(_ context.Context, id string) (*store.LoopDetail, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if d, ok := f.details[id]; ok {
		return d, nil
	}
	return &store.LoopDetail{Loop: store.Loop{ID: id}}, nil
}

func (f *fakeBackend) settle(t *testing.T, c *console, want func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		ok := want()
		c.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("console did not settle")
}

func testConsole(backend Backend) *console {
	return newConsole(backend, "console", "/repo", Hooks{Installed: true}, func() {})
}

func TestFlippingAndPanelCycling(t *testing.T) {
	c := testConsole(&fakeBackend{})
	// The dashboard is home: keys act on it and nothing is forwarded.
	if got := string(c.route([]byte{'n'})); got != "" {
		t.Fatalf("dashboard forwarded %q", got)
	}
	if c.screen != screenDashboard || c.mode != modeLoop {
		t.Fatalf("after n: screen %v mode %v", c.screen, c.mode)
	}
	c.route([]byte("go test ./..."))
	if string(c.line) != "go test ./..." {
		t.Fatalf("line = %q", string(c.line))
	}
	c.route([]byte{0x1b}) // escape cancels the editor
	if c.mode != modeKeys || c.line != nil {
		t.Fatalf("escape did not cancel: mode %v line %q", c.mode, string(c.line))
	}
	c.route([]byte{0x1b}) // escape again flips to the agent
	if c.screen != screenAgent {
		t.Fatal("escape did not flip to the agent")
	}
	if got := string(c.route([]byte("hello"))); got != "hello" {
		t.Fatalf("agent screen forwarded %q", got)
	}
	// ctrl-] always brings lazyloop back; on the dashboard it flips to codex.
	if got := string(c.route([]byte{prefixKey})); got != "" {
		t.Fatalf("prefix forwarded %q", got)
	}
	if c.screen != screenDashboard {
		t.Fatal("prefix did not return to the dashboard")
	}
	// Tab cycles the active panel through the three.
	start := c.panel
	c.route([]byte{'\t'})
	c.route([]byte{'\t'})
	c.route([]byte{'\t'})
	if c.panel != start {
		t.Fatalf("three tabs did not return to the first panel: %v", c.panel)
	}
}

func TestANewTrapRingsTheBell(t *testing.T) {
	rung := 0
	backend := &fakeBackend{}
	c := testConsole(backend)
	c.bell = func() { rung++ }
	c.refresh() // no traps yet
	backend.mu.Lock()
	backend.view.Traps = []store.Trap{{ID: "t1", Kind: store.TrapApproval, Prompt: "ok?"}}
	backend.mu.Unlock()
	c.refresh()
	if rung != 1 {
		t.Fatalf("bell rang %d times, want 1", rung)
	}
	c.refresh() // same trap still open; no second ring
	if rung != 1 {
		t.Fatalf("bell rang again on an unchanged trap: %d", rung)
	}
}

func TestTypingACommandArmsTheShorthandGoal(t *testing.T) {
	backend := &fakeBackend{}
	c := testConsole(backend)
	var typed []byte
	var typedMu sync.Mutex
	c.sendAgent = func(b []byte) { typedMu.Lock(); typed = append(typed, b...); typedMu.Unlock() }
	c.route([]byte{'n'})
	c.route([]byte("go test ./...\r"))
	backend.settle(t, c, func() bool { return strings.HasPrefix(c.message, "armed") })
	if len(backend.armed) != 1 || !strings.Contains(backend.armed[0], `bash.check("go test ./...")`) {
		t.Fatalf("armed = %q", backend.armed)
	}
	// Arming a loop starts the agent on it: the opening task is typed in and
	// submitted with a carriage return.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		typedMu.Lock()
		done := strings.Contains(string(typed), "go test ./...") && strings.Contains(string(typed), "\r")
		typedMu.Unlock()
		if done {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	typedMu.Lock()
	sent := string(typed)
	typedMu.Unlock()
	if !strings.Contains(sent, "Make this pass") || !strings.Contains(sent, "go test ./...") || !strings.HasSuffix(sent, "\r") {
		t.Fatalf("agent was not prompted to start: %q", sent)
	}
	c.refresh()
	text := visible(c.render(fullLayout(30, 100), 0))
	if !strings.Contains(text, "armed") || !strings.Contains(text, `"go test ./..."`) {
		t.Fatalf("pane does not show the armed loop:\n%s", text)
	}
	c.route([]byte("x"))
	backend.settle(t, c, func() bool { return c.message == "disarmed" })
	if backend.disarmed != 1 {
		t.Fatalf("disarmed %d times", backend.disarmed)
	}
}

func TestBadLoopIsReportedNotArmed(t *testing.T) {
	backend := &fakeBackend{}
	c := testConsole(backend)
	c.route([]byte{'n'})
	c.route([]byte("goal { verify x = nope; }\r"))
	if !strings.HasPrefix(c.message, "loop:") || len(backend.armed) != 0 {
		t.Fatalf("message %q, armed %v", c.message, backend.armed)
	}
}

func TestTrapsAreAnsweredByKey(t *testing.T) {
	backend := &fakeBackend{view: store.ConsoleObservation{Bound: true, CodexSessionID: "session",
		Run:   &store.RunObservation{ID: "run_1", Status: "trapped", StateKind: "trap", MachineKind: "goal"},
		Traps: []store.Trap{{ID: "trap_1", Kind: store.TrapApproval, Prompt: "Do the names match?"}}}}
	c := testConsole(backend)
	c.refresh()
	text := visible(c.render(fullLayout(30, 100), 0))
	if !strings.Contains(text, "Do the names match?") {
		t.Fatalf("pane does not surface the trap:\n%s", text)
	}
	c.route([]byte{'1'})
	if !strings.Contains(c.message, "wants a or r") {
		t.Fatalf("number on an approval trap gave %q", c.message)
	}
	c.route([]byte("r"))
	c.route([]byte("wrong package\r"))
	backend.settle(t, c, func() bool { return c.message == "rejected" })
	if len(backend.resolved) != 1 || backend.resolved[0] != "trap_1:reject:wrong package" {
		t.Fatalf("resolved = %q", backend.resolved)
	}

	backend.mu.Lock()
	backend.view.Traps = []store.Trap{{ID: "trap_2", Kind: store.TrapSelection, Prompt: "Which?", Options: []string{"result", "sentinel"}}}
	backend.mu.Unlock()
	c.refresh()
	c.route([]byte("a"))
	if !strings.Contains(c.message, "press its number") {
		t.Fatalf("a on a selection trap gave %q", c.message)
	}
	c.route([]byte("2"))
	backend.settle(t, c, func() bool { return c.message == "chose sentinel" })
	if backend.resolved[1] != "trap_2:sentinel:" {
		t.Fatalf("resolved = %q", backend.resolved)
	}
}

func TestStopNeedsTwoPresses(t *testing.T) {
	backend := &fakeBackend{view: store.ConsoleObservation{Bound: true, CodexSessionID: "session",
		Run: &store.RunObservation{ID: "run_1", Status: "running", MachineKind: "goal"}}}
	c := testConsole(backend)
	c.refresh()
	c.route([]byte{'d', 'n'})
	c.escape()
	if len(backend.killed) != 0 {
		t.Fatal("d then another key stopped the run")
	}
	c.route([]byte("dd"))
	backend.settle(t, c, func() bool { return c.message == "stopped" })
	if len(backend.killed) != 1 || backend.killed[0] != "session" {
		t.Fatalf("killed = %q", backend.killed)
	}
}

func TestBackendErrorsSurfaceInThePane(t *testing.T) {
	backend := &fakeBackend{fail: errors.New("request_failed: daemon says no")}
	c := testConsole(backend)
	c.route([]byte{'n'})
	c.route([]byte("go test\r"))
	backend.settle(t, c, func() bool { return strings.Contains(c.message, "daemon says no") })
}

func TestLimitsReadAsRemaining(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	attempts := store.LimitObservation{Kind: "attempts", Value: "10", ActivatedIteration: 2}
	if got := limitRemaining(attempts, 5, now); got != "attempts 3/10" {
		t.Fatalf("attempts = %q", got)
	}
	timed := store.LimitObservation{Kind: "time", Value: "1h", ActivatedAt: now.Add(-25 * time.Minute).Format(time.RFC3339Nano)}
	if got := limitRemaining(timed, 0, now); got != "time 35m00s left" {
		t.Fatalf("time = %q", got)
	}
	if got := limitRemaining(timed, 0, now.Add(2*time.Hour)); got != "time up" {
		t.Fatalf("expired = %q", got)
	}
}

func TestHooksMissingIsTheFirstThingShown(t *testing.T) {
	installed := false
	c := newConsole(&fakeBackend{}, "console", "/repo", Hooks{Installed: false, Install: func() error { installed = true; return nil }}, func() {})
	text := visible(c.render(fullLayout(30, 100), 0))
	if !strings.Contains(text, "hooks not installed") {
		t.Fatalf("pane does not warn about hooks:\n%s", text)
	}
	c.route([]byte{'s'})
	if !installed || !c.hooks.Installed || !strings.Contains(c.message, "installed") {
		t.Fatalf("s did not install: %v %v %q", installed, c.hooks.Installed, c.message)
	}
}

func TestCtrlCAndQQuitFromTheDashboard(t *testing.T) {
	for _, key := range [][]byte{{0x03}, {'q'}} {
		quit := 0
		c := testConsole(&fakeBackend{})
		c.quit = func() { quit++ }
		c.route(key)
		if quit != 1 {
			t.Fatalf("key %v did not quit: %d", key, quit)
		}
	}
	// Ctrl-C while typing a loop still quits.
	quit := 0
	c := testConsole(&fakeBackend{})
	c.quit = func() { quit++ }
	c.route([]byte{'n'})
	c.route([]byte{0x03})
	if quit != 1 {
		t.Fatalf("ctrl-c in the editor did not quit: %d", quit)
	}
	// On the agent screen, ctrl-c goes to the agent, not lazyloop.
	quit = 0
	c = testConsole(&fakeBackend{})
	c.quit = func() { quit++ }
	c.screen = screenAgent
	if got := c.route([]byte{0x03}); len(got) != 1 || got[0] != 0x03 || quit != 0 {
		t.Fatalf("ctrl-c on the agent screen quit lazyloop or was not forwarded: forwarded=%v quit=%d", got, quit)
	}
}
