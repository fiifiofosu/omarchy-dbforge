package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/fiifiofosu/dbforge/internal/model"
)

// waybarStatus is waybar's custom-module JSON contract.
type waybarStatus struct {
	Text    string `json:"text"`
	Alt     string `json:"alt,omitempty"`
	Tooltip string `json:"tooltip,omitempty"`
	Class   string `json:"class,omitempty"`
}

// Icon glyphs come from the Nerd Font Omarchy already uses for its bar.
const (
	iconDB    = "\U000f01bc" // nf-md-database
	iconAlert = "\U000f0026" // nf-md-alert
)

// maxTooltipRows caps how many instances the tooltip lists. With many
// instances the widget must summarise rather than render them all inline
// (spec 7, phase 4).
const maxTooltipRows = 12

func cmdStatus(ctx context.Context, c *Client, args []string) error {
	var waybar, menu bool
	for _, a := range args {
		switch a {
		case "--waybar":
			waybar = true
		case "--menu":
			menu = true
		}
	}

	list, err := c.List(ctx)

	switch {
	case waybar:
		return json.NewEncoder(os.Stdout).Encode(buildWaybarStatus(list, err))
	case menu:
		// Formatted here rather than in the shell script: getting quoting
		// right through bash into python into JSON is a reliable source of
		// bugs, and this way it is testable.
		if err != nil {
			return err
		}
		for _, line := range MenuLines(list) {
			fmt.Println(line)
		}
		return nil
	default:
		return printPlainStatus(list, err)
	}
}

// MenuLines renders one action per instance for the waybar right-click menu.
//
// The verb is implied by the instance's current state, so the menu lists
// things that will happen rather than nouns to choose between. The leading
// marker keeps the columns aligned in a dmenu-style picker.
func MenuLines(list []model.Instance) []string {
	sorted := append([]model.Instance(nil), list...)
	sort.Slice(sorted, func(a, b int) bool { return sorted[a].ID < sorted[b].ID })

	out := make([]string, 0, len(sorted))
	for _, i := range sorted {
		verb, mark := "Start", "-"
		switch {
		case i.Status == model.StatusMissing:
			verb, mark = "Forget", "!"
		case i.Status == model.StatusRunning:
			verb, mark = "Stop", "*"
		}
		out = append(out, fmt.Sprintf("%s %s %s  (%s:%s :%d)",
			mark, verb, i.ID, i.Engine, i.Version, i.Port))
	}
	return out
}

// buildWaybarStatus turns daemon state into the widget's payload.
//
// The critical distinction is between "the daemon is not running" and "there
// are no instances". Rendered naively those look identical -- both are simply
// an absence -- and they need completely different responses from the user
// (spec 7, phase 4).
func buildWaybarStatus(list []model.Instance, err error) waybarStatus {
	if err != nil {
		return waybarStatus{
			Text:    iconAlert,
			Alt:     "offline",
			Class:   "offline",
			Tooltip: "DBForge daemon is not running\n\nStart it with:\nsystemctl --user start dbforged",
		}
	}

	if len(list) == 0 {
		return waybarStatus{
			Text:    iconDB,
			Alt:     "empty",
			Class:   "empty",
			Tooltip: "No database instances\n\nClick to open DBForge",
		}
	}

	var running, stopped, problems int
	for _, i := range list {
		switch {
		case i.Status == model.StatusMissing:
			problems++
		case i.Status != model.StatusRunning && i.LastExitCode != 0:
			problems++
		case i.Status == model.StatusRunning:
			running++
		default:
			stopped++
		}
	}

	st := waybarStatus{
		Text:    fmt.Sprintf("%s %d", iconDB, running),
		Alt:     "running",
		Class:   "running",
		Tooltip: tooltipFor(list, running, stopped, problems),
	}

	switch {
	case problems > 0:
		// A degraded instance is the one thing worth pulling the eye, so it
		// wins over the plain running count.
		st.Text = fmt.Sprintf("%s %d", iconAlert, problems)
		st.Alt, st.Class = "problem", "problem"
	case running == 0:
		st.Text = iconDB
		st.Alt, st.Class = "stopped", "stopped"
	}
	return st
}

func tooltipFor(list []model.Instance, running, stopped, problems int) string {
	var b strings.Builder

	parts := []string{fmt.Sprintf("%d running", running)}
	if stopped > 0 {
		parts = append(parts, fmt.Sprintf("%d stopped", stopped))
	}
	if problems > 0 {
		parts = append(parts, fmt.Sprintf("%d need attention", problems))
	}
	b.WriteString("DBForge: " + strings.Join(parts, ", "))
	b.WriteString("\n")

	sorted := append([]model.Instance(nil), list...)
	// Surface the interesting ones first: a long list gets truncated, and a
	// broken instance must not be the row that falls off the end.
	sort.SliceStable(sorted, func(a, b int) bool {
		return severity(sorted[a]) > severity(sorted[b])
	})

	shown := sorted
	if len(shown) > maxTooltipRows {
		shown = shown[:maxTooltipRows]
	}
	for _, i := range shown {
		b.WriteString(fmt.Sprintf("\n%-16s %-9s %-6d %s",
			i.ID, i.Engine+":"+i.Version, i.Port, statusWord(i)))
	}
	if len(sorted) > len(shown) {
		b.WriteString(fmt.Sprintf("\n... and %d more", len(sorted)-len(shown)))
	}
	return b.String()
}

// severity ranks instances so the tooltip leads with anything wrong.
func severity(i model.Instance) int {
	switch {
	case i.Status == model.StatusMissing:
		return 3
	case i.Status != model.StatusRunning && i.LastExitCode != 0:
		return 2
	case i.Status == model.StatusRunning:
		return 1
	default:
		return 0
	}
}

func statusWord(i model.Instance) string {
	switch {
	case i.Status == model.StatusMissing:
		return "missing!"
	case i.Status != model.StatusRunning && i.LastExitCode != 0:
		return fmt.Sprintf("exited(%d)", i.LastExitCode)
	default:
		return string(i.Status)
	}
}

func printPlainStatus(list []model.Instance, err error) error {
	if err != nil {
		fmt.Println("daemon: offline")
		return err
	}
	running := 0
	for _, i := range list {
		if i.Status == model.StatusRunning {
			running++
		}
	}
	fmt.Printf("daemon: online\ninstances: %d (%d running)\n", len(list), running)
	return nil
}
