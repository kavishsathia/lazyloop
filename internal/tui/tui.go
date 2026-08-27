// Package tui is the screen behind a bare `lazyloop`: a dashboard listing every
// loop, and — one keypress away — the Codex running whichever loop you have
// selected. Each loop gets its own Codex process with a fresh session; several
// run at once, and the dashboard is how you watch and steer them.
package tui

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/vito/midterm"
	"golang.org/x/term"

	"hamster/internal/ir"
)

const (
	frameInterval = 33 * time.Millisecond
	quipInterval  = 12 * time.Second
	pollInterval  = 500 * time.Millisecond
)

// Options configure a session.
type Options struct {
	Dir     string   // working directory the agents start in
	Agent   []string // command to run per loop, as argv; defaults to codex
	Backend Backend  // reaches the daemon; nil leaves the dashboard decorative
	Hooks   Hooks
}

// layout is the terminal's size. The dashboard renders at the full width.
type layout struct {
	rows, cols int
	left       int
}

func fullLayout(rows, cols int) layout {
	if rows < 1 {
		rows = 1
	}
	if cols < 1 {
		cols = 1
	}
	return layout{rows: rows, cols: cols, left: cols}
}

// Run takes over the terminal until the person quits.
func Run(ctx context.Context, options Options) error {
	argv := options.Agent
	if len(argv) == 0 {
		argv = []string{"codex"}
	}
	if _, err := exec.LookPath(argv[0]); err != nil {
		return fmt.Errorf("%s is not on PATH", argv[0])
	}
	stdin := int(os.Stdin.Fd())
	if !term.IsTerminal(stdin) {
		return errors.New("lazyloop needs a terminal; run it interactively")
	}
	cols, rows, err := term.GetSize(stdin)
	if err != nil {
		return err
	}
	lay := fullLayout(rows, cols)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	previous, err := term.MakeRaw(stdin)
	if err != nil {
		return err
	}
	defer term.Restore(stdin, previous)

	out := bufio.NewWriterSize(os.Stdout, 1<<16)
	fmt.Fprint(out, "\x1b[?1049h\x1b[?25l\x1b[2J")
	out.Flush()
	defer fmt.Fprint(os.Stdout, "\x1b[0m\x1b[?25h\x1b[?1049l")

	var dirty atomic.Bool
	var bell atomic.Bool
	dirty.Store(true)
	markDirty := func() { dirty.Store(true) }

	fleet := newFleet(ctx, argv, options.Dir, os.Stdout, lay.rows, lay.cols, markDirty)
	defer fleet.killAll()

	quit := make(chan struct{})
	var quitOnce sync.Once
	console := newConsole(options.Backend, options.Dir, options.Hooks, markDirty)
	console.quit = func() { quitOnce.Do(func() { close(quit) }) }
	console.bell = func() { bell.Store(true); dirty.Store(true) }
	console.killAgent = fleet.kill
	console.startLoop = func(machine ir.Machine, source, label string) {
		if options.Backend == nil {
			console.setMessage("no daemon; cannot start a loop")
			return
		}
		token := NewConsoleToken()
		agent, err := fleet.spawn(token)
		if err != nil {
			console.setMessage(firstLine(err.Error()))
			return
		}
		armCtx, armCancel := context.WithTimeout(ctx, 5*time.Second)
		loop, err := options.Backend.Arm(armCtx, token, options.Dir, machine, source, label)
		armCancel()
		if err != nil {
			if agent.cmd.Process != nil {
				_ = agent.cmd.Process.Kill()
			}
			console.setMessage(firstLine(err.Error()))
			return
		}
		fleet.bind(token, loop.ID)
		console.selectLoop(loop.ID)
		console.setMessage("started: " + label)
		go injectWhenReady(ctx, agent, agentPrompt(label, source))
	}

	go func() {
		buffer := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buffer)
			if n > 0 {
				if forward := console.route(buffer[:n]); len(forward) > 0 {
					if agent := fleet.get(console.selected()); agent != nil {
						_ = agent.write(forward)
					}
				}
			}
			if err != nil {
				return
			}
		}
	}()
	if options.Backend != nil {
		go func() {
			console.refresh()
			ticker := time.NewTicker(pollInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					console.refresh()
				}
			}
		}()
	}

	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)

	frames := time.NewTicker(frameInterval)
	defer frames.Stop()
	quipTimer := time.NewTicker(quipInterval)
	defer quipTimer.Stop()
	clock := time.NewTicker(time.Second)
	defer clock.Stop()

	quip := rand.Intn(len(quips))
	lastScreen := screenMode(-1)
	lastAgent := ""

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-quit:
			return nil
		case <-winch:
			cols, rows, err := term.GetSize(stdin)
			if err != nil {
				continue
			}
			lay = fullLayout(rows, cols)
			fleet.resize(lay.rows, lay.cols)
			fmt.Fprint(out, "\x1b[2J")
			dirty.Store(true)
		case <-quipTimer.C:
			quip = nextQuip(quip)
			dirty.Store(true)
		case <-clock.C:
			dirty.Store(true)
		case <-frames.C:
			if bell.Swap(false) {
				out.WriteString("\a")
			}
			if !dirty.Swap(false) {
				out.Flush()
				continue
			}
			screen := console.activeScreen()
			if screen == screenAgent {
				selected := console.selected()
				fleet.setActive(selected)
				agent := fleet.get(selected)
				if screen != lastScreen || selected != lastAgent {
					fmt.Fprint(out, "\x1b[2J")
				}
				lastScreen, lastAgent = screen, selected
				if agent != nil {
					drawAgent(out, lay, agent.emu)
				} else {
					drawPlaceholder(out, lay)
				}
			} else {
				fleet.setActive("")
				if screen != lastScreen {
					fmt.Fprint(out, "\x1b[2J")
				}
				lastScreen, lastAgent = screen, ""
				drawDashboard(out, lay, console.render(lay, quip))
			}
		}
	}
}

// injectWhenReady hands a freshly spawned Codex its opening task once it has
// printed something (its composer is up) — or after a short wait if it is
// quiet — then submits with a carriage return.
func injectWhenReady(ctx context.Context, agent *agent, prompt string) {
	select {
	case <-agent.first:
	case <-time.After(3 * time.Second):
	case <-ctx.Done():
		return
	}
	select {
	case <-time.After(400 * time.Millisecond):
	case <-ctx.Done():
		return
	}
	_ = agent.write([]byte(prompt))
	select {
	case <-time.After(150 * time.Millisecond):
	case <-ctx.Done():
		return
	}
	_ = agent.write([]byte("\r"))
}

func nextQuip(current int) int {
	if len(quips) < 2 {
		return current
	}
	next := rand.Intn(len(quips) - 1)
	if next >= current {
		next++
	}
	return next
}

func drawAgent(out *bufio.Writer, lay layout, emulator *midterm.Terminal) {
	fmt.Fprint(out, "\x1b[?2026h\x1b[?25l")
	for row := 0; row < lay.rows; row++ {
		fmt.Fprintf(out, "\x1b[%d;1H\x1b[K", row+1)
		_ = emulator.RenderLine(out, row)
		out.WriteString("\x1b[0m")
	}
	fmt.Fprint(out, "\x1b[?2026l")
	out.Flush()
}

func drawDashboard(out *bufio.Writer, lay layout, rows []string) {
	fmt.Fprint(out, "\x1b[?2026h\x1b[?25l")
	for row := 0; row < lay.rows; row++ {
		fmt.Fprintf(out, "\x1b[%d;1H", row+1)
		if row < len(rows) {
			out.WriteString(rows[row])
		} else {
			out.WriteString(strings.Repeat(" ", lay.cols))
		}
		out.WriteString("\x1b[0m\x1b[K")
	}
	fmt.Fprint(out, "\x1b[?2026l")
	out.Flush()
}

func drawPlaceholder(out *bufio.Writer, lay layout) {
	fmt.Fprint(out, "\x1b[?2026h\x1b[?25l\x1b[2J")
	message := "this loop has no running agent"
	hint := "ctrl-]  back to the dashboard"
	fmt.Fprintf(out, "\x1b[%d;1H%s", lay.rows/2, fit(center(message, lay.cols), lay.cols, styleDim))
	fmt.Fprintf(out, "\x1b[%d;1H%s", lay.rows/2+2, fit(center(hint, lay.cols), lay.cols, styleDim))
	fmt.Fprint(out, "\x1b[?2026l")
	out.Flush()
}

// forwardedModes are the private modes an agent may set on the real terminal.
var forwardedModes = map[int]bool{
	1004: true, // focus reporting
	2004: true, // bracketed paste
}

var modeSequence = regexp.MustCompile(`\x1b\[\??(\d+)([hl])`)

// modeForwarder passes an allowlist of terminal-mode changes from one agent's
// emulator to the real terminal — but only while that agent is the one on
// screen, so a background agent's modes do not leak. It remembers what it set
// so it can turn those modes off when another agent takes the screen and back
// on when it returns.
type modeForwarder struct {
	w      io.Writer
	mu     sync.Mutex
	set    map[int]bool
	active func() bool
}

func (f *modeForwarder) Write(p []byte) (int, error) {
	for _, match := range modeSequence.FindAllSubmatch(p, -1) {
		mode, err := strconv.Atoi(string(match[1]))
		if err != nil || !forwardedModes[mode] {
			continue
		}
		on := match[2][0] == 'h'
		f.mu.Lock()
		f.set[mode] = on
		f.mu.Unlock()
		if f.active == nil || f.active() {
			fmt.Fprintf(f.w, "\x1b[?%d%s", mode, match[2])
		}
	}
	return len(p), nil
}

// reset turns off the modes this agent had enabled, when it leaves the screen.
func (f *modeForwarder) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for mode, on := range f.set {
		if on {
			fmt.Fprintf(f.w, "\x1b[?%dl", mode)
		}
	}
}

// reapply turns this agent's modes back on, when it takes the screen.
func (f *modeForwarder) reapply() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for mode, on := range f.set {
		if on {
			fmt.Fprintf(f.w, "\x1b[?%dh", mode)
		}
	}
}

func fit(text string, width int, style string) string {
	count := utf8.RuneCountInString(text)
	if count > width {
		runes := []rune(text)
		text = string(runes[:width])
		count = width
	}
	return style + text + "\x1b[0m" + strings.Repeat(" ", width-count)
}

func center(text string, width int) string {
	count := utf8.RuneCountInString(text)
	if count >= width {
		return text
	}
	return strings.Repeat(" ", (width-count)/2) + text
}

func wrap(text string, width int) []string {
	if width < 1 {
		return nil
	}
	var lines []string
	line := ""
	for _, word := range strings.Fields(text) {
		if line == "" {
			line = word
			continue
		}
		if utf8.RuneCountInString(line)+1+utf8.RuneCountInString(word) > width {
			lines = append(lines, line)
			line = word
			continue
		}
		line += " " + word
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}
