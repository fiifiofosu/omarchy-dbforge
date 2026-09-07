// Package tui is DBForge's terminal interface (spec 7, phase 3).
package tui

import "github.com/charmbracelet/lipgloss"

// Colours come from the terminal's own 16-colour palette rather than from
// lipgloss.AdaptiveColor.
//
// AdaptiveColor has to know whether the background is light or dark, which it
// learns by querying the terminal and blocking until it answers. Measured
// here, that delayed the first paint by a full 5 seconds on a terminal that
// does not reply -- and there is no way to know in advance which terminals
// those are. Palette colours need no query at all, and they adapt better
// anyway: they follow whatever theme the user has already chosen.
var (
	colAccent = lipgloss.Color("4") // blue
	colDim    = lipgloss.Color("8") // bright black
	colOK     = lipgloss.Color("2") // green
	colWarn   = lipgloss.Color("3") // yellow
	colErr    = lipgloss.Color("1") // red
	colText   = lipgloss.Color("7") // white
)

var (
	styleTitle = lipgloss.NewStyle().Bold(true).Foreground(colAccent)
	styleDim   = lipgloss.NewStyle().Foreground(colDim)
	styleErr   = lipgloss.NewStyle().Foreground(colErr)
	styleWarn  = lipgloss.NewStyle().Foreground(colWarn)
	styleOK    = lipgloss.NewStyle().Foreground(colOK)

	styleHeader = lipgloss.NewStyle().Bold(true).Foreground(colDim)
	styleRow    = lipgloss.NewStyle().Foreground(colText)
	// styleSelected marks the cursor row. Reverse video rather than a colour,
	// so it reads correctly whatever the terminal theme.
	styleSelected = lipgloss.NewStyle().Bold(true).Foreground(colAccent)

	styleHelp = lipgloss.NewStyle().Foreground(colDim)

	stylePanel = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colDim).
			Padding(0, 1)

	styleDanger = lipgloss.NewStyle().Bold(true).Foreground(colErr)

	styleInput = lipgloss.NewStyle().Foreground(colText)
)

// statusStyle colours a status word by severity.
func statusStyle(status string) lipgloss.Style {
	switch {
	case status == "running":
		return styleOK
	case status == "missing(!)":
		return styleErr
	case len(status) > 6 && status[:6] == "exited":
		return styleErr
	default:
		return styleDim
	}
}
