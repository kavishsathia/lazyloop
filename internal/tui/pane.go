package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"hamster/internal/store"
)

// Styles are bare SGR sequences; the pane is too small to want a theme.
const (
	styleBold   = "\x1b[1m"
	styleDim    = "\x1b[2m"
	styleItalic = "\x1b[3m"
	styleArt    = "\x1b[33m"
	styleGood   = "\x1b[32m"
	styleBad    = "\x1b[31m"
	styleWarn   = "\x1b[33m"
	styleFocus  = "\x1b[7m"
	styleKey    = "\x1b[36m"
)

type line struct {
	text  string
	style string
}

// render lays out the left pane. Every returned string is exactly lay.left
// cells wide. The pane reads top-down in the order a person needs it: what
// is waiting on them, what is running, what is armed, then how to act.
func (c *console) render(lay layout, quip int) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	width := lay.left
	if width == 0 {
		return nil
	}
	inner := width - 4
	var lines []line
	add := func(text, style string) { lines = append(lines, line{text, style}) }
	blank := func() { add("", "") }
	para := func(text, style string) {
		for _, wrapped := range wrap(text, inner) {
			add("  "+wrapped, style)
		}
	}
	heading := func(text string) {
		blank()
		add("  "+text, styleBold)
	}

	blank()
	for _, row := range art {
		add(center(row, width), styleArt)
	}
	blank()
	title := "lazyloop"
	if c.focus == focusConsole {
		title = " lazyloop "
		add(center(title, width), styleFocus)
	} else {
		add(center(title, width), styleBold)
	}
	add(center(filepath.Base(c.dir), width), styleDim)

	view := c.view
	if !c.hooks.Installed {
		heading("hooks")
		para("The agent's hooks do not reach Hamster yet, so nothing on the right is governed.", styleWarn)
		para("s  install the global hook", styleKey)
	}

	if view != nil && len(view.Traps) > 0 {
		trap := view.Traps[0]
		heading("waiting on you")
		para(trap.Prompt, styleItalic)
		if trap.Kind == store.TrapSelection {
			for index, option := range trap.Options {
				if index >= 9 {
					break
				}
				para(fmt.Sprintf("%d  %s", index+1, option), styleKey)
			}
		} else {
			para("a  approve    r  reject", styleKey)
		}
		if len(view.Traps) > 1 {
			para(fmt.Sprintf("and %d more after this", len(view.Traps)-1), styleDim)
		}
	}

	heading("loop")
	switch {
	case c.viewErr != nil:
		para("daemon: "+firstLine(c.viewErr.Error()), styleBad)
	case view == nil || !view.Bound:
		para("waiting for the agent's first hook", styleDim)
	case view.Run == nil:
		para("no loop is governing this agent", styleDim)
	default:
		run := view.Run
		name := run.MachineName
		if name == "" {
			name = "anonymous " + run.MachineKind
		}
		para(name, "")
		state := run.StateName
		if state == "" {
			state = run.StateID
		}
		statusStyle := ""
		switch run.Status {
		case "complete":
			statusStyle = styleGood
		case "trapped", "waiting":
			statusStyle = styleWarn
		case "running":
		default:
			statusStyle = styleBad
		}
		para(fmt.Sprintf("%s · %s (%s)", run.Status, state, run.StateKind), statusStyle)
		parts := []string{fmt.Sprintf("iteration %d", run.Iteration)}
		for _, limit := range view.Limits {
			parts = append(parts, limitRemaining(limit, run.Iteration, c.now()))
		}
		para(strings.Join(parts, " · "), styleDim)
		if len(view.Verifications) > 0 {
			for _, check := range view.Verifications {
				mark, style := "✓", styleGood
				if check.Error != "" || !check.Passed {
					mark, style = "✗", styleBad
				}
				text := mark + " " + check.Name
				if check.Command != "" {
					text += "  " + check.Command
				}
				para(text, style)
			}
		}
		if view.Summary.Events > 0 {
			para(fmt.Sprintf("%d tool calls · %d denied · %d checks", view.Summary.ToolCalls, view.Summary.Denied, view.Summary.Verifications), styleDim)
		}
		if run.TerminationReason != "" {
			para(run.TerminationReason, styleDim)
		}
	}

	if view != nil && view.Launch != nil {
		heading("armed")
		para(launchLabel(*view.Launch), "")
		para("starts with the agent's next message", styleDim)
		para("x  disarm", styleKey)
	}

	if c.message != "" {
		blank()
		para(c.message, styleItalic)
	}

	// The footer sits at the bottom: the line being typed, or the keys.
	var footer []line
	switch c.mode {
	case modeLoop:
		footer = append(footer, line{"  new loop", styleBold})
		footer = append(footer, editorLines("  › ", c.line, inner)...)
		footer = append(footer, line{"  a command to make pass, a .hmstr path, or goal { … }", styleDim})
		footer = append(footer, line{"  enter  arm    esc  cancel", styleKey})
	case modeNote:
		footer = append(footer, line{"  why reject?", styleBold})
		footer = append(footer, editorLines("  › ", c.line, inner)...)
		footer = append(footer, line{"  enter  reject with this note    esc  cancel", styleKey})
	default:
		if c.focus == focusConsole {
			footer = append(footer, line{"  n  new loop    k  kill    q  back to the agent", styleKey})
		} else {
			for _, wrapped := range wrap(quips[quip], inner) {
				footer = append(footer, line{"  " + wrapped, styleItalic})
			}
			footer = append(footer, line{"", ""})
			footer = append(footer, line{center("ctrl-] for the console", width), styleDim})
		}
	}

	rows := make([]string, 0, lay.rows)
	body := lay.rows - len(footer) - 1
	if body < 0 {
		body = 0
	}
	for index := 0; index < body; index++ {
		if index < len(lines) {
			rows = append(rows, fit(lines[index].text, width, lines[index].style))
		} else {
			rows = append(rows, fit("", width, ""))
		}
	}
	for len(rows) < lay.rows-len(footer) {
		rows = append(rows, fit("", width, ""))
	}
	for _, item := range footer {
		if len(rows) >= lay.rows {
			break
		}
		rows = append(rows, fit(item.text, width, item.style))
	}
	for len(rows) < lay.rows {
		rows = append(rows, fit("", width, ""))
	}
	return rows
}

func editorLines(prompt string, text []rune, width int) []line {
	content := string(text) + "█"
	var lines []line
	first := true
	for _, wrapped := range wrapRunes(content, width-utf8.RuneCountInString(prompt)) {
		prefix := prompt
		if !first {
			prefix = strings.Repeat(" ", utf8.RuneCountInString(prompt))
		}
		lines = append(lines, line{prefix + wrapped, ""})
		first = false
	}
	return lines
}

// wrapRunes breaks anywhere, unlike wrap: an edited line has no words to
// respect yet.
func wrapRunes(text string, width int) []string {
	if width < 1 {
		return []string{text}
	}
	runes := []rune(text)
	var lines []string
	for len(runes) > width {
		lines = append(lines, string(runes[:width]))
		runes = runes[width:]
	}
	return append(lines, string(runes))
}

func launchLabel(launch store.Launch) string {
	if launch.Source == "" {
		return launch.Machine.Name
	}
	source := strings.TrimSpace(launch.Source)
	if strings.HasPrefix(source, "goal") {
		// Show the verifier behind a shorthand, or just say it is a goal.
		if start := strings.Index(source, "bash.check("); start >= 0 {
			rest := source[start+len("bash.check("):]
			if end := strings.Index(rest, ");"); end >= 0 {
				return rest[:end]
			}
		}
		return "a goal"
	}
	return source
}
