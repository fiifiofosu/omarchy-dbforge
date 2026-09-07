package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/fiifiofosu/dbforge/internal/cli"
	"github.com/fiifiofosu/dbforge/internal/engines"
	"github.com/fiifiofosu/dbforge/internal/model"
	"github.com/fiifiofosu/dbforge/internal/registry"
)

// view is which screen is on top.
type view int

const (
	viewList view = iota
	viewLogs
	viewCreate
	viewConfirmDestroy
	viewConnection
)

// refreshInterval is how often the list re-polls the daemon. Frequent enough
// that a start or stop shows up on its own, infrequent enough to stay cheap.
const refreshInterval = 2 * time.Second

// Model is the root Bubble Tea model.
type Model struct {
	client   *cli.Client
	registry *registry.Client

	view     view
	width    int
	height   int
	quitting bool

	// list state
	instances []model.Instance
	cursor    int
	// daemonErr is set when the daemon cannot be reached. This must render
	// differently from "no instances" -- they look identical otherwise and
	// the distinction matters (spec 7, phase 4).
	daemonErr error
	loaded    bool

	// transient status line
	status     string
	statusErr  bool
	statusTime time.Time

	// daemonVersion is set only when it differs from this binary's, meaning
	// this process predates an upgrade and is running superseded code.
	daemonVersion string

	logs    logsModel
	create  createModel
	destroy destroyModel
	conn    connModel
}

// New builds the root model.
// Version is this binary's build version, set by the TUI's main.
//
// It is compared against the daemon's so a TUI left open across an upgrade can
// say so. That is not hypothetical: replacing the binaries does not restart a
// running process, so an open TUI keeps executing the old code while talking
// to the new daemon, and whatever the upgrade added simply appears not to
// work.
var Version = "dev"

func New(c *cli.Client) Model {
	return Model{
		client:   c,
		registry: registry.New(),
		view:     viewList,
		create:   newCreateModel(),
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.fetchInstances(), m.checkVersion(), tick())
}

// staleMsg carries the daemon's version when it differs from this binary's.
type staleMsg struct{ daemon string }

// checkVersion asks the daemon what it is running. Failure is silent: the
// daemon being unreachable is already reported by the instance fetch, and a
// second complaint about the same thing is noise.
func (m Model) checkVersion() tea.Cmd {
	c := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		v, err := c.Version(ctx)
		if err != nil || v == "" || v == Version {
			return nil
		}
		return staleMsg{daemon: v}
	}
}

// --- messages ---

type instancesMsg struct {
	instances []model.Instance
	err       error
}

type tickMsg time.Time

type actionDoneMsg struct {
	verb string
	id   string
	err  error
}

type connStringMsg struct {
	id  string
	str string
	err error
}

type versionsMsg struct {
	engine string
	result registry.Result
}

func tick() tea.Cmd {
	return tea.Tick(refreshInterval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m Model) fetchInstances() tea.Cmd {
	c := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		list, err := c.List(ctx)
		return instancesMsg{instances: list, err: err}
	}
}

// action runs a lifecycle verb against the daemon off the UI goroutine.
func (m Model) action(verb, id string, fn func(context.Context, string) error) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		return actionDoneMsg{verb: verb, id: id, err: fn(ctx, id)}
	}
}

func (m Model) fetchConnString(id string) tea.Cmd {
	c := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		s, err := c.ConnString(ctx, id)
		return connStringMsg{id: id, str: s, err: err}
	}
}

// fetchVersions refreshes an engine's version list in the background. The form
// renders from cache immediately, so this only ever upgrades what is shown.
func (m Model) fetchVersions(engine string) tea.Cmd {
	reg := m.registry
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return versionsMsg{engine: engine, result: reg.Versions(ctx, engine)}
	}
}

// selected returns the instance under the cursor.
func (m Model) selected() (model.Instance, bool) {
	if m.cursor < 0 || m.cursor >= len(m.instances) {
		return model.Instance{}, false
	}
	return m.instances[m.cursor], true
}

func (m *Model) setStatus(s string, isErr bool) {
	m.status = s
	m.statusErr = isErr
	m.statusTime = time.Now()
}

// --- update ---

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.logs.setSize(msg.Width, msg.Height)
		return m, nil

	case tickMsg:
		if m.quitting {
			return m, nil
		}
		// Only the list view polls. Re-polling under a modal would move the
		// selection out from under the user mid-decision.
		if m.view == viewList {
			return m, tea.Batch(m.fetchInstances(), tick())
		}
		return m, tick()

	case instancesMsg:
		m.loaded = true
		m.daemonErr = msg.err
		if msg.err == nil {
			m.instances = msg.instances
			if m.cursor >= len(m.instances) {
				m.cursor = max(0, len(m.instances)-1)
			}
		}
		return m, nil

	case actionDoneMsg:
		if msg.err != nil {
			m.setStatus(fmt.Sprintf("%s %s: %v", msg.verb, msg.id, msg.err), true)
		} else {
			m.setStatus(fmt.Sprintf("%s %s", msg.id, msg.verb), false)
		}
		return m, m.fetchInstances()

	case connStringMsg:
		m.conn = connModel{id: msg.id, str: msg.str, err: msg.err}
		m.view = viewConnection
		return m, nil

	case versionsMsg:
		m.create.applyVersions(msg.engine, msg.result)
		return m, nil

	case pullMsg:
		// Latest phase wins: this is a status line, not a log. The layer
		// counter only ever grows, so a dropped message costs nothing.
		m.create.pullPhase = msg.ev.Message
		if msg.ev.Layer > m.create.pullLayers {
			m.create.pullLayers = msg.ev.Layer
		}
		return m, waitForPull(m.create.pullCh)

	case spinMsg:
		if m.create.step != stepSubmitting {
			// The create finished; stop ticking rather than spinning forever
			// behind whatever view replaced it.
			return m, nil
		}
		m.create.spinFrame++
		return m, spinTick()

	case staleMsg:
		m.daemonVersion = msg.daemon
		return m, nil

	case createdMsg:
		if msg.err != nil {
			// Stay on the form so the user can fix the input rather than
			// losing everything they typed.
			m.create.step = stepRestart
			m.create.err = msg.err
			return m, nil
		}
		m.view = viewList
		m.setStatus(fmt.Sprintf("created %s on port %d", msg.inst.ID, msg.inst.Port), false)
		return m, m.fetchInstances()

	case logLineMsg:
		m.logs.append(msg)
		return m, m.logs.waitForNext()

	case logClosedMsg:
		m.logs.markClosed(msg.err)
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Ctrl+C always quits, from any view, and must tear down a log stream.
	if msg.Type == tea.KeyCtrlC {
		m.quitting = true
		m.logs.stop()
		return m, tea.Quit
	}

	switch m.view {
	case viewLogs:
		return m.updateLogs(msg)
	case viewCreate:
		return m.updateCreate(msg)
	case viewConfirmDestroy:
		return m.updateDestroy(msg)
	case viewConnection:
		switch msg.String() {
		case "esc", "q", "enter":
			m.view = viewList
		}
		return m, nil
	default:
		return m.updateList(msg)
	}
}

func (m Model) updateList(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc":
		m.quitting = true
		return m, tea.Quit

	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.instances)-1 {
			m.cursor++
		}
	case "g", "home":
		m.cursor = 0
	case "G", "end":
		m.cursor = max(0, len(m.instances)-1)

	case "r":
		return m, m.fetchInstances()

	case "s":
		if inst, ok := m.selected(); ok {
			if inst.Status == model.StatusRunning {
				m.setStatus("stopping "+inst.ID+"...", false)
				return m, m.action("stopped", inst.ID, m.client.Stop)
			}
			m.setStatus("starting "+inst.ID+"...", false)
			return m, m.action("started", inst.ID, m.client.Start)
		}

	case "R":
		if inst, ok := m.selected(); ok {
			m.setStatus("restarting "+inst.ID+"...", false)
			return m, m.action("restarted", inst.ID, m.client.RestartInstance)
		}

	case "l", "enter":
		if inst, ok := m.selected(); ok {
			m.logs = newLogsModel(m.client, inst.ID, m.width, m.height)
			m.view = viewLogs
			return m, m.logs.start()
		}

	case "c":
		if inst, ok := m.selected(); ok {
			return m, m.fetchConnString(inst.ID)
		}

	case "n":
		m.create = newCreateModel()
		m.view = viewCreate
		// Render from cache at once, then upgrade when the fetch lands.
		m.create.applyVersions(m.create.engine(), m.registry.CachedOnly(m.create.engine()))
		return m, m.fetchVersions(m.create.engine())

	case "d":
		if inst, ok := m.selected(); ok {
			m.destroy = newDestroyModel(inst)
			m.view = viewConfirmDestroy
		}
	}
	return m, nil
}

// --- view ---

func (m Model) View() string {
	if m.quitting {
		return ""
	}
	switch m.view {
	case viewLogs:
		return m.logs.view()
	case viewCreate:
		return m.viewCreate()
	case viewConfirmDestroy:
		return m.viewDestroy()
	case viewConnection:
		return m.viewConnection()
	default:
		return m.viewList()
	}
}

func (m Model) viewList() string {
	var b strings.Builder

	b.WriteString(styleTitle.Render("DBForge"))
	b.WriteString(styleDim.Render("  local database instances"))
	b.WriteString("\n")
	if note := m.staleNote(); note != "" {
		b.WriteString(note)
		b.WriteString("\n")
	}
	b.WriteString("\n")

	switch {
	case m.daemonErr != nil:
		// Distinct from "no instances": the daemon being down is a different
		// problem with a different fix.
		b.WriteString(styleErr.Render("daemon unreachable"))
		b.WriteString("\n\n")
		b.WriteString(styleDim.Render(wrap(m.daemonErr.Error(), max(20, m.width-2))))
		b.WriteString("\n\n")
		b.WriteString(styleHelp.Render("r refresh   q quit"))
		return b.String()

	case !m.loaded:
		b.WriteString(styleDim.Render("loading..."))
		return b.String()

	case len(m.instances) == 0:
		b.WriteString(styleDim.Render("No instances yet."))
		b.WriteString("\n\n")
		b.WriteString("Press ")
		b.WriteString(styleTitle.Render("n"))
		b.WriteString(" to create one.\n\n")
		b.WriteString(styleHelp.Render("n new   r refresh   q quit"))
		return b.String()
	}

	b.WriteString(styleHeader.Render(fmt.Sprintf(
		"  %-16s %-9s %-8s %-6s %-11s %-10s %s",
		"ID", "ENGINE", "VERSION", "PORT", "STATUS", "RESTART", "UPTIME")))
	b.WriteString("\n")

	for i, inst := range m.instances {
		cursor := "  "
		rowStyle := styleRow
		if i == m.cursor {
			cursor = styleSelected.Render("> ")
			rowStyle = styleSelected
		}

		status := statusLabel(inst)
		line := fmt.Sprintf("%-16s %-9s %-8s %-6d ",
			truncate(inst.ID, 16), truncate(inst.Engine, 9),
			truncate(inst.Version, 8), inst.Port)

		b.WriteString(cursor)
		b.WriteString(rowStyle.Render(line))
		b.WriteString(statusStyle(status).Render(fmt.Sprintf("%-11s", status)))
		b.WriteString(rowStyle.Render(fmt.Sprintf(" %-10s %s",
			string(inst.Restart), uptimeLabel(inst))))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	if m.status != "" && time.Since(m.statusTime) < 8*time.Second {
		if m.statusErr {
			b.WriteString(styleErr.Render(wrap(m.status, max(20, m.width-2))))
		} else {
			b.WriteString(styleOK.Render(m.status))
		}
		b.WriteString("\n\n")
	}

	b.WriteString(styleHelp.Render(
		"s start/stop   R restart   l logs   c connection   n new   d destroy   r refresh   q quit"))
	return b.String()
}

func (m Model) viewConnection() string {
	var b strings.Builder
	b.WriteString(styleTitle.Render("Connection: " + m.conn.id))
	b.WriteString("\n\n")

	if m.conn.err != nil {
		b.WriteString(styleErr.Render(m.conn.err.Error()))
	} else {
		b.WriteString(stylePanel.Width(min(max(40, m.width-4), 100)).Render(m.conn.str))
		b.WriteString("\n\n")
		b.WriteString(styleDim.Render("Select the text above to copy it."))
	}
	b.WriteString("\n\n")
	b.WriteString(styleHelp.Render("esc back"))
	return b.String()
}

type connModel struct {
	id  string
	str string
	err error
}

// --- shared helpers ---

func statusLabel(i model.Instance) string {
	switch {
	case i.Status == model.StatusMissing:
		return "missing(!)"
	case i.Status != model.StatusRunning && i.LastExitCode != 0:
		return fmt.Sprintf("exited(%d)", i.LastExitCode)
	default:
		return string(i.Status)
	}
}

func uptimeLabel(i model.Instance) string {
	d := i.Uptime()
	if d <= 0 {
		return "-"
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd%dh", int(d.Hours()/24), int(d.Hours())%24)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

// wrap breaks text at width on spaces, so long daemon errors stay readable.
func wrap(s string, width int) string {
	if width <= 0 || len(s) <= width {
		return s
	}
	var out strings.Builder
	line := 0
	for _, word := range strings.Fields(s) {
		if line > 0 && line+1+len(word) > width {
			out.WriteString("\n")
			line = 0
		} else if line > 0 {
			out.WriteString(" ")
			line++
		}
		out.WriteString(word)
		line += len(word)
	}
	return out.String()
}

func engineNames() []string { return engines.Names() }

// staleNote warns that this process is older than the daemon.
//
// It is shown persistently rather than as a transient status line, because the
// consequence is persistent: every feature the upgrade added is missing until
// the window is reopened, and a message that scrolls away after three seconds
// would be read once and then wondered about for an hour.
func (m Model) staleNote() string {
	if m.daemonVersion == "" {
		return ""
	}
	return styleWarn.Render(fmt.Sprintf(
		"  this window is running %s, the daemon is %s -- quit and reopen to pick up the new build",
		Version, m.daemonVersion))
}
