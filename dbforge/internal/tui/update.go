package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/fiifiofosu/dbforge/internal/update"
)

// The update screen checks for a newer release and says how to install it. It
// does not install one: DBForge no longer replaces its own binaries, because
// picking the executable and the checksum that verifies it from the same
// mutable "latest" pointer proves only that the two agree with each other. See
// the package comment in internal/update.
//
// So there are two states, checking and done, and the closing message always
// carries the command that would actually work.

type updateStep int

const (
	updateChecking updateStep = iota
	updateDone
)

type updateState struct {
	step    updateStep
	release update.Release
	// done is the closing message: what was found, and what to do about it.
	done string
	// hint is the install command, shown under done when there is a newer
	// release. Empty otherwise.
	hint string
	err  error
}

type updateCheckedMsg struct {
	release update.Release
	err     error
}

func (m Model) startUpdate() (tea.Model, tea.Cmd) {
	m.update = &updateState{step: updateChecking}
	m.view = viewUpdate
	return m, m.checkForUpdate()
}

func (m Model) checkForUpdate() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		rel, err := update.Latest(ctx)
		return updateCheckedMsg{release: rel, err: err}
	}
}

func (m Model) updateUpdate(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.update == nil {
		m.view = viewList
		return m, nil
	}
	switch msg.String() {
	case "esc", "q", "enter", "y", "n":
		m.update = nil
		m.view = viewList
		return m, m.fetchInstances()
	}
	return m, nil
}

func (m Model) viewUpdate() string {
	var b strings.Builder
	u := m.update
	if u == nil {
		return ""
	}

	b.WriteString(styleTitle.Render("Update DBForge"))
	b.WriteString("\n\n")

	if u.step == updateChecking {
		b.WriteString(styleDim.Render("checking " + update.Repo + " for a newer release..."))
		return b.String()
	}

	switch {
	case u.err != nil:
		b.WriteString(styleErr.Render(wrap(u.err.Error(), max(20, m.width-2))))
	case u.hint != "":
		b.WriteString(fmt.Sprintf("  installed  %s\n", styleDim.Render(Version)))
		b.WriteString(fmt.Sprintf("  available  %s\n\n", styleTitle.Render(u.release.Version)))
		b.WriteString(styleDim.Render(u.release.URL))
		b.WriteString("\n\n")
		b.WriteString(u.hint)
		b.WriteString("\n")
	default:
		b.WriteString(styleOK.Render(u.done))
	}

	b.WriteString("\n\n")
	b.WriteString(styleHelp.Render("enter back"))
	return b.String()
}

// applyUpdateMessages folds the update's own messages into the root model.
func (m Model) applyUpdateMessages(msg tea.Msg) (Model, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case updateCheckedMsg:
		if m.update == nil {
			return m, nil, true
		}
		m.update.step = updateDone
		switch {
		case errors.Is(msg.err, update.ErrNoRelease):
			m.update.err = fmt.Errorf(
				"no published release found for %s. If the repository is private, "+
					"releases are not visible to this machine", update.Repo)
		case msg.err != nil:
			m.update.err = msg.err
		case !update.Newer(Version, msg.release.Version):
			m.update.done = fmt.Sprintf("DBForge %s is the latest release.", msg.release.Version)
		default:
			m.update.release = msg.release
			m.update.hint = update.InstallHint()
		}
		return m, nil, true
	}
	return m, nil, false
}
