package daemon

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"time"
)

func Ensure(ctx context.Context, paths Paths, executable, home string) error {
	if connection, err := net.DialTimeout("unix", paths.Socket, 50*time.Millisecond); err == nil {
		connection.Close()
		return nil
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
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(3 * time.Second)
	defer timeout.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeout.C:
			return fmt.Errorf("Hamster daemon did not start")
		case <-ticker.C:
			connection, err := net.DialTimeout("unix", paths.Socket, 50*time.Millisecond)
			if err == nil {
				connection.Close()
				return nil
			}
		}
	}
}
