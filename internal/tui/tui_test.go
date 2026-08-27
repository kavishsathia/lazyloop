package tui

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/vito/midterm"
)

func TestLayoutSplitsAndSumsToWidth(t *testing.T) {
	for _, cols := range []int{3, 20, 57, 80, 120, 200} {
		lay := computeLayout(40, cols)
		if lay.left+1+lay.right != cols {
			t.Fatalf("cols=%d: left %d + divider + right %d != %d", cols, lay.left, lay.right, cols)
		}
		if lay.right < 1 || lay.left < 0 {
			t.Fatalf("cols=%d: degenerate layout %+v", cols, lay)
		}
	}
	wide := computeLayout(40, 160)
	if wide.left != 80 || wide.right != 79 {
		t.Fatalf("wide layout = %+v, want an even split", wide)
	}
	narrow := computeLayout(40, 60)
	if narrow.right < minRight {
		t.Fatalf("narrow layout gives the agent %d columns, want at least %d", narrow.right, minRight)
	}
}

var escapes = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)

func visible(lines []string) string {
	return escapes.ReplaceAllString(strings.Join(lines, "\n"), "")
}

func TestPaneLinesAreExactlyPaneWide(t *testing.T) {
	lay := computeLayout(24, 100)
	c := newConsole(nil, "console", "/tmp/some-project", Hooks{Installed: true}, func() {})
	for quip := range quips {
		lines := c.render(lay, quip)
		if len(lines) != lay.rows {
			t.Fatalf("quip %d: pane has %d lines, want %d", quip, len(lines), lay.rows)
		}
		for i, line := range lines {
			plain := escapes.ReplaceAllString(line, "")
			if got := utf8.RuneCountInString(plain); got != lay.left {
				t.Fatalf("quip %d line %d is %d cells wide, want %d: %q", quip, i, got, lay.left, plain)
			}
		}
	}
	if text := visible(c.render(lay, 0)); !strings.Contains(text, "some-project") || !strings.Contains(text, "ctrl-]") {
		t.Fatalf("idle pane lacks the directory or the prefix hint:\n%s", text)
	}
}

func TestWrapRespectsWidth(t *testing.T) {
	for _, line := range wrap("the quick brown fox jumps over the lazy dog", 10) {
		if utf8.RuneCountInString(line) > 10 {
			t.Fatalf("wrapped line %q exceeds width", line)
		}
	}
	if got := wrap("", 10); got != nil {
		t.Fatalf("wrap of empty = %v, want nil", got)
	}
}

func TestNextQuipNeverRepeats(t *testing.T) {
	for i := 0; i < 200; i++ {
		if next := nextQuip(3); next == 3 || next < 0 || next >= len(quips) {
			t.Fatalf("nextQuip(3) = %d", next)
		}
	}
}

func TestModeForwarderPassesOnlyInputEncodingModes(t *testing.T) {
	var real bytes.Buffer
	forwarder := &modeForwarder{w: &real, set: map[int]bool{}}
	emulator := midterm.NewTerminal(10, 40)
	emulator.ForwardRequests = forwarder
	// The exact startup preamble codex-cli 0.144.5 sends, plus mouse tracking
	// and a screen clear that must stay inside the emulator.
	_, _ = emulator.Write([]byte("\x1b[?2004h\x1b[>4;0m\x1b[>7u\x1b[?1004h\x1b[6n\x1b]10;?\x1b\\\x1b[?u\x1b[c\x1b[?1000h\x1b[?1006h\x1b[?1049h\x1b[2J"))
	if got, want := real.String(), "\x1b[?2004h\x1b[?1004h"; got != want {
		t.Fatalf("forwarded %q, want %q", got, want)
	}
	real.Reset()
	forwarder.reset()
	got := real.String()
	if !strings.Contains(got, "\x1b[?2004l") || !strings.Contains(got, "\x1b[?1004l") || strings.Count(got, "\x1b[") != 2 {
		t.Fatalf("reset emitted %q, want exactly the two modes turned off", got)
	}
}
