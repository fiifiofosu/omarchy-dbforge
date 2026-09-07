// Command dbforge-tui is DBForge's terminal interface.
//
// It is a separate binary from dbforge on purpose. Bubble Tea's package init
// calls lipgloss.HasDarkBackground(), which queries the terminal and blocks
// for termenv's 5-second OSCTimeout when the terminal does not answer. Because
// that happens at import time, merely linking the TUI into the CLI made every
// `dbctl list` take five seconds on such a terminal. Keeping it in its own
// binary means the CLI never pays for a dependency it is not using.
package main

import (
	"fmt"
	"os"

	"github.com/fiifiofosu/dbforge/internal/cli"
	"github.com/fiifiofosu/dbforge/internal/tui"
)

// version is stamped at build time: -ldflags "-X main.version=..."
var version = "dev"

func main() {
	// So the TUI can tell the user when it is older than the daemon, which is
	// what a window left open across an upgrade looks like.
	tui.Version = version

	if err := tui.Run(cli.NewClient(os.Getenv("DBFORGE_SOCKET"))); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
