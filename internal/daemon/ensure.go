package daemon

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
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
		if err := retire(ctx, paths.Socket, running); err != nil {
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

// shouldReplace is true when the caller's build is newer than the daemon's by
// more than filesystem timestamp jitter. A daemon that does not report an
// identity is too old to be asked to leave, and is left alone.
func shouldReplace(caller, running Identity) bool {
	if running.PID == 0 || running.Modified.IsZero() || caller.Modified.IsZero() {
		return false
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
// Its state is in SQLite; only in-memory observations are lost.
func retire(ctx context.Context, socket string, running Identity) error {
	process, err := os.FindProcess(running.PID)
	if err != nil {
		return err
	}
	if err := process.Signal(syscall.SIGTERM); err != nil {
		// Already gone, or not ours to signal; either way the socket check
		// below decides.
		_ = err
	}
	return waitForSocket(ctx, socket, false)
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
