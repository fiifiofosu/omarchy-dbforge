package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/fiifiofosu/dbforge/internal/cli"
	"github.com/fiifiofosu/dbforge/internal/model"
)

// Quitting DBForge shuts the whole application down: every running database
// and the daemon behind them. Nothing of DBForge is left running afterwards,
// which is the point -- but it also means quitting is no longer the free
// action closing a window usually is, so it asks first and says exactly what
// it is about to stop.
//
// What was running is remembered, and comes back on the next launch. That is
// the difference between this and stopping each instance by hand.

type shutdownState struct {
	// running is what will be stopped, captured when q was pressed.
	running []string
	working bool
	err     error
}

// shutdownDoneMsg carries the daemon's report, or the failure to get one.
type shutdownDoneMsg struct {
	report cli.SuspendReport
	err    error
}

func (m Model) runningIDs() []string {
	var out []string
	for _, i := range m.instances {
		if i.Status == model.StatusRunning {
			out = append(out, i.ID)
		}
	}
	return out
}

func (m Model) updateQuit(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.shutdown == nil {
		m.view = viewList
		return m, nil
	}
	if m.shutdown.working {
		// Mid-shutdown: the databases are being stopped and interrupting that
		// would leave the application half down. Ctrl+C still works, and is
		// handled before this.
		return m, nil
	}

	switch msg.String() {
	case "y", "enter":
		m.shutdown.working = true
		m.shutdown.err = nil
		return m, m.submitShutdown()
	case "n", "esc", "q":
		m.shutdown = nil
		m.view = viewList
		return m, nil
	case "w":
		// Leave the databases running and just close the window. The escape
		// hatch for "I only wanted my terminal back".
		m.quitting = true
		m.logs.stop()
		return m, tea.Quit
	}
	return m, nil
}

// submitShutdown asks the daemon to stop everything, including itself.
func (m Model) submitShutdown() tea.Cmd {
	c := m.client
	return func() tea.Msg {
		// Generous: stopping a database is a checkpoint, and Postgres is
		// allowed a full minute of it before Podman resorts to SIGKILL.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		rep, err := c.Shutdown(ctx)
		return shutdownDoneMsg{report: rep, err: err}
	}
}

func (m Model) viewQuit() string {
	var b strings.Builder
	s := m.shutdown
	if s == nil {
		// Nothing to confirm; the list is what belongs on screen.
		return m.viewList()
	}

	b.WriteString(styleTitle.Render("Quit DBForge"))
	b.WriteString("\n\n")

	if s.working {
		b.WriteString("Stopping ")
		b.WriteString(styleTitle.Render(plural(len(s.running), "database", "databases")))
		b.WriteString(" and the DBForge daemon...\n\n")
		for _, id := range s.running {
			b.WriteString(styleDim.Render("  " + id))
			b.WriteString("\n")
		}
		b.WriteString("\n")
		b.WriteString(styleHelp.Render("this can take a moment: a database is stopped cleanly, not killed"))
		return b.String()
	}

	if s.err != nil {
		b.WriteString(styleErr.Render(wrap(s.err.Error(), max(20, m.width-2))))
		b.WriteString("\n\n")
	}

	switch n := len(s.running); n {
	case 0:
		b.WriteString("This stops the DBForge daemon. No databases are running.\n\n")
	default:
		b.WriteString(fmt.Sprintf("This stops %s and then the DBForge daemon:\n\n",
			plural(n, "database", "databases")))
		for _, id := range s.running {
			b.WriteString("  " + styleTitle.Render(id) + "\n")
		}
		b.WriteString("\n")
		b.WriteString(styleDim.Render("They come back next time you start DBForge."))
		b.WriteString("\n")
		b.WriteString(styleDim.Render("Anything connected to them will be disconnected."))
		b.WriteString("\n")
		b.WriteString("\n")
	}

	b.WriteString(styleHelp.Render("y quit DBForge   w close this window only   n cancel"))
	return b.String()
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}
