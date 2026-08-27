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

	"hamster/internal/store"
)

// The console is the left pane once it has something to say. It is a small
// state machine over keystrokes: which pane has focus, whether a line is
// being typed, and what the daemon last reported. Every action it takes goes
// through the Backend; it never touches run state itself.

const prefixKey = 0x1d // ctrl-]

// screenMode is which full screen is showing. lazyloop and the agent each own
// the whole terminal; a keystroke flips between them. The agent runs the
// whole time, drawn only while it is the one on screen.
type screenMode int

const (
	screenAgent     screenMode = iota // Codex fills the screen
	screenDashboard                   // the lazyloop console fills the screen
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
	focusList   panelFocus = iota // the list of loops (up/down selects)
	focusSource                   // the selected loop's source (up/down scrolls)
	focusLogs                     // the selected loop's log (up/down scrolls)
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
	token   string
	dir     string
	hooks   Hooks
	now     func() time.Time

	screen    screenMode
	mode      mode
	line      []rune
	message   string
	armKill   bool
	view      *store.ConsoleObservation
	viewErr   error
	lastTraps int
	dirty     func()
	// bell is rung when a trap newly opens, so an escalation reaches a person
	// who is watching the agent instead of the dashboard. Optional.
	bell func()
	// sendAgent types into the agent. Arming a loop uses it to hand the agent
	// its opening task, so a loop started from the dashboard begins on its own
	// without the person flipping over to prompt it. Optional.
	sendAgent func([]byte)
	// quit ends the whole session. Ctrl-C or q from the dashboard calls it.
	// Optional.
	quit func()

	// The three panels and what is selected within them.
	panel           panelFocus
	loops           []store.LoopSummary
	selectedID      string
	detail          *store.LoopDetail
	srcScroll       int
	logScroll       int
	logUserScrolled bool
}

func newConsole(backend Backend, token, dir string, hooks Hooks, dirty func()) *console {
	// The dashboard is home: you land on lazyloop and flip to the agent when
	// you want to talk to it.
	return &console{backend: backend, token: token, dir: dir, hooks: hooks, now: time.Now, dirty: dirty, screen: screenDashboard}
}

// route takes raw bytes from the person's terminal and returns the bytes the
// agent should see. While the agent fills the screen every key reaches it
// except the prefix, which flips to the dashboard; while the dashboard fills
// the screen every key acts on it, and nothing is forwarded.
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
			// Arrow keys drive the panels; other escape sequences are ignored,
			// and a lone escape is the cancel key.
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
			// Multi-byte text only matters to the line editor.
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
	case 'x':
		c.run("disarmed", func(ctx context.Context) error { return c.backend.Disarm(ctx, c.token) })
	case 'a':
		c.answer(store.TrapApprove, "")
	case 'r':
		if trap := c.openTrap(); trap != nil && trap.Kind == store.TrapApproval {
			c.mode, c.line, c.message = modeNote, nil, ""
		}
	case 'd':
		if c.view == nil || c.view.Run == nil || !c.view.Bound {
			c.message = "nothing running to stop"
			return
		}
		if !c.armKill {
			c.armKill = true
			c.message = "press d again to stop the running loop"
			return
		}
		c.armKill = false
		session := c.view.CodexSessionID
		c.run("stopped", func(ctx context.Context) error {
			return c.backend.Kill(ctx, session, "stopped from the lazyloop console")
		})
	case 's':
		if c.hooks.Installed || c.hooks.Install == nil {
			return
		}
		if err := c.hooks.Install(); err != nil {
			c.message = "install hooks: " + err.Error()
			return
		}
		c.hooks.Installed = true
		c.message = "hooks installed; restart the agent to pick them up"
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

func (c *console) arm(text string) {
	machine, source, err := CompileRequest(text)
	if err != nil {
		c.message = "loop: " + firstLine(err.Error())
		return
	}
	prompt := agentPrompt(text, source)
	label := strings.TrimSpace(text)
	c.message = "…"
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := c.backend.Arm(ctx, c.token, c.dir, machine, source, label)
		c.mu.Lock()
		start := err == nil && c.sendAgent != nil
		if err != nil {
			c.message = firstLine(err.Error())
		} else if start {
			c.message = "armed — starting the agent"
		} else {
			c.message = "armed: " + text
		}
		send := c.sendAgent
		c.mu.Unlock()
		if start {
			// The text lands first, then Enter a beat later, so the agent's
			// composer has ingested the whole line before it submits.
			send([]byte(prompt))
			time.Sleep(150 * time.Millisecond)
			send([]byte("\r"))
		}
		c.dirty()
		c.refresh()
	}()
}

// agentPrompt is the opening task typed into the agent when a loop is armed.
// The hook that claims the loop already tells the agent what must pass, so
// this only has to point it at the work and set the stopping condition.
func agentPrompt(text, source string) string {
	command := strings.TrimSpace(text)
	if strings.HasPrefix(command, "goal") || strings.HasPrefix(command, "foreach") || strings.HasSuffix(command, ".hmstr") {
		return "I've armed a Hamster loop. Work it until Hamster reports the loop complete or escalates."
	}
	return fmt.Sprintf("Make this pass, and keep working until Hamster reports the loop complete: %s", command)
}

// requestQuit ends the whole session.
func (c *console) requestQuit() {
	if c.quit != nil {
		c.quit()
	}
}

// cyclePanel moves keyboard focus between the three panels.
func (c *console) cyclePanel(delta int) {
	c.panel = panelFocus((int(c.panel) + delta + 3) % 3)
	c.dirty()
}

// nav moves within the focused panel: selecting a loop, or scrolling text.
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

// openTrap is the trap awaiting an answer: the selected loop's if it has one,
// otherwise the running loop's.
func (c *console) openTrap() *store.Trap {
	if c.detail != nil && len(c.detail.Traps) > 0 {
		return &c.detail.Traps[0]
	}
	if c.view != nil && len(c.view.Traps) > 0 {
		return &c.view.Traps[0]
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

// refresh asks the daemon what it knows: the running loop, the list of loops,
// and the selected loop's source and log. Called on a timer and after every
// action, so the panels never wait a full tick to show what just happened.
func (c *console) refresh() {
	if c.backend == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	view, err := c.backend.Observe(ctx, c.token)
	loops, loopsErr := c.backend.ListLoops(ctx, c.dir)
	if loopsErr != nil {
		loops = nil
	}

	c.mu.Lock()
	traps := 0
	if view != nil {
		traps = len(view.Traps)
	}
	escalated := traps > c.lastTraps
	c.lastTraps = traps
	c.view, c.viewErr = view, err
	c.loops = loops
	// Keep the selection valid; default to the newest loop, which is the one
	// running or armed right now.
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

func sameView(a, b *store.ConsoleObservation) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.Bound != b.Bound || len(a.Traps) != len(b.Traps) || (a.Armed == nil) != (b.Armed == nil) || (a.Run == nil) != (b.Run == nil) {
		return false
	}
	if a.Run != nil && (a.Run.ID != b.Run.ID || a.Run.StateID != b.Run.StateID || a.Run.Status != b.Run.Status || a.Run.Iteration != b.Run.Iteration || a.Run.UpdatedAt != b.Run.UpdatedAt) {
		return false
	}
	return a.Summary == b.Summary && a.TrapCount == b.TrapCount
}

func firstLine(text string) string {
	if index := strings.IndexByte(text, '\n'); index >= 0 {
		return text[:index]
	}
	return text
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
