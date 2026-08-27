package daemon

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Identity is what a daemon was started from. It is reported on ping so a
// caller can tell whether the daemon answering it is older than itself.
type Identity struct {
	Executable string    `json:"executable"`
	Modified   time.Time `json:"modified"`
	PID        int       `json:"pid"`
}

// CurrentIdentity describes the running executable.
func CurrentIdentity() Identity {
	identity := Identity{PID: os.Getpid()}
	executable, err := os.Executable()
	if err != nil {
		return identity
	}
	identity.Executable = executable
	if info, err := os.Stat(executable); err == nil {
		identity.Modified = info.ModTime()
	}
	return identity
}

// Ensure makes sure a daemon at least as new as this executable is answering
// on the socket. A daemon left over from an earlier build would keep
// answering hooks with the old code — every rebuild during development used
// to leave one behind — so a daemon built strictly before the caller is
// replaced. Strictly: two binaries built together from the same source never
// take turns replacing each other.
func Ensure(ctx context.Context, paths Paths, executable, home string) error {
	if running, ok := ping(ctx, paths.Socket); ok {
		if !shouldReplace(CurrentIdentity(), running) {
			return nil
		}
		if err := retire(ctx, paths, home, running); err != nil {
			return err
		}
	}
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer null.Close()
	command := exec.Command(executable, "daemon", "serve", "--home", home)
	command.Stdin, command.Stdout, command.Stderr = null, null, null
	if err := command.Start(); err != nil {
		return fmt.Errorf("start Hamster daemon: %w", err)
	}
	_ = command.Process.Release()
	return waitForSocket(ctx, paths.Socket, true)
}

// shouldReplace is true when the caller is running newer code than the
// daemon. A daemon that reports no identity predates identities themselves,
// so any caller that can ask the question is newer than it — this is the
// one-time upgrade from a pre-identity daemon, and it must be retired or the
// old code keeps answering hooks forever. Among identified daemons, the
// caller wins only when its build is newer by more than timestamp jitter, so
// two binaries built together never take turns.
func shouldReplace(caller, running Identity) bool {
	if caller.Modified.IsZero() {
		return false
	}
	if running.Modified.IsZero() {
		return true
	}
	return caller.Modified.After(running.Modified.Add(time.Second))
}

func ping(ctx context.Context, socket string) (Identity, bool) {
	connection, err := net.DialTimeout("unix", socket, 50*time.Millisecond)
	if err != nil {
		return Identity{}, false
	}
	connection.Close()
	var result struct {
		Identity Identity `json:"identity"`
	}
	pingContext, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err := (Client{SocketPath: socket}).Call(pingContext, "ensure-ping", "ping", nil, &result); err != nil {
		// Something answers but does not speak the protocol we expect;
		// treat it as present and unknown.
		return Identity{}, true
	}
	return result.Identity, true
}

// retire asks an older daemon to stop and waits for its socket to go away.
// A daemon that reported its PID is signalled directly; a pre-identity daemon
// gave none, so it is found by its serve process for this home. Its state is
// in SQLite; only in-memory observations are lost.
func retire(ctx context.Context, paths Paths, home string, running Identity) error {
	pid := running.PID
	if pid == 0 {
		pid = findServeProcess(home)
	}
	if pid == 0 {
		return fmt.Errorf("an older Hamster daemon is answering but could not be located; stop it and try again")
	}
	if process, err := os.FindProcess(pid); err == nil {
		// Already gone, or not ours to signal; either way the socket check
		// below is what decides.
		_ = process.Signal(syscall.SIGTERM)
	}
	return waitForSocket(ctx, paths.Socket, false)
}

// findServeProcess locates a `daemon serve --home <home>` process, which is
// how a pre-identity daemon is retired: it answers ping but names no PID.
func findServeProcess(home string) int {
	self := os.Getpid()
	output, err := exec.Command("pgrep", "-f", "daemon serve").Output()
	if err != nil {
		return 0
	}
	for _, field := range strings.Fields(string(output)) {
		pid, err := strconv.Atoi(field)
		if err != nil || pid == self {
			continue
		}
		if commandLineMatchesHome(pid, home) {
			return pid
		}
	}
	return 0
}

func commandLineMatchesHome(pid int, home string) bool {
	output, err := exec.Command("ps", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return false
	}
	line := string(output)
	if !strings.Contains(line, "daemon serve") {
		return false
	}
	// A default-home daemon carries no --home flag; match it too.
	return strings.Contains(line, "--home "+home) || !strings.Contains(line, "--home ")
}

func waitForSocket(ctx context.Context, socket string, want bool) error {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(3 * time.Second)
	defer timeout.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeout.C:
			if want {
				return fmt.Errorf("Hamster daemon did not start")
			}
			return fmt.Errorf("older Hamster daemon did not stop; stop it and try again")
		case <-ticker.C:
			connection, err := net.DialTimeout("unix", socket, 50*time.Millisecond)
			if err == nil {
				connection.Close()
			}
			if (err == nil) == want {
				return nil
			}
		}
	}
}
