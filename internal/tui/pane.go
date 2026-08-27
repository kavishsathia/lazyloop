package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"hamster/internal/store"
)

// The pane borrows lazygit's look: each concern is a rounded panel with its
// name in the top border, and colour carries meaning — green for what passed,
// red for what failed, amber for what is waiting on a person, blue for the
// keys they can press.
const (
	reset = "\x1b[0m"

	styleBold   = "\x1b[1m"
	styleDim    = "\x1b[2m"
	styleItalic = "\x1b[3m"
	styleFocus  = "\x1b[7m"

	styleArt  = "\x1b[38;5;214m" // amber hamster
	styleGood = "\x1b[38;5;42m"  // green
	styleBad  = "\x1b[38;5;203m" // red
	styleWarn = "\x1b[38;5;214m" // amber
	styleKey  = "\x1b[38;5;39m"  // blue

	borderIdle  = "\x1b[38;5;240m" // a panel at rest
	borderRun   = "\x1b[38;5;39m"  // a loop in progress
	borderGood  = "\x1b[38;5;42m"
	borderWarn  = "\x1b[38;5;214m"
	borderBad   = "\x1b[38;5;203m"
	borderAlert = "\x1b[38;5;203m" // a question waiting on the person
)

type line struct {
	text  string
	style string
}

// render lays out the left pane. Every returned string is exactly lay.left
// cells wide. It reads top-down in the order a person needs it: the masthead,
// what is waiting on them, the loop, what is armed, then how to act.
func (c *console) render(lay layout, quip int) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	width := lay.left
	if width < 4 {
		return nil
	}
	view := c.view
	focused := c.focus == focusConsole

	var body []string
	body = append(body, c.masthead(width)...)

	if !c.hooks.Installed {
		body = append(body, panel(width, "hooks", borderWarn, []line{
			{"the agent's hooks don't reach Hamster yet — nothing on the right is governed", styleWarn},
			{"s  install the global hook", styleKey},
		})...)
	}

	if view != nil && len(view.Traps) > 0 {
		body = append(body, c.trapPanel(width, view.Traps)...)
	}

	body = append(body, c.loopPanel(width, view, focused)...)

	if view != nil && view.Launch != nil {
		body = append(body, panel(width, "armed", pick(focused, borderRun, borderIdle), []line{
			{launchLabel(*view.Launch), styleBold},
			{"starts with the agent's next message", styleDim},
			{"x  disarm", styleKey},
		})...)
	}

	if c.message != "" {
		body = append(body, blankRow(width), styledRow("  "+firstLine(c.message), width, styleItalic))
	}

	footer := c.footer(width, quip)
	return frame(width, lay.rows, body, footer)
}

func (c *console) masthead(width int) []string {
	rows := []string{blankRow(width)}
	for _, art := range art {
		rows = append(rows, styledRow(center(art, width), width, styleArt))
	}
	rows = append(rows, blankRow(width))
	if c.focus == focusConsole {
		rows = append(rows, styledRow(center(" lazyloop ", width), width, styleFocus+styleBold))
	} else {
		rows = append(rows, styledRow(center("lazyloop", width), width, styleBold))
	}
	rows = append(rows, styledRow(center(filepath.Base(c.dir), width), width, styleDim))
	rows = append(rows, blankRow(width))
	return rows
}

func (c *console) trapPanel(width int, traps []store.Trap) []string {
	trap := traps[0]
	body := []line{{trap.Prompt, styleItalic}}
	if trap.Kind == store.TrapSelection {
		for index, option := range trap.Options {
			if index >= 9 {
				break
			}
			body = append(body, line{fmt.Sprintf("%d  %s", index+1, option), styleKey})
		}
	} else {
		body = append(body, line{"a  approve", styleGood}, line{"r  reject", styleBad})
	}
	if len(traps) > 1 {
		body = append(body, line{fmt.Sprintf("and %d more after this", len(traps)-1), styleDim})
	}
	return panel(width, "waiting on you", borderAlert, body)
}

func (c *console) loopPanel(width int, view *store.ConsoleObservation, focused bool) []string {
	switch {
	case c.viewErr != nil:
		return panel(width, "loop", borderBad, []line{{"daemon: " + firstLine(c.viewErr.Error()), styleBad}})
	case view == nil || !view.Bound:
		return panel(width, "loop", pick(focused, borderRun, borderIdle), []line{{"waiting for the agent's first hook", styleDim}})
	case view.Run == nil:
		return panel(width, "loop", pick(focused, borderRun, borderIdle), []line{{"no loop is governing this agent", styleDim}})
	}

	run := view.Run
	textStyle, border := statusColour(run.Status)
	if run.Status == "running" && !focused {
		border = borderIdle
	}

	name := run.MachineName
	if name == "" {
		name = "anonymous " + run.MachineKind
	}
	state := run.StateName
	if state == "" {
		state = run.StateID
	}
	body := []line{
		{name, styleBold},
		{fmt.Sprintf("%s · %s", run.Status, state), textStyle},
	}
	meter := []string{fmt.Sprintf("iteration %d", run.Iteration)}
	for _, limit := range view.Limits {
		meter = append(meter, limitRemaining(limit, run.Iteration, c.now()))
	}
	body = append(body, line{strings.Join(meter, " · "), styleDim})

	for _, check := range view.Verifications {
		mark, style := "✓", styleGood
		if check.Error != "" || !check.Passed {
			mark, style = "✗", styleBad
		}
		label := mark + " " + check.Name
		if check.Command != "" {
			label += "  " + check.Command
		}
		body = append(body, line{label, style})
		if !check.Passed {
			if output := lastLine(check.Output); output != "" {
				body = append(body, line{"  " + output, styleDim})
			} else if check.Error != "" {
				body = append(body, line{"  " + firstLine(check.Error), styleDim})
			}
		}
	}
	if view.Summary.Events > 0 {
		body = append(body, line{fmt.Sprintf("%d tool calls · %d denied · %d checks", view.Summary.ToolCalls, view.Summary.Denied, view.Summary.Verifications), styleDim})
	}
	if run.TerminationReason != "" {
		body = append(body, line{run.TerminationReason, styleDim})
	}
	return panel(width, "loop", border, body)
}

func (c *console) footer(width int, quip int) []string {
	switch c.mode {
	case modeLoop:
		return editorPanel(width, "new loop", c.line, "a command to make pass, a .hmstr path, or goal { … }")
	case modeNote:
		return editorPanel(width, "why reject?", c.line, "the agent re-derives the work from your reason")
	}
	if c.focus == focusConsole {
		return []string{
			keyRow(width, []keyHint{{"n", "loop"}, {"k", "kill"}, {"q", "agent"}}),
		}
	}
	rows := []string{}
	for _, wrapped := range wrap(quips[quip], width-4) {
		rows = append(rows, styledRow("  "+wrapped, width, styleItalic))
	}
	rows = append(rows, blankRow(width), styledRow(center("ctrl-]  console", width), width, styleDim))
	return rows
}

type keyHint struct{ key, label string }

func keyRow(width int, hints []keyHint) string {
	var b strings.Builder
	visible := 0
	b.WriteString("  ")
	visible += 2
	for index, hint := range hints {
		if index > 0 {
			b.WriteString("   ")
			visible += 3
		}
		b.WriteString(styleKey + hint.key + reset + " " + styleDim + hint.label + reset)
		visible += utf8.RuneCountInString(hint.key) + 1 + utf8.RuneCountInString(hint.label)
	}
	if visible < width {
		b.WriteString(strings.Repeat(" ", width-visible))
	}
	return b.String()
}

func editorPanel(width int, title string, text []rune, hint string) []string {
	body := editorLines("› ", text, width-4)
	body = append(body, line{hint, styleDim})
	return panel(width, title, borderRun, body)
}

// panel draws a rounded box with its title in the top border. Every row it
// returns is exactly width visible cells.
func panel(width int, title, border string, body []line) []string {
	if width < 4 {
		return nil
	}
	inner := width - 4 // "│ " + content + " │"
	rows := make([]string, 0, len(body)+2)

	label := ""
	if title != "" {
		label = "─ " + title + " "
	}
	if runeLen(label) > width-3 {
		label = truncateRunes(label, width-3)
	}
	dashes := width - 2 - runeLen(label)
	rows = append(rows, border+"╭"+label+strings.Repeat("─", dashes)+"╮"+reset)

	for _, item := range body {
		wrapped := wrap(item.text, inner)
		if len(wrapped) == 0 {
			wrapped = []string{""}
		}
		for _, piece := range wrapped {
			content := fit(piece, inner, item.style)
			rows = append(rows, border+"│"+reset+" "+content+" "+border+"│"+reset)
		}
	}
	rows = append(rows, border+"╰"+strings.Repeat("─", width-2)+"╯"+reset)
	return rows
}

// frame stacks the body and footer to exactly rows lines, padding the gap
// between them so the footer sits at the bottom, and trimming the body from
// its end if it does not fit.
func frame(width, rows int, body, footer []string) []string {
	if len(footer) > rows {
		footer = footer[:rows]
	}
	capacity := rows - len(footer)
	if len(body) > capacity {
		body = body[:capacity]
	}
	out := make([]string, 0, rows)
	out = append(out, body...)
	for len(out) < capacity {
		out = append(out, blankRow(width))
	}
	out = append(out, footer...)
	for len(out) < rows {
		out = append(out, blankRow(width))
	}
	return out
}

func statusColour(status string) (text, border string) {
	switch status {
	case "complete":
		return styleGood, borderGood
	case "trapped", "waiting":
		return styleWarn, borderWarn
	case "running":
		return styleKey, borderRun
	default:
		return styleBad, borderBad
	}
}

func pick(cond bool, yes, no string) string {
	if cond {
		return yes
	}
	return no
}

func blankRow(width int) string {
	return strings.Repeat(" ", width)
}

func styledRow(text string, width int, style string) string {
	return fit(text, width, style)
}

func runeLen(text string) int { return utf8.RuneCountInString(text) }

func truncateRunes(text string, width int) string {
	if runeLen(text) <= width {
		return text
	}
	return string([]rune(text)[:width])
}

// lastLine is where a failing command puts its verdict.
func lastLine(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	return strings.TrimSpace(lines[len(lines)-1])
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
