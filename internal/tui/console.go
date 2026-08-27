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
	modeKeys  mode = iota
	modeBuild      // composing a loop from recipes
	modeNote       // typing the reasoning behind a rejection
)

// A clause is one recipe the person added while composing a loop. The kind is
// the recipe letter; arg is what they typed for it.
type clause struct {
	kind byte
	arg  string
}

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

	draft  []clause
	recipe byte // the recipe awaiting its argument, or 0 in the menu

	panel           panelFocus
	loops           []store.LoopSummary
	selLoop         string
	selAgent        int // -1 on a loop row, >=0 on that loop's agent
	detail          *store.LoopDetail
	srcScroll       int
	logScroll       int
	logUserScrolled bool
	lastTraps       int

	// Captured on the last render so a mouse click can find the row it hit.
	listTop     int
	listOffset  int
	listLeftW   int
	listTargets []target

	dirty func()
	// Callbacks the runtime supplies.
	bell      func()                           // ring the terminal on a new escalation
	quit      func()                           // end the whole session
	startLoop func(ir.Machine, string, string) // spawn a Codex for a newly armed loop
	killAgent func(string)                     // kill the Codex running a loop
}

func newConsole(backend Backend, dir string, hooks Hooks, dirty func()) *console {
	return &console{backend: backend, dir: dir, hooks: hooks, now: time.Now, dirty: dirty, screen: screenDashboard, selAgent: -1}
}

// A target is a selectable row in the list: a loop, or one of its agents.
type target struct {
	loop  string
	agent int // -1 for the loop's own row
}

// targets is the flat, navigable list of rows: each loop followed by its
// agents.
func (c *console) targets() []target {
	var t []target
	for _, loop := range c.loops {
		t = append(t, target{loop.ID, -1})
		for index := range loop.Agents {
			t = append(t, target{loop.ID, index})
		}
	}
	return t
}

func (c *console) targetIndex() int {
	for index, tgt := range c.targets() {
		if tgt.loop == c.selLoop && tgt.agent == c.selAgent {
			return index
		}
	}
	return 0
}

// onAgentRow reports whether the cursor is on an agent, not a loop. ctrl-]
// opens an agent's Codex, so it is only allowed here.
func (c *console) onAgentRow() bool {
	return c.selAgent >= 0
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
			if col, row, button, press, end, ok := parseMouse(input, index); ok {
				c.mouse(col, row, button, press)
				index = end
				continue
			}
			if c.mode == modeKeys && index+2 < len(input) && input[index+1] == '[' {
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

// parseMouse decodes an SGR mouse report (\x1b[<b;x;y M/m) starting at index.
// It returns the 1-based column and row, the button code, whether it was a
// press, and the index of the final byte.
func parseMouse(input []byte, index int) (col, row, button int, press bool, end int, ok bool) {
	if index+3 >= len(input) || input[index+1] != '[' || input[index+2] != '<' {
		return 0, 0, 0, false, 0, false
	}
	nums := []int{0}
	for i := index + 3; i < len(input); i++ {
		switch b := input[i]; {
		case b >= '0' && b <= '9':
			nums[len(nums)-1] = nums[len(nums)-1]*10 + int(b-'0')
		case b == ';':
			nums = append(nums, 0)
		case b == 'M' || b == 'm':
			if len(nums) != 3 {
				return 0, 0, 0, false, 0, false
			}
			return nums[1], nums[2], nums[0], b == 'M', i, true
		default:
			return 0, 0, 0, false, 0, false
		}
	}
	return 0, 0, 0, false, 0, false
}

// mouse acts on a click or scroll in the dashboard.
func (c *console) mouse(col, row, button int, press bool) {
	switch button {
	case 64: // wheel up
		c.nav(-1)
		return
	case 65: // wheel down
		c.nav(1)
		return
	}
	if !press || button&3 != 0 { // only a left-button press selects
		return
	}
	if col < 1 || col > c.listLeftW {
		return
	}
	// The list panel's content starts on screen row 2 (row 1 is its border).
	bodyIndex := c.listOffset + (row - 2)
	if bodyIndex < 0 || bodyIndex >= len(c.listTargets) {
		return
	}
	c.panel = focusList
	c.setTarget(c.listTargets[bodyIndex])
	c.dirty()
}

func (c *console) escape() {
	switch {
	case c.mode == modeBuild && c.recipe != 0:
		c.recipe, c.line = 0, nil // cancel the field, stay in the builder
	case c.mode != modeKeys:
		c.mode, c.line, c.recipe, c.draft = modeKeys, nil, 0, nil
		c.message = ""
	default:
		c.screen = screenAgent
	}
	c.armKill = false
	c.dirty()
}

func (c *console) key(r rune) {
	defer c.dirty()
	if c.mode == modeBuild {
		c.build(r)
		return
	}
	if c.mode != modeKeys {
		c.edit(r)
		return
	}
	if r != 'd' {
		c.armKill = false
	}
	switch r {
	case prefixKey:
		if c.onAgentRow() {
			c.screen = screenAgent
		} else {
			c.message = "select an agent (a row under a loop) to open it"
		}
	case 'q':
		c.requestQuit()
	case '\t':
		c.cyclePanel(1)
	case 'j':
		c.nav(1)
	case 'k':
		c.nav(-1)
	case 'n':
		c.mode, c.line, c.recipe, c.draft, c.message = modeBuild, nil, 0, nil, ""
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
		c.mode, c.line = modeKeys, nil
		if text == "" {
			c.message = "a rejection needs a reason; the agent re-derives the work from it"
			return
		}
		c.answer(store.TrapReject, text)
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

// build handles a keystroke while composing a loop. In the menu a recipe
// letter opens a field; while a field is open every printable key is its
// argument, Enter commits the clause, and Escape cancels just the field.
func (c *console) build(r rune) {
	if c.recipe != 0 {
		switch r {
		case '\r', '\n':
			c.commitClause()
		case 0x7f, 0x08:
			if len(c.line) > 0 {
				c.line = c.line[:len(c.line)-1]
			}
		case 0x15:
			c.line = nil
		default:
			if r >= 0x20 && r < 0x7f {
				c.line = append(c.line, r)
			}
		}
		return
	}
	switch r {
	case 'l', 'a', 'm', 'j', 't':
		c.recipe, c.line, c.message = byte(r), nil, ""
	case '\r', '\n':
		c.startDraft()
	case 0x7f, 0x08: // undo the last clause
		if len(c.draft) > 0 {
			c.draft = c.draft[:len(c.draft)-1]
		}
	}
}

// commitClause validates a field and appends it to the draft.
func (c *console) commitClause() {
	arg := strings.TrimSpace(string(c.line))
	if c.recipe == 'l' || c.recipe == 'a' {
		if n, err := strconv.Atoi(arg); err != nil || n <= 0 {
			c.message = "type a positive number"
			return
		}
	} else if arg == "" {
		c.message = "type something for this check"
		return
	}
	c.draft = append(c.draft, clause{kind: c.recipe, arg: arg})
	c.recipe, c.line, c.message = 0, nil, ""
}

// startDraft compiles the composed loop and hands it to the runtime.
func (c *console) startDraft() {
	if !c.draftHasGate() {
		c.message = "add a check (m, j, or t) before starting"
		return
	}
	source := c.draftScript()
	machine, _, err := CompileRequest(source)
	if err != nil {
		c.message = "loop: " + firstLine(err.Error())
		return
	}
	if c.startLoop == nil {
		c.message = "cannot start a loop in this mode"
		return
	}
	label := c.draftLabel()
	c.mode, c.line, c.recipe, c.draft = modeKeys, nil, 0, nil
	c.message = "starting a Codex for this loop…"
	go c.startLoop(machine, source, label)
}

func (c *console) draftHasGate() bool {
	for _, cl := range c.draft {
		if cl.kind == 'm' || cl.kind == 'j' || cl.kind == 't' {
			return true
		}
	}
	return false
}

// draftScript renders the composed clauses as a goal. Limits come first, then
// the gates, then a return of the first machine check (or true).
func (c *console) draftScript() string {
	var limits, gates []string
	firstVerify, verifyN := "", 0
	for _, cl := range c.draft {
		switch cl.kind {
		case 'l':
			limits = append(limits, fmt.Sprintf("  limit time %sm;", cl.arg))
		case 'a':
			limits = append(limits, fmt.Sprintf("  limit attempts %s;", cl.arg))
		case 'm':
			verifyN++
			name := fmt.Sprintf("check%d", verifyN)
			if firstVerify == "" {
				firstVerify = name
			}
			gates = append(gates, fmt.Sprintf("  verify %s = bash.check(%s);", name, quote(cl.arg)))
		case 'j':
			gates = append(gates, fmt.Sprintf("  judge %s;", quote(cl.arg)))
		case 't':
			gates = append(gates, fmt.Sprintf("  trap %s;", quote(cl.arg)))
		}
	}
	ret := firstVerify
	if ret == "" {
		ret = "true"
	}
	var b strings.Builder
	b.WriteString("goal {\n")
	for _, l := range limits {
		b.WriteString(l + "\n")
	}
	for _, l := range gates {
		b.WriteString(l + "\n")
	}
	b.WriteString("  return " + ret + ";\n}\n")
	return b.String()
}

func (c *console) draftLabel() string {
	for _, cl := range c.draft {
		if cl.kind == 'm' {
			return cl.arg
		}
	}
	for _, cl := range c.draft {
		if cl.kind == 'j' || cl.kind == 't' {
			return cl.arg
		}
	}
	return "loop"
}

// recipePrompt is what the field asks for, and how the legend reads.
func recipePrompt(recipe byte) string {
	switch recipe {
	case 'l':
		return "minutes"
	case 'a':
		return "attempts"
	case 'm':
		return "shell command to pass"
	case 'j':
		return "what should the reviewer check"
	case 't':
		return "question for the human"
	}
	return ""
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
	all := c.targets()
	if len(all) == 0 {
		return
	}
	index := c.targetIndex() + delta
	if index < 0 {
		index = 0
	}
	if index >= len(all) {
		index = len(all) - 1
	}
	c.setTarget(all[index])
}

func (c *console) setTarget(tgt target) {
	if tgt.loop != c.selLoop {
		c.srcScroll, c.logScroll, c.logUserScrolled = 0, 0, false
		c.detail = nil
	}
	c.selLoop, c.selAgent = tgt.loop, tgt.agent
}

func (c *console) selectedSummary() (store.LoopSummary, bool) {
	for _, loop := range c.loops {
		if loop.ID == c.selLoop {
			return loop, true
		}
	}
	return store.LoopSummary{}, false
}

// selected reports the loop the agent screen should show.
func (c *console) selected() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.selLoop
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
	c.selLoop, c.selAgent = id, -1
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
	if c.selLoop == "" || !c.hasLoop(c.selLoop) {
		if len(loops) > 0 {
			c.selLoop = loops[0].ID
		} else {
			c.selLoop = ""
		}
		c.selAgent = -1
		c.srcScroll, c.logScroll, c.logUserScrolled = 0, 0, false
	}
	// An agent row can vanish when a loop's agent count shrinks.
	if summary, ok := c.selectedSummary(); ok && c.selAgent >= len(summary.Agents) {
		c.selAgent = -1
	}
	selID := c.selLoop
	c.mu.Unlock()

	var detail *store.LoopDetail
	if selID != "" {
		detail, _ = c.backend.LoadLoop(ctx, selID)
	}
	c.mu.Lock()
	if selID == c.selLoop {
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
