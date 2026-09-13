package tui

import (
	"github.com/charmbracelet/lipgloss"

	"github.com/fiifiofosu/dbforge/internal/engines"
	"github.com/fiifiofosu/dbforge/internal/model"
)

// The list shows the pieces of a connection string as columns, because that
// is what people open DBForge to find out. Port was already there; host and
// database complete the address, so the only thing left to fetch from `c` is
// the password.
//
// Nine columns do not fit an 80-column terminal, so they are dropped in
// increasing order of usefulness once the width runs out -- rather than
// wrapping the row, which makes the table unreadable, or truncating it, which
// silently hides the rightmost column with no hint that it exists.

// column is one field of the instance table.
type column struct {
	header string
	// width is the column's fixed width. The last visible column is rendered
	// unpadded, so it can be as wide as its content needs.
	width int
	value func(model.Instance) string
	// colour, when set, styles the cell independently of the row (status is
	// coloured by severity, and stays coloured on the selected row).
	colour func(string) lipgloss.Style
	// essential columns are never dropped: an instance cannot be identified
	// or connected to without them.
	essential bool
	// priority orders dropping among non-essential columns, lowest first.
	priority int
}

// gutter is the single space between columns.
const gutter = 1

// cursorWidth is the "> " marker in front of every row.
const cursorWidth = 2

// passwordWidth fits a generated password (32 characters) whole. A truncated
// password is worse than none -- it cannot be copied and it cannot be checked
// -- so the column is either fully there or dropped, which is why it is the
// widest column and the last one to go.
const passwordWidth = 33

func listColumns(passwords map[string]string) []column {
	return []column{
		{header: "ID", width: 16, essential: true,
			value: func(i model.Instance) string { return i.ID }},
		{header: "ENGINE", width: 9, priority: 4,
			value: func(i model.Instance) string { return i.Engine }},
		{header: "VERSION", width: 8, priority: 3,
			value: func(i model.Instance) string { return i.Version }},
		{header: "HOST", width: 10, priority: 6,
			value: func(model.Instance) string { return engines.Host }},
		{header: "PORT", width: 6, essential: true,
			value: func(i model.Instance) string { return itoa(i.Port) }},
		{header: "DATABASE", width: 10, priority: 5,
			value: databaseLabel},
		{header: "PASSWORD", width: passwordWidth, priority: 7,
			value: func(i model.Instance) string { return passwordLabel(i, passwords) }},
		{header: "STATUS", width: 11, essential: true,
			value: statusLabel, colour: statusStyle},
		{header: "RESTART", width: 10, priority: 1,
			value: func(i model.Instance) string { return string(i.Restart) }},
		{header: "UPTIME", width: 7, priority: 2,
			value: uptimeLabel},
	}
}

// databaseLabel is the database an instance's connection string points at.
//
// The edge cases are all real: MySQL and MariaDB have no default database, an
// instance whose engine has been dropped from the catalogue (an old config
// read by a newer DBForge) has no answer at all, and Redis numbers its
// databases rather than naming them. All three read as "-" or the engine's own
// answer, never as a guess.
func databaseLabel(i model.Instance) string {
	eng, err := engines.Get(i.Engine)
	if err != nil || eng.Database == "" {
		return "-"
	}
	return eng.Database
}

// passwordLabel is the instance's generated password, as far as the TUI knows
// it yet.
//
// It is fetched per instance rather than arriving with the list, because the
// list response deliberately carries no passwords. So there are three answers,
// and they must not look alike: an engine that has no password, one whose
// password has not come back yet, and the password itself.
func passwordLabel(i model.Instance, passwords map[string]string) string {
	eng, err := engines.Get(i.Engine)
	if err != nil || !eng.NeedsPassword {
		return "-"
	}
	if pw, ok := passwords[i.ID]; ok {
		return pw
	}
	return "..."
}

// visibleColumns drops the least useful columns until the row fits width.
// A width of 0 means "unknown, assume room for everything": the first paint
// happens before Bubble Tea reports the window size, and a table that starts
// narrow and jumps wider looks broken.
func visibleColumns(width int, passwords map[string]string) []column {
	cols := listColumns(passwords)
	if width <= 0 {
		return cols
	}
	for rowWidth(cols) > width && len(cols) > 0 {
		drop := -1
		for i, c := range cols {
			if c.essential {
				continue
			}
			if drop == -1 || c.priority < cols[drop].priority {
				drop = i
			}
		}
		if drop == -1 {
			// Only essential columns left. They overflow rather than vanish:
			// a cramped table still beats one missing the port.
			break
		}
		cols = append(cols[:drop], cols[drop+1:]...)
	}
	return cols
}

// rowWidth is what a rendered row costs, including the cursor marker. The
// last column is unpadded, so it counts as its header rather than its width --
// anything longer is the caller's problem to truncate.
func rowWidth(cols []column) int {
	if len(cols) == 0 {
		return 0
	}
	w := cursorWidth
	for _, c := range cols[:len(cols)-1] {
		w += c.width + gutter
	}
	return w + len(cols[len(cols)-1].header)
}

// renderHeader lays out the column titles.
func renderHeader(cols []column) string {
	var b []byte
	b = append(b, ' ', ' ')
	for i, c := range cols {
		b = append(b, pad(c.header, c.width, i == len(cols)-1)...)
		if i < len(cols)-1 {
			b = append(b, ' ')
		}
	}
	return styleHeader.Render(string(b))
}

// renderRow lays out one instance, with rowStyle for the row as a whole and
// each column's own colour where it has one.
func renderRow(cols []column, inst model.Instance, rowStyle lipgloss.Style) string {
	var out []byte
	for i, c := range cols {
		last := i == len(cols)-1
		cell := pad(truncate(c.value(inst), c.width), c.width, last)
		style := rowStyle
		if c.colour != nil {
			style = c.colour(c.value(inst))
		}
		out = append(out, style.Render(cell)...)
		if !last {
			out = append(out, rowStyle.Render(" ")...)
		}
	}
	return string(out)
}

// pad left-aligns s in width columns. The last column is left unpadded so the
// row carries no trailing whitespace.
func pad(s string, width int, last bool) string {
	if last || len(s) >= width {
		return s
	}
	return s + spaces(width-len(s))
}

func spaces(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = ' '
	}
	return string(b)
}

func itoa(n int) string {
	if n == 0 {
		return "-"
	}
	digits := make([]byte, 0, 6)
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
