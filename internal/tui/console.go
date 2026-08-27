package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"hamster/internal/ir"
	"hamster/internal/store"
)

const prefixKey = 0x1d // ctrl-]

// screenMode is which full screen is showing.
type screenMode int

const (
	screenAgent     screenMode = iota // the selected loop's Codex fills the screen
	screenDashboard                   // the lazyloop dashboard fills the screen
)

type mode int

const (
	modeKeys mode = iota
	modeLoop      // typing a loop to arm
	modeNote      // typing the reasoning behind a rejection
)

// panelFocus is which of the three dashboard panels the keys act on.
type panelFocus int

const (
	focusList panelFocus = iota
	focusSource
	focusLogs
)

// Hooks tells the console whether the agent's hooks reach the daemon at all,
// and how to make them.
type Hooks struct {
	Installed bool
	Install   func() error
}

type console struct {
	mu      sync.Mutex
	backend Backend
	dir     string
	hooks   Hooks
	now     func() time.Time

	screen  screenMode
	mode    mode
	line    []rune
	message string
	armKill bool

	panel           panelFocus
	loops           []store.LoopSummary
	selectedID      string
	detail          *store.LoopDetail
	srcScroll       int
	logScroll       int
	logUserScrolled bool
	lastTraps       int

	dirty func()
	// Callbacks the runtime supplies.
	bell      func()                           // ring the terminal on a new escalation
	quit      func()                           // end the whole session
	startLoop func(ir.Machine, string, string) // spawn a Codex for a newly armed loop
	killAgent func(string)                     // kill the Codex running a loop
}

func newConsole(backend Backend, dir string, hooks Hooks, dirty func()) *console {
	return &console{backend: backend, dir: dir, hooks: hooks, now: time.Now, dirty: dirty, screen: screenDashboard}
}

// route takes raw bytes from the person's terminal and returns the bytes the
// selected agent should see. On the agent screen everything but the prefix is
// forwarded; on the dashboard every key acts on it and nothing is forwarded.
func (c *console) route(input []byte) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	forward := make([]byte, 0, len(input))
	for index := 0; index < len(input); index++ {
		b := input[index]
		if c.screen == screenAgent {
			if b == prefixKey {
				c.screen = screenDashboard
				c.dirty()
				continue
			}
			forward = append(forward, b)
			continue
		}
		if b == 0x03 { // ctrl-c quits lazyloop from the dashboard
			c.requestQuit()
			continue
		}
		if b == 0x1b {
			if index+2 < len(input) && input[index+1] == '[' {
				switch input[index+2] {
				case 'A':
					c.nav(-1)
					index += 2
					continue
				case 'B':
					c.nav(1)
					index += 2
					continue
				case 'C':
					c.cyclePanel(1)
					index += 2
					continue
				case 'D':
					c.cyclePanel(-1)
					index += 2
					continue
				}
			}
			if index+1 < len(input) {
				index = len(input) - 1
				continue
			}
			c.escape()
			continue
		}
		if b >= 0x80 {
			r, size := utf8.DecodeRune(input[index:])
			if c.mode != modeKeys && unicode.IsPrint(r) {
				c.line = append(c.line, r)
				c.dirty()
			}
			index += size - 1
			continue
		}
		c.key(rune(b))
	}
	return forward
}

func (c *console) escape() {
	if c.mode != modeKeys {
		c.mode, c.line = modeKeys, nil
		c.message = ""
	} else {
		c.screen = screenAgent
	}
	c.armKill = false
	c.dirty()
}

func (c *console) key(r rune) {
	defer c.dirty()
	if c.mode != modeKeys {
		c.edit(r)
		return
	}
	if r != 'd' {
		c.armKill = false
	}
	switch r {
	case prefixKey:
		c.screen = screenAgent
	case 'q':
		c.requestQuit()
	case '\t':
		c.cyclePanel(1)
	case 'j':
		c.nav(1)
	case 'k':
		c.nav(-1)
	case 'n':
		c.mode, c.line, c.message = modeLoop, nil, ""
	case 'a':
		c.answer(store.TrapApprove, "")
	case 'r':
		if trap := c.openTrap(); trap != nil && trap.Kind == store.TrapApproval {
			c.mode, c.line, c.message = modeNote, nil, ""
		}
	case 'd':
		c.stopSelected()
	case 's':
		if c.hooks.Installed || c.hooks.Install == nil {
			return
		}
		if err := c.hooks.Install(); err != nil {
			c.message = "install hooks: " + err.Error()
			return
		}
		c.hooks.Installed = true
		c.message = "hooks installed; new loops will be governed"
	default:
		if r >= '1' && r <= '9' {
			c.choose(int(r - '1'))
		}
	}
}

func (c *console) edit(r rune) {
	switch r {
	case '\r', '\n':
		text := strings.TrimSpace(string(c.line))
		submitted := c.mode
		c.mode, c.line = modeKeys, nil
		switch submitted {
		case modeLoop:
			c.arm(text)
		case modeNote:
			if text == "" {
				c.message = "a rejection needs a reason; the agent re-derives the work from it"
				return
			}
			c.answer(store.TrapReject, text)
		}
	case 0x7f, 0x08:
		if len(c.line) > 0 {
			c.line = c.line[:len(c.line)-1]
		}
	case 0x15: // ctrl-u
		c.line = nil
	default:
		if r >= 0x20 && r < 0x7f {
			c.line = append(c.line, r)
		}
	}
}

// arm compiles what the person typed and hands it to the runtime, which
// spawns a fresh Codex for it. A loop is one agent's whole job.
func (c *console) arm(text string) {
	machine, source, err := CompileRequest(text)
	if err != nil {
		c.message = "loop: " + firstLine(err.Error())
		return
	}
	if c.startLoop == nil {
		c.message = "cannot start a loop in this mode"
		return
	}
	c.message = "starting a Codex for this loop…"
	go c.startLoop(machine, source, strings.TrimSpace(text))
}

func (c *console) requestQuit() {
	if c.quit != nil {
		c.quit()
	}
}

func (c *console) cyclePanel(delta int) {
	c.panel = panelFocus((int(c.panel) + delta + 3) % 3)
	c.dirty()
}

func (c *console) nav(delta int) {
	switch c.panel {
	case focusList:
		c.moveSelection(delta)
	case focusSource:
		c.srcScroll = clampScroll(c.srcScroll + delta)
	case focusLogs:
		c.logScroll = clampScroll(c.logScroll + delta)
		c.logUserScrolled = true
	}
	c.dirty()
}

func clampScroll(v int) int {
	if v < 0 {
		return 0
	}
	return v
}

func (c *console) moveSelection(delta int) {
	if len(c.loops) == 0 {
		return
	}
	index := c.selectedIndex() + delta
	if index < 0 {
		index = 0
	}
	if index >= len(c.loops) {
		index = len(c.loops) - 1
	}
	if id := c.loops[index].ID; id != c.selectedID {
		c.selectedID = id
		c.srcScroll, c.logScroll, c.logUserScrolled = 0, 0, false
		c.detail = nil
	}
}

func (c *console) selectedIndex() int {
	for index, loop := range c.loops {
		if loop.ID == c.selectedID {
			return index
		}
	}
	return 0
}

func (c *console) selectedSummary() (store.LoopSummary, bool) {
	for _, loop := range c.loops {
		if loop.ID == c.selectedID {
			return loop, true
		}
	}
	return store.LoopSummary{}, false
}

// selected reports the loop the agent screen should show.
func (c *console) selected() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.selectedID
}

func (c *console) activeScreen() screenMode {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.screen
}

func (c *console) setMessage(message string) {
	c.mu.Lock()
	c.message = message
	c.mu.Unlock()
	c.dirty()
}

// selectLoop focuses a loop, resetting the panels that follow it. Used by the
// runtime after arming so the new loop is the one on screen.
func (c *console) selectLoop(id string) {
	c.mu.Lock()
	c.selectedID = id
	c.srcScroll, c.logScroll, c.logUserScrolled = 0, 0, false
	c.detail = nil
	c.mu.Unlock()
	c.dirty()
}

func (c *console) stopSelected() {
	summary, ok := c.selectedSummary()
	if !ok {
		c.message = "no loop selected"
		return
	}
	if !c.armKill {
		c.armKill = true
		c.message = "press d again to stop this loop"
		return
	}
	c.armKill = false
	id := summary.ID
	if c.killAgent != nil {
		c.killAgent(id)
	}
	c.run("stopped", func(ctx context.Context) error { return c.backend.StopLoop(ctx, id) })
}

// openTrap is the trap the selected loop is waiting on, if any.
func (c *console) openTrap() *store.Trap {
	if c.detail != nil && len(c.detail.Traps) > 0 {
		return &c.detail.Traps[0]
	}
	return nil
}

func (c *console) answer(resolution, note string) {
	trap := c.openTrap()
	if trap == nil {
		c.message = "nothing is waiting on you"
		return
	}
	if trap.Kind != store.TrapApproval {
		c.message = "this trap wants a choice: press its number"
		return
	}
	id := trap.ID
	done := "approved"
	if resolution == store.TrapReject {
		done = "rejected"
	}
	c.run(done, func(ctx context.Context) error { return c.backend.Resolve(ctx, id, resolution, note) })
}

func (c *console) choose(index int) {
	trap := c.openTrap()
	if trap == nil {
		c.message = "nothing is waiting on you"
		return
	}
	if trap.Kind != store.TrapSelection {
		c.message = "this trap wants a or r"
		return
	}
	if index >= len(trap.Options) {
		c.message = fmt.Sprintf("options go up to %d", len(trap.Options))
		return
	}
	id, option := trap.ID, trap.Options[index]
	c.run("chose "+option, func(ctx context.Context) error { return c.backend.Resolve(ctx, id, option, "") })
}

// run performs a backend action off the input path and reports how it went.
func (c *console) run(done string, action func(context.Context) error) {
	c.message = "…"
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := action(ctx)
		c.mu.Lock()
		if err != nil {
			c.message = firstLine(err.Error())
		} else {
			c.message = done
		}
		c.mu.Unlock()
		c.dirty()
		c.refresh()
	}()
}

// refresh reloads the list of loops and the selected loop's detail. Called on
// a timer and after every action.
func (c *console) refresh() {
	if c.backend == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	loops, err := c.backend.ListLoops(ctx, c.dir)
	if err != nil {
		loops = nil
	}

	c.mu.Lock()
	traps := 0
	for _, loop := range loops {
		traps += loop.Traps
	}
	escalated := traps > c.lastTraps
	c.lastTraps = traps
	c.loops = loops
	if c.selectedID == "" || !c.hasLoop(c.selectedID) {
		if len(loops) > 0 {
			c.selectedID = loops[0].ID
		} else {
			c.selectedID = ""
		}
		c.srcScroll, c.logScroll, c.logUserScrolled = 0, 0, false
	}
	selID := c.selectedID
	c.mu.Unlock()

	var detail *store.LoopDetail
	if selID != "" {
		detail, _ = c.backend.LoadLoop(ctx, selID)
	}
	c.mu.Lock()
	if selID == c.selectedID {
		c.detail = detail
	}
	c.mu.Unlock()

	if escalated && c.bell != nil {
		c.bell()
	}
	c.dirty()
}

func (c *console) hasLoop(id string) bool {
	for _, loop := range c.loops {
		if loop.ID == id {
			return true
		}
	}
	return false
}

func firstLine(text string) string {
	if index := strings.IndexByte(text, '\n'); index >= 0 {
		return text[:index]
	}
	return text
}

// agentPrompt is the opening task typed into the fresh Codex for a loop. The
// hook that claims the loop already tells the agent what must pass, so this
// only points it at the work and sets the stopping condition.
func agentPrompt(text, source string) string {
	command := strings.TrimSpace(text)
	if strings.HasPrefix(command, "goal") || strings.HasPrefix(command, "foreach") || strings.HasSuffix(command, ".hmstr") {
		return "I've armed a Hamster loop. Work it until Hamster reports the loop complete or escalates."
	}
	return fmt.Sprintf("Make this pass, and keep working until Hamster reports the loop complete: %s", command)
}

// limitRemaining describes how much of a limit is left, as the person reads it.
func limitRemaining(limit store.LimitObservation, iteration int, now time.Time) string {
	value := fmt.Sprint(limit.Value)
	switch limit.Kind {
	case "attempts":
		maximum, err := strconv.Atoi(value)
		if err != nil {
			return "attempts " + value
		}
		used := iteration - limit.ActivatedIteration
		return fmt.Sprintf("attempts %d/%d", used, maximum)
	case "time":
		duration, err := time.ParseDuration(value)
		if err != nil {
			return "time " + value
		}
		activated, err := time.Parse(time.RFC3339Nano, limit.ActivatedAt)
		if err != nil {
			return "time " + value
		}
		left := activated.Add(duration).Sub(now).Round(time.Second)
		if left <= 0 {
			return "time up"
		}
		return "time " + shortDuration(left) + " left"
	case "traps":
		return "traps ≤ " + value
	}
	return limit.Kind + " " + value
}

func shortDuration(d time.Duration) string {
	switch {
	case d >= time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	case d >= time.Minute:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%ds", int(d.Seconds()))
}
