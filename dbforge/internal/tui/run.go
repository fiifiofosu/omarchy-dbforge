package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/fiifiofosu/dbforge/internal/cli"
)

// Run starts the TUI and blocks until the user quits.
func Run(c *cli.Client) error {
	p := tea.NewProgram(New(c), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		return fmt.Errorf("running the TUI: %w", err)
	}
	return nil
}
