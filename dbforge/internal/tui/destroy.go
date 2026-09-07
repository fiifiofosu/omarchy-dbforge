package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/fiifiofosu/dbforge/internal/model"
)

// destroyChoice is the deliberate fork in the destroy flow. The plan calls
// this the single most consequential foot-gun in DBngin-style tools, so the
// two outcomes are never one keystroke apart (spec 7, phase 3).
type destroyChoice int

const (
	// destroyContainerOnly removes the container and keeps the data. This is
	// the default and it is recoverable.
	destroyContainerOnly destroyChoice = iota
	// destroyWithData deletes the database. Irreversible, and gated behind
	// typing the instance name.
	destroyWithData
)

type destroyStep int

const (
	// stepChoose picks between keeping and wiping the data.
	stepChoose destroyStep = iota
	// stepTypeName is only ever reached on the wipe path.
	stepTypeName
	stepWorking
)

type destroyModel struct {
	inst   model.Instance
	choice destroyChoice
	step   destroyStep
	typed  string
	err    error
}

func newDestroyModel(inst model.Instance) destroyModel {
	// Always start on the safe option. A user who mashes enter keeps their data.
	return destroyModel{inst: inst, choice: destroyContainerOnly, step: stepChoose}
}

func (m Model) submitDestroy() tea.Cmd {
	c := m.client
	id := m.destroy.inst.ID
	wipe := m.destroy.choice == destroyWithData
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		verb := "destroyed (data kept)"
		if wipe {
			verb = "destroyed with data"
		}
		// Force, because the TUI's confirmation already covers the "it is
		// running" case that --force guards on the CLI.
		return actionDoneMsg{verb: verb, id: id, err: c.Remove(ctx, id, wipe, true)}
	}
}

func (m Model) updateDestroy(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	d := &m.destroy

	switch d.step {
	case stepChoose:
		switch msg.String() {
		case "esc", "q", "n":
			m.view = viewList
			return m, nil
		case "up", "k", "down", "j", "tab":
			if d.choice == destroyContainerOnly {
				d.choice = destroyWithData
			} else {
				d.choice = destroyContainerOnly
			}
			return m, nil
		case "enter":
			if d.choice == destroyContainerOnly {
				d.step = stepWorking
				m.view = viewList
				return m, m.submitDestroy()
			}
			// The destructive path never completes on enter alone.
			d.step = stepTypeName
			d.typed = ""
			return m, nil
		}
		return m, nil

	case stepTypeName:
		switch msg.String() {
		case "esc":
			d.step = stepChoose
			d.typed = ""
			d.err = nil
			return m, nil
		case "backspace":
			if d.typed != "" {
				d.typed = d.typed[:len(d.typed)-1]
			}
			return m, nil
		case "enter":
			if strings.TrimSpace(d.typed) != d.inst.ID {
				d.err = fmt.Errorf("that does not match %q; nothing was removed", d.inst.ID)
				d.typed = ""
				return m, nil
			}
			d.step = stepWorking
			m.view = viewList
			return m, m.submitDestroy()
		}
		if msg.Type == tea.KeyRunes {
			d.typed += string(msg.Runes)
		}
		return m, nil
	}
	return m, nil
}

func (m Model) viewDestroy() string {
	d := m.destroy
	var b strings.Builder

	b.WriteString(styleDanger.Render("Destroy " + d.inst.ID))
	b.WriteString("\n\n")

	switch d.step {
	case stepChoose:
		b.WriteString("What should happen to the data?\n\n")

		b.WriteString(destroyOption(
			"Remove the container, keep the data",
			"The database files stay at "+d.inst.DataDir+". Recreating this\n     instance later reattaches to them.",
			d.choice == destroyContainerOnly, false))

		b.WriteString(destroyOption(
			"Remove the container AND delete the data",
			"Permanently deletes the database. This cannot be undone.",
			d.choice == destroyWithData, true))

		b.WriteString("\n")
		b.WriteString(styleHelp.Render("up/down switch   enter confirm   esc cancel"))

	case stepTypeName:
		b.WriteString(styleDanger.Render("This permanently deletes all data for " + d.inst.ID + "."))
		b.WriteString("\n")
		b.WriteString(styleDim.Render(d.inst.DataDir))
		b.WriteString("\n\n")
		b.WriteString("Type the instance name to confirm:\n\n  ")
		b.WriteString(styleInput.Render(d.typed))
		b.WriteString(styleSelected.Render("█"))
		b.WriteString("\n")
		if d.err != nil {
			b.WriteString("\n")
			b.WriteString(styleErr.Render(d.err.Error()))
			b.WriteString("\n")
		}
		b.WriteString("\n")
		b.WriteString(styleHelp.Render("enter confirm   esc back"))
	}

	return b.String()
}

func destroyOption(title, detail string, selected, danger bool) string {
	marker := "   "
	titleStyle := styleDim
	if selected {
		marker = styleSelected.Render(" > ")
		titleStyle = styleSelected
		if danger {
			titleStyle = styleDanger
		}
	}
	return fmt.Sprintf("%s%s\n     %s\n\n",
		marker, titleStyle.Render(title), styleDim.Render(detail))
}
