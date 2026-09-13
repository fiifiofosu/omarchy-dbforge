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
	"context"
	"fmt"
	"os"
	"time"

	"github.com/fiifiofosu/dbforge/internal/cli"
	"github.com/fiifiofosu/dbforge/internal/tui"
)

// version is stamped at build time: -ldflags "-X main.version=..."
var version = "dev"

func main() {
	// So the TUI can tell the user when it is older than the daemon, which is
	// what a window left open across an upgrade looks like.
	tui.Version = version

	socket := os.Getenv("DBFORGE_SOCKET")

	// Quitting DBForge stops the daemon along with the databases, so launching
	// it has to bring the daemon back -- otherwise the second launch of the
	// application finds nothing running. A daemon that is already up costs one
	// connect here.
	//
	// A failure is not fatal: the list screen reports an unreachable daemon
	// clearly, and that is a better place to see the problem than a message
	// printed before the terminal is even set up.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	if err := cli.EnsureDaemon(ctx, socket); err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v\n", err)
	}
	cancel()

	if err := tui.Run(cli.NewClient(socket)); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
