package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"hamster/internal/store"
)

// The dashboard is three lazygit-style panels: the list of loops on the left,
// the selected loop's source top-right, its log bottom-right, and a key bar
// along the bottom. Colour carries meaning — green passed, red failed, amber
// waiting on a person, blue the active panel and the keys.
const (
	reset = "\x1b[0m"

	styleBold   = "\x1b[1m"
	styleDim    = "\x1b[2m"
	styleItalic = "\x1b[3m"
	styleFocus  = "\x1b[7m"

	styleGood = "\x1b[38;5;42m"
	styleBad  = "\x1b[38;5;203m"
	styleWarn = "\x1b[38;5;214m"
	styleKey  = "\x1b[38;5;39m"

	borderIdle = "\x1b[38;5;240m"
	borderRun  = "\x1b[38;5;39m"
)

type line struct {
	text  string
	style string
}

// render lays out the whole dashboard at the full terminal size. Every row it
// returns is exactly lay.left cells wide.
func (c *console) render(lay layout, quip int) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	width, height := lay.left, lay.rows
	if width < 8 || height < 4 {
		rows := make([]string, height)
		for i := range rows {
			rows[i] = fit("lazyloop", width, styleBold)
		}
		return rows
	}

	panelsH := height - 1
	leftW := width / 3
	if leftW < 24 {
		leftW = 24
	}
	if leftW > width-40 {
		leftW = width - 40
	}
	if leftW < 8 || leftW > width-8 {
		leftW = width / 2
	}
	rightW := width - leftW
	topH := panelsH / 2
	if topH < 3 {
		topH = 3
	}
	if topH > panelsH-3 {
		topH = panelsH - 3
	}
	botH := panelsH - topH

	left := c.renderList(leftW, panelsH)
	right := append(c.renderSource(rightW, topH), c.renderLogs(rightW, botH)...)

	rows := make([]string, 0, height)
	for i := 0; i < panelsH; i++ {
		l := blankRow(leftW)
		if i < len(left) {
			l = left[i]
		}
		r := blankRow(rightW)
		if i < len(right) {
			r = right[i]
		}
		rows = append(rows, l+r)
	}
	rows = append(rows, c.barRow(width))
	return rows
}

func (c *console) renderList(width, height int) []string {
	title := fmt.Sprintf("%s · %d loops", filepath.Base(c.dir), len(c.loops))
	border := borderFor(c.panel == focusList)
	if len(c.loops) == 0 {
		return scrollPanel(width, height, title, border, []line{
			{"no loops yet", styleDim},
			{"press n to start one", styleKey},
		}, 0)
	}
	inner := width - 4
	body := make([]line, 0, len(c.loops))
	targets := make([]target, 0, len(c.loops))
	selectedRow := 0
	for _, loop := range c.loops {
		marker, style := loopMarker(loop.Status)
		label := loop.Label
		if label == "" {
			label = "(loop)"
		}
		if loop.ID == c.selLoop && c.selAgent == -1 {
			style = styleFocus
			selectedRow = len(body)
		} else if loop.Traps > 0 {
			style = styleWarn
		}
		body = append(body, line{loopRow(marker, label, loopMeta(loop, c.now()), inner), style})
		targets = append(targets, target{loop.ID, -1})
		// Each loop's agents, indented beneath it.
		for index, agent := range loop.Agents {
			branch := "├"
			if index == len(loop.Agents)-1 {
				branch = "└"
			}
			name := "subagent " + shortAgent(agent.AgentID)
			if agent.Root {
				name = "root agent"
			}
			text := truncateRunes(fmt.Sprintf("  %s %s", branch, name), inner-runeLen(agent.Status)-1)
			gap := inner - runeLen(text) - runeLen(agent.Status)
			if gap < 1 {
				gap = 1
			}
			rowStyle := agentStyle(agent.Status)
			if loop.ID == c.selLoop && c.selAgent == index {
				rowStyle = styleFocus
				selectedRow = len(body)
			}
			body = append(body, line{text + strings.Repeat(" ", gap) + agent.Status, rowStyle})
			targets = append(targets, target{loop.ID, index})
		}
	}
	offset := windowOffset(selectedRow, len(body), height-2)
	// Record where each row landed so a mouse click can find it. Content
	// starts one row below the panel's top border.
	c.listTop = 1
	c.listOffset = offset
	c.listLeftW = width
	c.listTargets = targets
	return scrollPanel(width, height, title, border, body, offset)
}

func (c *console) renderSource(width, height int) []string {
	border := borderFor(c.panel == focusSource)
	if c.mode == modeBuild && c.recipe == 'm' {
		return c.renderChecks(width, height)
	}
	if c.mode == modeBuild {
		var body []line
		for _, raw := range strings.Split(strings.TrimRight(c.draftScript(), "\n"), "\n") {
			body = append(body, line{raw, ""})
		}
		if len(c.draft) == 0 {
			body = append(body, line{"", ""}, line{"add checks with the keys below", styleDim})
		}
		return scrollPanel(width, height, "building", borderRun, body, 0)
	}
	if c.detail == nil || strings.TrimSpace(c.detail.Source) == "" {
		return scrollPanel(width, height, "loop", border, []line{{"no source recorded for this loop", styleDim}}, 0)
	}
	var body []line
	for _, raw := range strings.Split(strings.TrimRight(c.detail.Source, "\n"), "\n") {
		style := ""
		if strings.HasPrefix(strings.TrimSpace(raw), "//") {
			style = styleDim
		}
		body = append(body, line{raw, style})
	}
	return scrollPanel(width, height, "loop", border, body, c.srcScroll)
}

// renderChecks is the machine-check picker: the commands this repo offers,
// with the highlighted one committed when the field is left empty.
func (c *console) renderChecks(width, height int) []string {
	inner := width - 4
	typing := strings.TrimSpace(string(c.line)) != ""
	if len(c.suggestions) == 0 {
		return scrollPanel(width, height, "checks", borderRun, []line{
			{"no checks detected here", styleDim},
			{"type a shell command below", styleKey},
		}, 0)
	}
	body := []line{{"checks in this repo — ↑↓ to pick, or type your own", styleDim}, {"", ""}}
	for index, check := range c.suggestions {
		marker := "  "
		style := ""
		if index == c.suggestHi {
			marker = "❯ "
			if !typing {
				style = styleFocus
			} else {
				style = styleKey
			}
		}
		text := truncateRunes(marker+check.Command, inner-runeLen(check.Source)-1)
		gap := inner - runeLen(text) - runeLen(check.Source)
		if gap < 1 {
			gap = 1
		}
		body = append(body, line{text + strings.Repeat(" ", gap) + check.Source, style})
	}
	offset := windowOffset(c.suggestHi+2, len(body), height-2)
	return scrollPanel(width, height, "checks", borderRun, body, offset)
}

func (c *console) renderLogs(width, height int) []string {
	border := borderFor(c.panel == focusLogs)
	if c.detail == nil || len(c.detail.Logs) == 0 {
		note := "no activity yet"
		if c.detail != nil && c.detail.Status == "armed" {
			note = "armed — starts with the agent's next message"
		}
		return scrollPanel(width, height, "logs", border, []line{{note, styleDim}}, 0)
	}
	body := make([]line, 0, len(c.detail.Logs))
	for _, log := range c.detail.Logs {
		body = append(body, line{log.Text, levelStyle(log.Level)})
	}
	offset := c.logScroll
	if !c.logUserScrolled {
		// Pin to the latest line until the person scrolls up.
		if maximum := len(body) - (height - 2); maximum > 0 {
			offset = maximum
		}
	}
	return scrollPanel(width, height, "logs", border, body, offset)
}

// barRow is the bottom line: the editor while typing, otherwise the last
// message or the keys.
func (c *console) barRow(width int) string {
	switch c.mode {
	case modeBuild:
		return c.buildBar(width)
	case modeNote:
		return editorBar(width, "why reject?", c.line)
	}
	if c.pendingRemove != "" {
		return fit("  "+firstLine(c.message), width, styleBad)
	}
	if trap := c.openTrap(); trap != nil {
		answer := "a approve · r reject"
		if trap.Kind == store.TrapSelection {
			answer = "press the number"
		}
		return fit("  ! "+firstLine(trap.Prompt)+"   "+answer, width, styleWarn)
	}
	if !c.hooks.Installed {
		return fit("  hooks not installed — press s to govern this workspace", width, styleWarn)
	}
	if c.message != "" {
		return fit("  "+firstLine(c.message), width, styleItalic)
	}
	hints := "n new · d stop · x remove · ↑↓ move · tab panel · ctrl-] open · q quit"
	return fit("  "+hints, width, styleDim)
}

// buildBar shows the recipe legend while composing, or the current field's
// prompt while a recipe is waiting for its argument.
func (c *console) buildBar(width int) string {
	if c.recipe != 0 {
		return editorBar(width, recipePrompt(c.recipe), c.line)
	}
	legend := "l imit · a ttempts · m achine · j udge · t rap · ⏎ start · ⌫ undo · esc cancel"
	if c.message != "" {
		legend = firstLine(c.message) + "   " + legend
	}
	return fit("  "+legend, width, styleKey)
}

func editorBar(width int, label string, text []rune) string {
	prefix := "  " + label + " › "
	room := width - utf8.RuneCountInString(prefix) - 1
	if room < 1 {
		room = 1
	}
	shown := string(text) + "█"
	if utf8.RuneCountInString(shown) > room {
		runes := []rune(shown)
		shown = string(runes[len(runes)-room:])
	}
	return fit(styleKey+"  "+label+" › "+reset+shown, width, "")
}

// scrollPanel draws a rounded box of exactly height rows, showing the slice of
// body starting at offset. The title carries a position indicator when the
// content does not fit.
func scrollPanel(width, height int, title, border string, body []line, offset int) []string {
	if width < 4 || height < 2 {
		return nil
	}
	inner := width - 4
	viewport := height - 2
	if viewport < 1 {
		viewport = 1
	}
	maxOffset := len(body) - viewport
	if maxOffset < 0 {
		maxOffset = 0
	}
	if offset > maxOffset {
		offset = maxOffset
	}
	if offset < 0 {
		offset = 0
	}

	label := "─ " + title + " "
	if len(body) > viewport {
		label = fmt.Sprintf("─ %s %d-%d/%d ", title, offset+1, offset+viewport, len(body))
	}
	if runeLen(label) > width-3 {
		label = truncateRunes(label, width-3)
	}
	rows := make([]string, 0, height)
	rows = append(rows, border+"╭"+label+strings.Repeat("─", width-2-runeLen(label))+"╮"+reset)
	for i := 0; i < viewport; i++ {
		content := fit("", inner, "")
		if idx := offset + i; idx < len(body) {
			content = fit(body[idx].text, inner, body[idx].style)
		}
		rows = append(rows, border+"│"+reset+" "+content+" "+border+"│"+reset)
	}
	rows = append(rows, border+"╰"+strings.Repeat("─", width-2)+"╯"+reset)
	return rows
}

// windowOffset keeps the selected index inside a viewport of the given size.
func windowOffset(selected, total, viewport int) int {
	if viewport < 1 || total <= viewport {
		return 0
	}
	offset := selected - viewport/2
	if offset < 0 {
		offset = 0
	}
	if offset > total-viewport {
		offset = total - viewport
	}
	return offset
}

func borderFor(active bool) string {
	if active {
		return borderRun
	}
	return borderIdle
}

// loopRow composes a list row of exactly width cells: the marker and label on
// the left, the meta (tool calls and elapsed time) right-aligned.
func loopRow(marker, label, meta string, width int) string {
	left := marker + " " + label
	if meta == "" {
		return truncateRunes(left, width)
	}
	room := width - runeLen(meta) - 1
	if room < 1 {
		room = 1
	}
	left = truncateRunes(left, room)
	gap := width - runeLen(left) - runeLen(meta)
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + meta
}

// loopMeta is the live cost of a loop: how many tool calls it has made and how
// long it has been running. A finished loop's clock is stopped; a running one
// counts against now, so the list ticks up as you watch.
func loopMeta(loop store.LoopSummary, now time.Time) string {
	if loop.Started == nil {
		return ""
	}
	end := now
	if loop.Ended != nil {
		end = *loop.Ended
	}
	elapsed := end.Sub(*loop.Started)
	if elapsed < 0 {
		elapsed = 0
	}
	if loop.ToolCalls > 0 {
		return fmt.Sprintf("%dc %s", loop.ToolCalls, clockDuration(elapsed))
	}
	return clockDuration(elapsed)
}

// clockDuration formats an elapsed time compactly: m:ss under an hour, h:mm
// over it.
func clockDuration(d time.Duration) string {
	seconds := int(d.Seconds())
	if seconds < 3600 {
		return fmt.Sprintf("%d:%02d", seconds/60, seconds%60)
	}
	return fmt.Sprintf("%dh%02dm", seconds/3600, (seconds%3600)/60)
}

func shortAgent(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// agentStyle dims the branch while colouring the status by how the agent's run
// is doing.
func agentStyle(status string) string {
	switch status {
	case "complete":
		return styleGood
	case "running":
		return styleDim
	case "trapped", "waiting":
		return styleWarn
	case "idle":
		return styleDim
	}
	return styleBad
}

func loopMarker(status string) (glyph, style string) {
	switch status {
	case "complete":
		return "●", styleGood
	case "running":
		return "●", styleKey
	case "armed":
		return "○", styleWarn
	case "trapped", "waiting":
		return "●", styleWarn
	default:
		return "●", styleBad
	}
}

func levelStyle(level string) string {
	switch level {
	case "good":
		return styleGood
	case "bad":
		return styleBad
	case "warn":
		return styleWarn
	case "dim":
		return styleDim
	}
	return ""
}

func blankRow(width int) string { return strings.Repeat(" ", width) }

func runeLen(text string) int { return utf8.RuneCountInString(text) }

func truncateRunes(text string, width int) string {
	if runeLen(text) <= width {
		return text
	}
	return string([]rune(text)[:width])
}
