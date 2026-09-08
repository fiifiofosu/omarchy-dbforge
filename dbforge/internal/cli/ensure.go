package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/fiifiofosu/dbforge/internal/daemon"
)

// EnsureDaemon starts dbforged if it is not already listening, and waits for
// it to answer.
//
// Quitting DBForge now stops the daemon along with the databases, so launching
// the app has to be able to bring it back -- otherwise the second launch finds
// nothing running and the application appears broken. Starting it through
// systemd where that is available keeps one supervisor in charge: two daemons
// racing for the same socket is a worse failure than none.
//
// A daemon that is already up makes this a single connect and nothing else.
func EnsureDaemon(ctx context.Context, socket string) error {
	if socket == "" {
		socket = daemon.SocketPath()
	}
	if daemonListening(socket) {
		return nil
	}

	started, err := startDaemon(ctx)
	if err != nil {
		return err
	}

	// Poll rather than sleep a fixed time: a daemon that is already warm
	// answers in milliseconds, and one that has to reconcile a dozen
	// containers can take a few seconds.
	deadline := time.Now().Add(10 * time.Second)
	for {
		if daemonListening(socket) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("started dbforged with %s but it is not listening on %s",
				started, socket)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// daemonListening reports whether something accepts connections on the socket.
// A stale socket file left by a killed daemon refuses the connect, which is
// exactly the answer we want.
func daemonListening(socket string) bool {
	c, err := net.DialTimeout("unix", socket, 300*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// startDaemon launches dbforged and reports how it did so.
func startDaemon(ctx context.Context) (string, error) {
	if _, err := exec.LookPath("systemctl"); err == nil {
		cmd := exec.CommandContext(ctx, "systemctl", "--user", "start", "dbforged")
		out, err := cmd.CombinedOutput()
		switch {
		case err == nil:
			return "systemctl --user start dbforged", nil
		case !unitUnknown(out):
			return "", fmt.Errorf("systemctl --user start dbforged: %w: %s", err, out)
		}
		// No such unit: this is a source install with no service file. Fall
		// through and run the binary directly rather than failing.
	}

	path, err := findDaemon()
	if err != nil {
		return "", err
	}
	// Detached on purpose: the daemon must outlive the process that started
	// it, which is usually a TUI the user will quit.
	cmd := exec.Command(path)
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	// A new session, so the daemon is not killed with the terminal that
	// launched the TUI.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("starting %s: %w", path, err)
	}
	// Reaped by init once it outlives us; not waiting on it is the point.
	go func() { _ = cmd.Wait() }()
	return path, nil
}

// unitUnknown reports whether systemctl failed only because dbforged is not
// installed as a unit.
func unitUnknown(out []byte) bool {
	s := string(out)
	for _, marker := range []string{"not found", "not-found", "No such file"} {
		if strings.Contains(s, marker) {
			return true
		}
	}
	return false
}

// findDaemon locates the dbforged binary: next to this executable first, since
// that is the copy that belongs to this installation, then $PATH.
func findDaemon() (string, error) {
	if self, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(self), "dbforged")
		if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
			return candidate, nil
		}
	}
	path, err := exec.LookPath("dbforged")
	if err != nil {
		return "", errors.New("cannot find dbforged: install DBForge, " +
			"or start the daemon yourself with `dbforged`")
	}
	return path, nil
}
