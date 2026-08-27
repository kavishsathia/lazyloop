package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"

	"github.com/creack/pty"
	"github.com/vito/midterm"

	"hamster/internal/hook"
)

// An agent is one Codex process, spawned for one loop. Each loop gets its own
// fresh session and context; several run at once, each on its own pty and
// terminal emulator. Only the one on screen is drawn, but all of them read
// their output continuously so switching to any is instant.
type agent struct {
	token  string
	loopID string

	cmd *exec.Cmd
	pty *os.File
	emu *midterm.Terminal
	fwd *modeForwarder

	writeMu sync.Mutex
	dead    atomic.Bool
	first   chan struct{} // closed on the first byte of output
	firstMu sync.Once
}

func (a *agent) write(b []byte) error {
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	_, err := a.pty.Write(b)
	return err
}

// fleet owns every agent. It spawns them on demand, routes output and input,
// and draws whichever loop is selected.
type fleet struct {
	ctx    context.Context
	argv   []string
	dir    string
	stdout *os.File
	onData func() // mark the screen dirty

	mu      sync.Mutex
	rows    int
	cols    int
	byToken map[string]*agent
	byLoop  map[string]*agent
	active  string // loopID whose mode changes reach the real terminal
}

func newFleet(ctx context.Context, argv []string, dir string, stdout *os.File, rows, cols int, onData func()) *fleet {
	return &fleet{
		ctx: ctx, argv: argv, dir: dir, stdout: stdout, onData: onData,
		rows: rows, cols: cols, byToken: map[string]*agent{}, byLoop: map[string]*agent{},
	}
}

// spawn starts a Codex process carrying the given console token and begins
// draining its output into a fresh emulator.
func (f *fleet) spawn(token string) (*agent, error) {
	f.mu.Lock()
	rows, cols := f.rows, f.cols
	f.mu.Unlock()

	command := exec.CommandContext(f.ctx, f.argv[0], f.argv[1:]...)
	command.Dir = f.dir
	command.Env = append(os.Environ(), hook.ConsoleEnvironment+"="+token)
	ptmx, err := pty.StartWithSize(command, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
	if err != nil {
		return nil, fmt.Errorf("start %s: %w", f.argv[0], err)
	}
	ag := &agent{token: token, cmd: command, pty: ptmx, emu: midterm.NewTerminal(rows, cols), first: make(chan struct{})}
	ag.emu.ForwardResponses = ptmx
	ag.fwd = &modeForwarder{w: f.stdout, set: map[int]bool{}, active: func() bool { return f.isActive(ag) }}
	ag.emu.ForwardRequests = ag.fwd

	f.mu.Lock()
	f.byToken[token] = ag
	f.mu.Unlock()

	go func() {
		buffer := make([]byte, 32*1024)
		for {
			n, err := ptmx.Read(buffer)
			if n > 0 {
				_, _ = ag.emu.Write(buffer[:n])
				ag.firstMu.Do(func() { close(ag.first) })
				f.onData()
			}
			if err != nil {
				ag.dead.Store(true)
				f.onData()
				return
			}
		}
	}()
	return ag, nil
}

// bind indexes an agent by the loop it is running, once the daemon has
// assigned the loop its id.
func (f *fleet) bind(token, loopID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ag, ok := f.byToken[token]; ok {
		ag.loopID = loopID
		f.byLoop[loopID] = ag
	}
}

func (f *fleet) get(loopID string) *agent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.byLoop[loopID]
}

func (f *fleet) isActive(ag *agent) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return ag.loopID != "" && ag.loopID == f.active
}

// setActive chooses which loop's Codex the terminal belongs to: it turns off
// the previous agent's terminal modes and applies the new one's, so bracketed
// paste and focus reporting follow the agent on screen.
func (f *fleet) setActive(loopID string) {
	f.mu.Lock()
	if loopID == f.active {
		f.mu.Unlock()
		return
	}
	previous := f.byLoop[f.active]
	next := f.byLoop[loopID]
	f.active = loopID
	f.mu.Unlock()
	if previous != nil {
		previous.fwd.reset()
	}
	if next != nil {
		next.fwd.reapply()
	}
}

func (f *fleet) resize(rows, cols int) {
	f.mu.Lock()
	f.rows, f.cols = rows, cols
	agents := make([]*agent, 0, len(f.byToken))
	for _, ag := range f.byToken {
		agents = append(agents, ag)
	}
	f.mu.Unlock()
	for _, ag := range agents {
		_ = pty.Setsize(ag.pty, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
		ag.emu.Resize(rows, cols)
	}
}

func (f *fleet) killAll() {
	f.mu.Lock()
	agents := make([]*agent, 0, len(f.byToken))
	for _, ag := range f.byToken {
		agents = append(agents, ag)
	}
	f.mu.Unlock()
	for _, ag := range agents {
		if ag.cmd.Process != nil {
			_ = ag.cmd.Process.Kill()
		}
	}
}

func (f *fleet) kill(loopID string) {
	if ag := f.get(loopID); ag != nil && ag.cmd.Process != nil {
		_ = ag.cmd.Process.Kill()
	}
}
