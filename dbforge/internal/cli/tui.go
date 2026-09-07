package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// tuiBinary is the companion binary holding the terminal interface.
const tuiBinary = "dbforge-tui"

// runTUI hands off to the TUI binary.
//
// The TUI lives in its own executable because Bubble Tea's package init
// queries the terminal and can block for five seconds; linking it into this
// binary would slow down every CLI invocation, whether or not the TUI is used.
func runTUI() error {
	path, err := findTUI()
	if err != nil {
		return err
	}

	cmd := exec.Command(path)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = os.Environ()

	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			// The TUI already printed whatever went wrong.
			os.Exit(exitErr.ExitCode())
		}
		return fmt.Errorf("running %s: %w", tuiBinary, err)
	}
	return nil
}

// findTUI looks beside this binary first, so a build tree and an installed
// copy each find their own companion rather than whichever is on PATH.
func findTUI() (string, error) {
	if p := os.Getenv("DBFORGE_TUI_BIN"); p != "" {
		return p, nil
	}
	if self, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(self); err == nil {
			self = resolved
		}
		candidate := filepath.Join(filepath.Dir(self), tuiBinary)
		if fi, err := os.Stat(candidate); err == nil && !fi.IsDir() {
			return candidate, nil
		}
	}
	if p, err := exec.LookPath(tuiBinary); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("%s not found; install it alongside dbctl (make install)", tuiBinary)
}
