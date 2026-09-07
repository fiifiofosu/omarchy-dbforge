package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/fiifiofosu/dbforge/internal/daemon"
	"github.com/fiifiofosu/dbforge/internal/model"
	"github.com/fiifiofosu/dbforge/internal/registry"
)

// createStep is the position in the guided form: engine, then version, then
// name, then an optional port override (spec 7, phase 3).
type createStep int

const (
	stepEngine createStep = iota
	stepVersion
	stepName
	stepPort
	stepRestart
	stepSubmitting
)

type createModel struct {
	step createStep

	engines     []string
	engineIndex int

	versions     []string
	versionIndex int
	versionSrc   registry.Source
	versionFetch time.Time
	versionErr   error
	versionsFor  string // which engine the list belongs to

	name string
	port string

	restarts     []model.RestartPolicy
	restartIndex int

	err error
}

func newCreateModel() createModel {
	return createModel{
		step:     stepEngine,
		engines:  engineNames(),
		restarts: []model.RestartPolicy{model.RestartAlways, model.RestartOnFailure, model.RestartNo},
	}
}

func (c createModel) engine() string {
	if c.engineIndex < 0 || c.engineIndex >= len(c.engines) {
		return ""
	}
	return c.engines[c.engineIndex]
}

func (c createModel) version() string {
	if c.versionIndex < 0 || c.versionIndex >= len(c.versions) {
		return ""
	}
	return c.versions[c.versionIndex]
}

// applyVersions installs a version list, ignoring one that arrives for an
// engine the user has since moved away from.
func (c *createModel) applyVersions(engine string, r registry.Result) {
	if engine != c.engine() {
		return
	}
	c.versions = r.Versions
	c.versionSrc = r.Source
	c.versionFetch = r.FetchedAt
	c.versionErr = r.Err
	c.versionsFor = engine
	if c.versionIndex >= len(c.versions) {
		c.versionIndex = 0
	}
}

type createdMsg struct {
	inst model.Instance
	err  error
}

func (m Model) submitCreate() tea.Cmd {
	c := m.client
	opt := daemon.CreateOptions{
		Ref:     m.create.engine() + ":" + m.create.version(),
		ID:      strings.TrimSpace(m.create.name),
		Start:   true,
		Restart: m.create.restarts[m.create.restartIndex],
	}
	if p := strings.TrimSpace(m.create.port); p != "" {
		if n, err := strconv.Atoi(p); err == nil {
			opt.Port = n
		}
	}
	return func() tea.Msg {
		// Generous: a first pull of a database image is slow.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		inst, err := c.Create(ctx, opt)
		return createdMsg{inst: inst, err: err}
	}
}

func (m Model) updateCreate(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	c := &m.create

	switch msg.String() {
	case "esc":
		if c.step == stepSubmitting {
			return m, nil // do not abandon an in-flight create
		}
		m.view = viewList
		return m, nil

	case "tab", "down":
		switch c.step {
		case stepEngine:
			if c.engineIndex < len(c.engines)-1 {
				c.engineIndex++
				return m, m.loadVersionsFor(c.engine())
			}
		case stepVersion:
			if c.versionIndex < len(c.versions)-1 {
				c.versionIndex++
			}
		case stepRestart:
			if c.restartIndex < len(c.restarts)-1 {
				c.restartIndex++
			}
		}
		return m, nil

	case "shift+tab", "up":
		switch c.step {
		case stepEngine:
			if c.engineIndex > 0 {
				c.engineIndex--
				return m, m.loadVersionsFor(c.engine())
			}
		case stepVersion:
			if c.versionIndex > 0 {
				c.versionIndex--
			}
		case stepRestart:
			if c.restartIndex > 0 {
				c.restartIndex--
			}
		}
		return m, nil

	case "enter":
		return m.advanceCreate()

	case "backspace":
		switch c.step {
		case stepName:
			if c.name != "" {
				c.name = c.name[:len(c.name)-1]
			}
		case stepPort:
			if c.port != "" {
				c.port = c.port[:len(c.port)-1]
			}
		}
		return m, nil
	}

	// Text entry for the free-form steps.
	if msg.Type == tea.KeyRunes {
		switch c.step {
		case stepName:
			c.name += string(msg.Runes)
		case stepPort:
			for _, r := range msg.Runes {
				if r >= '0' && r <= '9' {
					c.port += string(r)
				}
			}
		}
	}
	return m, nil
}

func (m Model) loadVersionsFor(engine string) tea.Cmd {
	// Show the cache instantly so the list is never empty while we wait.
	m.create.applyVersions(engine, m.registry.CachedOnly(engine))
	return m.fetchVersions(engine)
}

func (m Model) advanceCreate() (tea.Model, tea.Cmd) {
	c := &m.create
	switch c.step {
	case stepEngine:
		c.step = stepVersion
		return m, m.loadVersionsFor(c.engine())

	case stepVersion:
		if c.version() == "" {
			c.err = fmt.Errorf("pick a version")
			return m, nil
		}
		c.err = nil
		c.step = stepName
		if c.name == "" {
			// A sensible default the user can accept or overwrite.
			c.name = c.engine() + strings.ReplaceAll(c.version(), ".", "")
		}
		return m, nil

	case stepName:
		if strings.TrimSpace(c.name) == "" {
			c.err = fmt.Errorf("name cannot be empty")
			return m, nil
		}
		for _, inst := range m.instances {
			if inst.ID == strings.TrimSpace(c.name) {
				// Catch the clash here rather than after a round trip.
				c.err = fmt.Errorf("an instance named %q already exists", inst.ID)
				return m, nil
			}
		}
		c.err = nil
		c.step = stepPort
		return m, nil

	case stepPort:
		if p := strings.TrimSpace(c.port); p != "" {
			n, err := strconv.Atoi(p)
			if err != nil || n < 1 || n > 65535 {
				c.err = fmt.Errorf("port must be between 1 and 65535")
				return m, nil
			}
		}
		c.err = nil
		c.step = stepRestart
		return m, nil

	case stepRestart:
		c.step = stepSubmitting
		return m, m.submitCreate()
	}
	return m, nil
}

func (m Model) viewCreate() string {
	c := m.create
	var b strings.Builder

	b.WriteString(styleTitle.Render("New instance"))
	b.WriteString("\n\n")

	b.WriteString(fieldLine("Engine", c.engine(), c.step == stepEngine))
	if c.step >= stepVersion {
		b.WriteString(fieldLine("Version", c.version(), c.step == stepVersion))
	}
	if c.step >= stepName {
		b.WriteString(fieldLine("Name", c.name, c.step == stepName))
	}
	if c.step >= stepPort {
		port := c.port
		if port == "" {
			port = styleDim.Render("(auto)")
		}
		b.WriteString(fieldLine("Port", port, c.step == stepPort))
	}
	if c.step >= stepRestart {
		b.WriteString(fieldLine("Restart", string(c.restarts[c.restartIndex]), c.step == stepRestart))
	}

	b.WriteString("\n")

	switch c.step {
	case stepEngine:
		b.WriteString(chooser(c.engines, c.engineIndex))
	case stepVersion:
		b.WriteString(chooser(c.versions, c.versionIndex))
		b.WriteString(versionSourceNote(c))
	case stepRestart:
		opts := make([]string, len(c.restarts))
		for i, r := range c.restarts {
			opts[i] = string(r)
		}
		b.WriteString(chooser(opts, c.restartIndex))
		b.WriteString("\n")
		b.WriteString(styleDim.Render(restartHelp(c.restarts[c.restartIndex])))
		b.WriteString("\n")
	case stepSubmitting:
		b.WriteString(styleWarn.Render("creating... (pulling the image can take a while)"))
		b.WriteString("\n")
	}

	if c.err != nil {
		b.WriteString("\n")
		b.WriteString(styleErr.Render(c.err.Error()))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	if c.step == stepSubmitting {
		b.WriteString(styleHelp.Render("please wait..."))
	} else {
		b.WriteString(styleHelp.Render("up/down choose   enter next   esc cancel"))
	}
	return b.String()
}

// versionSourceNote tells the user when the list is not live, which matters
// when creating an instance offline (spec 7, phase 3).
func versionSourceNote(c createModel) string {
	switch c.versionSrc {
	case registry.SourceCache:
		when := "unknown time"
		if !c.versionFetch.IsZero() {
			when = humanAge(c.versionFetch) + " ago"
		}
		msg := fmt.Sprintf("\noffline: showing a cached list from %s", when)
		if c.versionErr != nil {
			msg += "\nreason: " + c.versionErr.Error()
		}
		return styleWarn.Render(msg) + "\n"
	case registry.SourceBuiltin:
		msg := "\noffline: showing DBForge's builtin list, which may be out of date"
		if c.versionErr != nil {
			msg += "\nreason: " + c.versionErr.Error()
		}
		return styleWarn.Render(msg) + "\n"
	default:
		return ""
	}
}

func restartHelp(p model.RestartPolicy) string {
	switch p {
	case model.RestartAlways:
		return "comes back after a crash, a daemon restart, or a reboot"
	case model.RestartOnFailure:
		return "comes back only after an unclean exit"
	default:
		return "only ever started explicitly"
	}
}

func fieldLine(label, value string, active bool) string {
	marker := "  "
	style := styleDim
	if active {
		marker = styleSelected.Render("> ")
		style = styleInput
	}
	return fmt.Sprintf("%s%-9s %s\n", marker, label+":", style.Render(value))
}

func chooser(options []string, index int) string {
	if len(options) == 0 {
		return styleDim.Render("  (no options)") + "\n"
	}
	var b strings.Builder
	// Keep the selection visible without scrolling the whole list.
	start := max(0, min(index-3, len(options)-7))
	end := min(len(options), start+7)
	for i := start; i < end; i++ {
		if i == index {
			b.WriteString(styleSelected.Render("  ▸ " + options[i]))
		} else {
			b.WriteString(styleDim.Render("    " + options[i]))
		}
		b.WriteString("\n")
	}
	if end < len(options) {
		b.WriteString(styleDim.Render(fmt.Sprintf("    ... %d more\n", len(options)-end)))
	}
	return b.String()
}

func humanAge(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
