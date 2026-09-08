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

// The update screen is deliberately three states and no more: it is checking,
// it is asking, or it is installing. Anything it cannot do -- a packaged
// install, no release, no network -- ends as a message with the command that
// would work, rather than a retry loop.

type updateStep int

const (
	updateChecking updateStep = iota
	updateOffer
	updateWorking
	updateDone
)

type updateState struct {
	step    updateStep
	release update.Release
	// progress is the last thing the installer said it was doing.
	progress string
	// done is the closing message: what happened, good or bad.
	done string
	err  error
	// progressCh carries steps from the installer goroutine. Kept on the
	// model so each progress message can re-arm the wait for the next one.
	progressCh chan updateProgressMsg
}

type updateCheckedMsg struct {
	release update.Release
	err     error
}

type updateProgressMsg string

type updateAppliedMsg struct {
	replaced []string
	// restartErr is separate: the binaries are in place either way, and
	// "installed but restart it yourself" is not a failed update.
	restartErr error
	err        error
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

// applyUpdate installs the release and restarts the daemon.
//
// Progress is streamed through a channel rather than returned at the end,
// because a download is the one thing here slow enough that a still screen
// reads as a hang.
func (m Model) applyUpdate(rel update.Release, ch chan updateProgressMsg) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()

		defer close(ch)
		dir, err := update.InstallDir()
		if err != nil {
			return updateAppliedMsg{err: err}
		}
		replaced, err := update.Apply(ctx, rel, dir, func(s string) {
			// Non-blocking: a dropped progress line costs nothing, and the
			// installer must never wait on the UI.
			select {
			case ch <- updateProgressMsg(s):
			default:
			}
		})
		if err != nil {
			return updateAppliedMsg{replaced: replaced, err: err}
		}
		return updateAppliedMsg{replaced: replaced, restartErr: update.RestartDaemon(ctx)}
	}
}

func waitForUpdateProgress(ch chan updateProgressMsg) tea.Cmd {
	return func() tea.Msg {
		s, ok := <-ch
		if !ok {
			return nil
		}
		return s
	}
}

func (m Model) updateUpdate(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	u := m.update
	if u == nil {
		m.view = viewList
		return m, nil
	}

	switch u.step {
	case updateOffer:
		switch msg.String() {
		case "y", "enter":
			u.step = updateWorking
			u.progress = "starting"
			u.progressCh = make(chan updateProgressMsg, 8)
			return m, tea.Batch(m.applyUpdate(u.release, u.progressCh),
				waitForUpdateProgress(u.progressCh))
		case "n", "esc", "q":
			m.update = nil
			m.view = viewList
			return m, nil
		}
	case updateWorking:
		// Replacing binaries halfway through is not something to offer a
		// cancel for.
		return m, nil
	default:
		switch msg.String() {
		case "esc", "q", "enter", "y", "n":
			m.update = nil
			m.view = viewList
			return m, m.fetchInstances()
		}
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

	switch u.step {
	case updateChecking:
		b.WriteString(styleDim.Render("checking " + update.Repo + " for a newer release..."))
		return b.String()

	case updateOffer:
		b.WriteString(fmt.Sprintf("  installed  %s\n", styleDim.Render(Version)))
		b.WriteString(fmt.Sprintf("  available  %s\n\n", styleTitle.Render(u.release.Version)))
		b.WriteString(styleDim.Render(u.release.URL))
		b.WriteString("\n\n")
		b.WriteString("Downloads the release binaries, checks them against the\n")
		b.WriteString("release's own SHA256SUMS, and restarts the daemon.\n")
		b.WriteString(styleDim.Render("Your databases keep running; the TUI keeps the old code until you reopen it."))
		b.WriteString("\n")
		b.WriteString("\n")
		b.WriteString(styleHelp.Render("y update   n cancel"))
		return b.String()

	case updateWorking:
		b.WriteString(styleDim.Render(u.progress))
		b.WriteString("\n\n")
		b.WriteString(styleHelp.Render("installing; this cannot be cancelled"))
		return b.String()

	default:
		if u.err != nil {
			b.WriteString(styleErr.Render(wrap(u.err.Error(), max(20, m.width-2))))
		} else {
			b.WriteString(styleOK.Render(u.done))
		}
		b.WriteString("\n\n")
		b.WriteString(styleHelp.Render("enter back"))
		return b.String()
	}
}

// applyUpdateMessages folds the update's own messages into the root model.
func (m Model) applyUpdateMessages(msg tea.Msg) (Model, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case updateCheckedMsg:
		if m.update == nil {
			return m, nil, true
		}
		switch {
		case errors.Is(msg.err, update.ErrNoRelease):
			m.update.step = updateDone
			m.update.err = fmt.Errorf(
				"no published release found for %s. If the repository is private, "+
					"releases are not visible to this machine", update.Repo)
		case msg.err != nil:
			m.update.step = updateDone
			m.update.err = msg.err
		case !update.Newer(Version, msg.release.Version):
			m.update.step = updateDone
			m.update.done = fmt.Sprintf("DBForge %s is the latest release.", msg.release.Version)
		default:
			m.update.step = updateOffer
			m.update.release = msg.release
		}
		return m, nil, true

	case updateProgressMsg:
		if m.update == nil {
			return m, nil, true
		}
		m.update.progress = string(msg)
		// Re-arm: one wait yields one message, and the installer has more.
		return m, waitForUpdateProgress(m.update.progressCh), true

	case updateAppliedMsg:
		if m.update == nil {
			return m, nil, true
		}
		m.update.step = updateDone
		switch {
		case msg.err != nil:
			m.update.err = msg.err
		case msg.restartErr != nil:
			m.update.done = fmt.Sprintf("Installed %s. %s",
				m.update.release.Version, msg.restartErr)
		default:
			m.update.done = fmt.Sprintf(
				"Installed %s and restarted the daemon.\nReopen this window to run the new version.",
				m.update.release.Version)
		}
		return m, nil, true
	}
	return m, nil, false
}
