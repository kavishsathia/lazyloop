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

type focus int

const (
	focusAgent focus = iota
	focusConsole
)

type mode int

const (
	modeKeys mode = iota
	modeLoop      // typing a loop to arm
	modeNote      // typing the reasoning behind a rejection
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

	focus   focus
	mode    mode
	line    []rune
	message string
	armKill bool
	view    *store.ConsoleObservation
	viewErr error
	dirty   func()
}

func newConsole(backend Backend, token, dir string, hooks Hooks, dirty func()) *console {
	return &console{backend: backend, token: token, dir: dir, hooks: hooks, now: time.Now, dirty: dirty}
}

// route takes raw bytes from the person's terminal and returns the bytes the
// agent should see. The prefix key toggles focus; with the console focused
// nothing is forwarded.
func (c *console) route(input []byte) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	forward := make([]byte, 0, len(input))
	for index := 0; index < len(input); index++ {
		b := input[index]
		if c.focus == focusAgent {
			if b == prefixKey {
				c.focus = focusConsole
				c.dirty()
				continue
			}
			forward = append(forward, b)
			continue
		}
		if b == 0x1b {
			// A lone escape is a key; an escape sequence is one the console
			// does not use, and is swallowed whole.
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
		c.focus = focusAgent
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
	if r != 'k' {
		c.armKill = false
	}
	switch r {
	case prefixKey, 'q':
		c.focus = focusAgent
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
	case 'k':
		if c.view == nil || c.view.Run == nil || !c.view.Bound {
			c.message = "nothing to kill"
			return
		}
		if !c.armKill {
			c.armKill = true
			c.message = "press k again to kill the run"
			return
		}
		c.armKill = false
		session := c.view.CodexSessionID
		c.run("killed", func(ctx context.Context) error {
			return c.backend.Kill(ctx, session, "killed from the lazyloop console")
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
	c.run("armed: "+text, func(ctx context.Context) error {
		return c.backend.Arm(ctx, c.token, c.dir, machine, source)
	})
}

func (c *console) openTrap() *store.Trap {
	if c.view == nil || len(c.view.Traps) == 0 {
		return nil
	}
	return &c.view.Traps[0]
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

// refresh asks the daemon what it knows. Called on a timer and after every
// action, so the pane never waits a full tick to show what just happened.
func (c *console) refresh() {
	if c.backend == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	view, err := c.backend.Observe(ctx, c.token)
	c.mu.Lock()
	changed := err != nil || c.viewErr != nil || !sameView(c.view, view)
	c.view, c.viewErr = view, err
	c.mu.Unlock()
	if changed {
		c.dirty()
	}
}

func sameView(a, b *store.ConsoleObservation) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.Bound != b.Bound || len(a.Traps) != len(b.Traps) || (a.Launch == nil) != (b.Launch == nil) || (a.Run == nil) != (b.Run == nil) {
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
