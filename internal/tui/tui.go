// Package tui is the split screen behind a bare `lazyloop`: the console on
// the left, the agent on the right, in the directory you ran it from.
//
// The agent is the stock CLI running on a pseudo-terminal. Its output is
// decoded by an in-process terminal emulator and painted into the right pane,
// so its own rendering — colours, alt screen, cursor — survives intact.
// Everything typed goes straight to the agent until the prefix key hands
// focus to the console; Hamster draws chrome and answers questions, nothing
// more.
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

	"github.com/creack/pty"
	"github.com/vito/midterm"
	"golang.org/x/term"

	"hamster/internal/hook"
)

const (
	frameInterval = 33 * time.Millisecond
	quipInterval  = 12 * time.Second
	pollInterval  = 500 * time.Millisecond
	divider       = "│"
	minLeft       = 16
	minRight      = 40
)

// Options configure a split-screen session.
type Options struct {
	// Dir is the working directory the agent starts in.
	Dir string
	// Agent is the command to run on the right, as argv. Defaults to codex.
	Agent []string
	// Backend reaches the daemon. Nil leaves the console decorative.
	Backend Backend
	// Hooks says whether the agent's hooks reach the daemon, and how to fix that.
	Hooks Hooks
	// Token identifies this screen to hooks. Generated when empty.
	Token string
}

type layout struct {
	rows, cols  int
	left, right int
}

func computeLayout(rows, cols int) layout {
	if rows < 1 {
		rows = 1
	}
	if cols < 3 {
		cols = 3
	}
	left := cols / 2
	right := cols - left - 1
	if right < minRight {
		right = min(minRight, cols-1-minLeft)
		if right < 1 {
			right = 1
		}
		left = cols - right - 1
	}
	if left < minLeft {
		left = min(minLeft, cols-1-right)
		if left < 0 {
			left = 0
		}
		right = cols - left - 1
	}
	return layout{rows: rows, cols: cols, left: left, right: right}
}

// Run takes over the terminal until the agent exits.
func Run(ctx context.Context, options Options) error {
	agent := options.Agent
	if len(agent) == 0 {
		agent = []string{"codex"}
	}
	if _, err := exec.LookPath(agent[0]); err != nil {
		return fmt.Errorf("%s is not on PATH", agent[0])
	}
	stdin := int(os.Stdin.Fd())
	if !term.IsTerminal(stdin) {
		return errors.New("lazyloop needs a terminal; run it interactively")
	}
	cols, rows, err := term.GetSize(stdin)
	if err != nil {
		return err
	}
	lay := computeLayout(rows, cols)
	token := options.Token
	if token == "" {
		token = NewConsoleToken()
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	command := exec.CommandContext(ctx, agent[0], agent[1:]...)
	command.Dir = options.Dir
	command.Env = append(os.Environ(), hook.ConsoleEnvironment+"="+token)
	ptmx, err := pty.StartWithSize(command, &pty.Winsize{Rows: uint16(lay.rows), Cols: uint16(lay.right)})
	if err != nil {
		return fmt.Errorf("start %s: %w", agent[0], err)
	}
	defer ptmx.Close()

	emulator := midterm.NewTerminal(lay.rows, lay.right)
	// Answers to the agent's own queries (cursor position, device
	// attributes) belong to the agent, not to the real terminal.
	emulator.ForwardResponses = ptmx
	// A few of the agent's requests are really for the terminal the person
	// is sitting at: how to encode pastes and focus changes.
	forwarder := &modeForwarder{w: os.Stdout, set: map[int]bool{}}
	emulator.ForwardRequests = forwarder

	previous, err := term.MakeRaw(stdin)
	if err != nil {
		return err
	}
	defer term.Restore(stdin, previous)

	out := bufio.NewWriterSize(os.Stdout, 1<<16)
	fmt.Fprint(out, "\x1b[?1049h\x1b[?25l\x1b[2J")
	out.Flush()
	defer func() {
		forwarder.reset()
		fmt.Fprint(os.Stdout, "\x1b[0m\x1b[?25h\x1b[?1049l")
	}()

	var dirty atomic.Bool
	dirty.Store(true)
	console := newConsole(options.Backend, token, options.Dir, options.Hooks, func() { dirty.Store(true) })
	quip := rand.Intn(len(quips))

	exited := make(chan struct{})
	go func() {
		defer close(exited)
		buffer := make([]byte, 32*1024)
		for {
			n, err := ptmx.Read(buffer)
			if n > 0 {
				_, _ = emulator.Write(buffer[:n])
				dirty.Store(true)
			}
			if err != nil {
				// EIO is how a pty says the child has gone.
				return
			}
		}
	}()
	go func() {
		buffer := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buffer)
			if n > 0 {
				if forward := console.route(buffer[:n]); len(forward) > 0 {
					if _, err := ptmx.Write(forward); err != nil {
						return
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
	// Limits count down whether or not anything else changes.
	clock := time.NewTicker(time.Second)
	defer clock.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-exited:
			_ = command.Wait()
			return nil
		case <-winch:
			cols, rows, err := term.GetSize(stdin)
			if err != nil {
				continue
			}
			lay = computeLayout(rows, cols)
			_ = pty.Setsize(ptmx, &pty.Winsize{Rows: uint16(lay.rows), Cols: uint16(lay.right)})
			emulator.Resize(lay.rows, lay.right)
			fmt.Fprint(out, "\x1b[2J")
			dirty.Store(true)
		case <-quipTimer.C:
			quip = nextQuip(quip)
			dirty.Store(true)
		case <-clock.C:
			dirty.Store(true)
		case <-frames.C:
			if dirty.Swap(false) {
				draw(out, lay, emulator, console.render(lay, quip))
			}
		}
	}
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

func draw(out *bufio.Writer, lay layout, emulator *midterm.Terminal, left []string) {
	// Synchronized output: the terminal paints the frame at once.
	fmt.Fprint(out, "\x1b[?2026h\x1b[?25l")
	for row := 0; row < lay.rows; row++ {
		fmt.Fprintf(out, "\x1b[%d;1H", row+1)
		if row < len(left) {
			out.WriteString(left[row])
		} else {
			out.WriteString(strings.Repeat(" ", lay.left))
		}
		fmt.Fprintf(out, "\x1b[0m\x1b[2m%s\x1b[0m\x1b[K", divider)
		_ = emulator.RenderLine(out, row)
		out.WriteString("\x1b[0m")
	}
	fmt.Fprint(out, "\x1b[?2026l")
	out.Flush()
}

// forwardedModes are the private modes an agent may set on the real terminal.
// Both change how input is encoded and neither carries coordinates, so they
// can pass through untouched; anything positional (mouse tracking) would need
// its events translated into pane coordinates first, and is not forwarded.
var forwardedModes = map[int]bool{
	1004: true, // focus reporting
	2004: true, // bracketed paste
}

var modeSequence = regexp.MustCompile(`\x1b\[\??(\d+)([hl])`)

// modeForwarder passes an allowlist of mode changes from the emulator to the
// real terminal, normalising them to private-mode form, and remembers what it
// turned on so it can turn it off again when the session ends.
type modeForwarder struct {
	w   io.Writer
	mu  sync.Mutex
	set map[int]bool
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
		fmt.Fprintf(f.w, "\x1b[?%d%s", mode, match[2])
	}
	return len(p), nil
}

func (f *modeForwarder) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for mode, on := range f.set {
		if on {
			fmt.Fprintf(f.w, "\x1b[?%dl", mode)
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
