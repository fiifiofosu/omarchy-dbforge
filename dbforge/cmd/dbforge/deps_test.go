package main

import (
	"os/exec"
	"strings"
	"testing"
)

// The CLI must not link the TUI.
//
// Bubble Tea's package init calls lipgloss.HasDarkBackground(), which queries
// the terminal and blocks for termenv's 5-second OSCTimeout when the terminal
// does not answer. Because that runs at import time, simply linking the TUI
// into this binary made `dbctl list` take 5.03s on such a terminal instead of
// 0.05s. The TUI therefore lives in cmd/dbforge-tui, and this test keeps it
// there.
func TestCLIDoesNotLinkTheTUI(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Skipf("cannot run go list: %v", err)
	}
	for _, banned := range []string{
		"github.com/charmbracelet/bubbletea",
		"github.com/fiifiofosu/dbforge/internal/tui",
	} {
		if strings.Contains(string(out), banned) {
			t.Errorf("cmd/dbforge depends on %s; that costs every CLI invocation "+
				"a terminal query at startup. Keep the TUI in cmd/dbforge-tui.", banned)
		}
	}
}
