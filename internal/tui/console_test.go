package tui

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"hamster/internal/ir"
	"hamster/internal/store"
)

// fakeBackend serves loops in memory and records what the console asked for.
type fakeBackend struct {
	mu       sync.Mutex
	loops    []store.LoopSummary
	details  map[string]*store.LoopDetail
	armed    []string
	resolved []string
	stopped  []string
	fail     error
}

func (f *fakeBackend) Arm(_ context.Context, _, _ string, machine ir.Machine, source, label string) (*store.Loop, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return nil, f.fail
	}
	id := "loop-" + label
	f.armed = append(f.armed, source)
	f.loops = append([]store.LoopSummary{{ID: id, Label: label, Status: "armed"}}, f.loops...)
	if f.details == nil {
		f.details = map[string]*store.LoopDetail{}
	}
	f.details[id] = &store.LoopDetail{Loop: store.Loop{ID: id, Label: label, Source: source}, Status: "armed"}
	return &store.Loop{ID: id, Label: label, Source: source}, nil
}

func (f *fakeBackend) Resolve(_ context.Context, trapID, resolution, note string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resolved = append(f.resolved, trapID+":"+resolution+":"+note)
	for _, d := range f.details {
		d.Traps = nil
	}
	return nil
}

func (f *fakeBackend) StopLoop(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return f.fail
	}
	f.stopped = append(f.stopped, id)
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

func testConsole(backend *fakeBackend) *console {
	c := newConsole(backend, "/repo", Hooks{Installed: true}, func() {})
	// A realistic startLoop: arm through the backend and select the new loop,
	// as the runtime does after spawning a Codex.
	c.startLoop = func(machine ir.Machine, source, label string) {
		loop, err := backend.Arm(context.Background(), "token", "/repo", machine, source, label)
		if err != nil {
			c.setMessage(firstLine(err.Error()))
			return
		}
		c.selectLoop(loop.ID)
		c.setMessage("started: " + label)
	}
	return c
}

func settle(t *testing.T, c *console, want func() bool) {
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

var escapes = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)

func visible(lines []string) string {
	return escapes.ReplaceAllString(strings.Join(lines, "\n"), "")
}

func TestFlippingAndPanelCycling(t *testing.T) {
	c := testConsole(&fakeBackend{})
	if got := string(c.route([]byte{'n'})); got != "" {
		t.Fatalf("dashboard forwarded %q", got)
	}
	if c.screen != screenDashboard || c.mode != modeBuild {
		t.Fatalf("after n: screen %v mode %v", c.screen, c.mode)
	}
	c.route([]byte{0x1b}) // cancel the builder
	if c.mode != modeKeys {
		t.Fatalf("escape did not cancel: %v", c.mode)
	}
	c.route([]byte{0x1b}) // flip to the agent
	if c.screen != screenAgent {
		t.Fatal("escape did not flip to the agent")
	}
	if got := string(c.route([]byte("hi"))); got != "hi" {
		t.Fatalf("agent screen forwarded %q", got)
	}
	c.route([]byte{prefixKey}) // back to the dashboard
	if c.screen != screenDashboard {
		t.Fatal("prefix did not return to the dashboard")
	}
	start := c.panel
	c.route([]byte{'\t', '\t', '\t'})
	if c.panel != start {
		t.Fatalf("three tabs did not cycle back: %v", c.panel)
	}
}

func TestComposingAndStartingALoop(t *testing.T) {
	backend := &fakeBackend{}
	c := testConsole(backend)
	c.route([]byte{'n'})               // open the builder
	c.route([]byte{'l'})               // limit
	c.route([]byte("5\r"))             // 5 minutes
	c.route([]byte{'m'})               // machine check
	c.route([]byte("go test ./...\r")) // command, commit clause
	// The goal panel shows the live draft.
	if text := visible(c.render(fullLayout(30, 120), 0)); !strings.Contains(text, "limit time 5m") || !strings.Contains(text, `bash.check("go test ./...")`) {
		t.Fatalf("draft not shown live:\n%s", text)
	}
	c.route([]byte{'\r'}) // start
	settle(t, c, func() bool { return strings.HasPrefix(c.message, "started") })
	backend.mu.Lock()
	armed := append([]string(nil), backend.armed...)
	backend.mu.Unlock()
	if len(armed) != 1 || !strings.Contains(armed[0], "limit time 5m") || !strings.Contains(armed[0], `bash.check("go test ./...")`) {
		t.Fatalf("armed = %q", armed)
	}
	if c.mode != modeKeys {
		t.Fatalf("builder did not close after start: %v", c.mode)
	}
	c.refresh()
	if text := visible(c.render(fullLayout(30, 120), 0)); !strings.Contains(text, "go test ./...") {
		t.Fatalf("list does not show the loop:\n%s", text)
	}
}

func TestBuilderRefusesToStartWithoutAGate(t *testing.T) {
	c := testConsole(&fakeBackend{})
	c.route([]byte{'n'})
	c.route([]byte{'l'})
	c.route([]byte("5\r")) // only a limit, no check
	c.route([]byte{'\r'})  // try to start
	if !strings.Contains(c.message, "add a check") || c.mode != modeBuild {
		t.Fatalf("started without a gate: message %q mode %v", c.message, c.mode)
	}
	// Undo the limit, then a bad number is rejected.
	c.route([]byte{0x7f})
	c.route([]byte{'a'})
	c.route([]byte("lots\r"))
	if !strings.Contains(c.message, "positive number") {
		t.Fatalf("bad attempts accepted: %q", c.message)
	}
}

func TestJudgeAndTrapRecipesCompose(t *testing.T) {
	backend := &fakeBackend{}
	c := testConsole(backend)
	c.route([]byte{'n'})
	c.route([]byte{'j'})
	c.route([]byte("reviewed for security\r"))
	c.route([]byte{'t'})
	c.route([]byte("Does it read well?\r"))
	c.route([]byte{'\r'})
	settle(t, c, func() bool { return strings.HasPrefix(c.message, "started") })
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.armed) != 1 {
		t.Fatalf("not started: %q", backend.armed)
	}
	src := backend.armed[0]
	if !strings.Contains(src, `judge "reviewed for security"`) || !strings.Contains(src, `trap "Does it read well?"`) {
		t.Fatalf("recipes not composed:\n%s", src)
	}
}

func TestTrapsAreAnsweredByKey(t *testing.T) {
	backend := &fakeBackend{
		loops:   []store.LoopSummary{{ID: "l1", Label: "review", Status: "trapped", Traps: 1}},
		details: map[string]*store.LoopDetail{"l1": {Loop: store.Loop{ID: "l1", Label: "review"}, Status: "trapped", Traps: []store.Trap{{ID: "trap_1", Kind: store.TrapApproval, Prompt: "Do the names match?"}}}},
	}
	c := testConsole(backend)
	c.refresh()
	if text := visible(c.render(fullLayout(30, 120), 0)); !strings.Contains(text, "Do the names match?") {
		t.Fatalf("pane does not surface the trap:\n%s", text)
	}
	c.route([]byte{'1'})
	if !strings.Contains(c.message, "wants a or r") {
		t.Fatalf("number on approval trap gave %q", c.message)
	}
	c.route([]byte("r"))
	c.route([]byte("wrong package\r"))
	settle(t, c, func() bool { return c.message == "rejected" })
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.resolved) != 1 || backend.resolved[0] != "trap_1:reject:wrong package" {
		t.Fatalf("resolved = %q", backend.resolved)
	}
}

func TestStopNeedsTwoPresses(t *testing.T) {
	backend := &fakeBackend{loops: []store.LoopSummary{{ID: "l1", Label: "go test", Status: "running"}}}
	c := testConsole(backend)
	killed := 0
	c.killAgent = func(string) { killed++ }
	c.refresh()
	c.route([]byte{'d'})
	if len(backend.stopped) != 0 {
		t.Fatal("one press stopped the loop")
	}
	c.route([]byte{'d'})
	settle(t, c, func() bool { return c.message == "stopped" })
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.stopped) != 1 || backend.stopped[0] != "l1" || killed != 1 {
		t.Fatalf("stopped = %q, killed = %d", backend.stopped, killed)
	}
}

func TestStopErrorSurfaces(t *testing.T) {
	backend := &fakeBackend{loops: []store.LoopSummary{{ID: "l1", Label: "x", Status: "running"}}, fail: errors.New("request_failed: daemon says no")}
	c := testConsole(backend)
	c.killAgent = func(string) {}
	c.refresh()
	c.route([]byte("dd"))
	settle(t, c, func() bool { return strings.Contains(c.message, "daemon says no") })
}

func TestANewTrapRingsTheBell(t *testing.T) {
	backend := &fakeBackend{loops: []store.LoopSummary{{ID: "l1", Label: "x", Status: "running"}}}
	rung := 0
	c := testConsole(backend)
	c.bell = func() { rung++ }
	c.refresh() // no traps yet
	backend.mu.Lock()
	backend.loops[0].Traps = 1
	backend.mu.Unlock()
	c.refresh()
	if rung != 1 {
		t.Fatalf("bell rang %d times, want 1", rung)
	}
	c.refresh() // unchanged
	if rung != 1 {
		t.Fatalf("bell rang again on unchanged traps: %d", rung)
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
	quit := 0
	c := testConsole(&fakeBackend{})
	c.quit = func() { quit++ }
	c.screen = screenAgent
	if got := c.route([]byte{0x03}); len(got) != 1 || got[0] != 0x03 || quit != 0 {
		t.Fatalf("ctrl-c on the agent screen quit or was not forwarded: forwarded=%v quit=%d", got, quit)
	}
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
}

func TestHooksMissingIsWarned(t *testing.T) {
	installed := false
	c := newConsole(&fakeBackend{}, "/repo", Hooks{Installed: false, Install: func() error { installed = true; return nil }}, func() {})
	if text := visible(c.render(fullLayout(30, 120), 0)); !strings.Contains(text, "hooks not installed") {
		t.Fatalf("pane does not warn about hooks:\n%s", text)
	}
	c.route([]byte{'s'})
	if !installed || !c.hooks.Installed {
		t.Fatalf("s did not install: %v %v", installed, c.hooks.Installed)
	}
}

func agentBackend() *fakeBackend {
	return &fakeBackend{loops: []store.LoopSummary{{ID: "l1", Label: "go test", Status: "running",
		Agents: []store.LoopAgent{{AgentID: "ROOT", Root: true, Status: "running"}}}}}
}

func TestCtrlBracketOnlyOpensAnAgentRow(t *testing.T) {
	c := testConsole(agentBackend())
	c.refresh()
	if c.onAgentRow() {
		t.Fatal("selection started on an agent row, want the loop row")
	}
	// On a loop row, ctrl-] does not open the agent screen.
	c.route([]byte{prefixKey})
	if c.screen == screenAgent {
		t.Fatal("ctrl-] flipped from a loop row")
	}
	if !strings.Contains(c.message, "select an agent") {
		t.Fatalf("no hint on a loop row: %q", c.message)
	}
	// Move down to the agent row; now ctrl-] opens it.
	c.route([]byte{'j'})
	if !c.onAgentRow() {
		t.Fatal("j did not move onto the agent row")
	}
	c.route([]byte{prefixKey})
	if c.screen != screenAgent {
		t.Fatal("ctrl-] did not open the agent")
	}
}

func TestClickingSelectsTheRow(t *testing.T) {
	c := testConsole(agentBackend())
	c.refresh()
	c.render(fullLayout(30, 120), 0) // captures the list geometry
	// Row 2 is the loop, row 3 its agent (row 1 is the panel border).
	c.mouse(2, 3, 0, true)
	if !c.onAgentRow() {
		t.Fatalf("clicking the agent row did not select it: agent=%d", c.selAgent)
	}
	c.mouse(2, 2, 0, true)
	if c.onAgentRow() {
		t.Fatal("clicking the loop row still shows an agent row selected")
	}
	// A click outside the list changes nothing.
	before := c.selLoop
	c.mouse(200, 3, 0, true)
	if c.selLoop != before {
		t.Fatal("a click outside the list moved the selection")
	}
}

func TestParseMouseDecodesSGR(t *testing.T) {
	col, row, button, press, end, ok := parseMouse([]byte("\x1b[<0;5;3M"), 0)
	if !ok || col != 5 || row != 3 || button != 0 || !press || end != 8 {
		t.Fatalf("parseMouse = %d,%d,%d,%v,%d,%v", col, row, button, press, end, ok)
	}
	if _, _, _, _, _, ok := parseMouse([]byte("\x1b[A"), 0); ok {
		t.Fatal("an arrow key was parsed as a mouse event")
	}
}
