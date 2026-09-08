package tui

import (
	"strings"
	"testing"

	"github.com/fiifiofosu/dbforge/internal/model"
)

func headers(cols []column) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = c.header
	}
	return out
}

func has(cols []column, header string) bool {
	for _, c := range cols {
		if c.header == header {
			return true
		}
	}
	return false
}

func TestWideTerminalShowsEveryColumn(t *testing.T) {
	cols := visibleColumns(500, nil)
	for _, want := range []string{"ID", "ENGINE", "VERSION", "HOST", "PORT", "DATABASE", "PASSWORD", "STATUS", "RESTART", "UPTIME"} {
		if !has(cols, want) {
			t.Errorf("column %q missing at width 500: %v", want, headers(cols))
		}
	}
}

func TestNarrowTerminalKeepsTheAddressAndDropsTheRest(t *testing.T) {
	// Whatever else goes, what an instance is and how to reach it must stay.
	for _, width := range []int{40, 60, 72, 80, 100} {
		cols := visibleColumns(width, nil)
		for _, want := range []string{"ID", "PORT", "STATUS"} {
			if !has(cols, want) {
				t.Errorf("width %d dropped essential column %q: %v", width, want, headers(cols))
			}
		}
		if w := rowWidth(cols); w > width && len(cols) > 3 {
			t.Errorf("width %d: row is %d wide with columns %v", width, w, headers(cols))
		}
	}
}

func TestColumnsAreDroppedLeastUsefulFirst(t *testing.T) {
	// RESTART and UPTIME go before the connection details do.
	full := rowWidth(listColumns(nil))
	cols := visibleColumns(full-1, nil)
	if has(cols, "RESTART") {
		t.Errorf("RESTART should be the first column dropped: %v", headers(cols))
	}
	if !has(cols, "HOST") || !has(cols, "DATABASE") {
		t.Errorf("connection columns dropped too early: %v", headers(cols))
	}
}

func TestUnknownWidthAssumesRoom(t *testing.T) {
	// The first paint happens before the window size arrives; a table that
	// starts narrow and jumps wider looks broken.
	if got, want := len(visibleColumns(0, nil)), len(listColumns(nil)); got != want {
		t.Errorf("width 0 showed %d columns, want all %d", got, want)
	}
}

func TestDatabaseLabelPerEngine(t *testing.T) {
	cases := map[string]string{
		"postgres": "postgres",
		// MySQL and MariaDB start with no user database. That is the answer,
		// not a gap to fill with a guess.
		"mysql":   "-",
		"mariadb": "-",
		// Redis numbers its databases; a client that selects nothing is on 0.
		"redis": "0",
		// An engine this build no longer knows -- an old config read by a
		// newer DBForge -- must not panic or invent a name.
		"cassandra": "-",
		"":          "-",
	}
	for engine, want := range cases {
		if got := databaseLabel(model.Instance{Engine: engine}); got != want {
			t.Errorf("databaseLabel(%q) = %q, want %q", engine, got, want)
		}
	}
}

func TestRenderRowShowsHostAndDatabase(t *testing.T) {
	inst := model.Instance{
		ID: "pg", Engine: "postgres", Version: "18.6", Port: 15432,
		Status: model.StatusRunning, Restart: "always",
	}
	row := renderRow(visibleColumns(500, map[string]string{"pg": "s3cret-p4ssword"}), inst, styleRow)
	for _, want := range []string{"127.0.0.1", "15432", "postgres", "s3cret-p4ssword"} {
		if !strings.Contains(row, want) {
			t.Errorf("row %q lacks %q", row, want)
		}
	}
	if strings.HasSuffix(row, " ") {
		t.Errorf("row has trailing whitespace: %q", row)
	}
}

func TestPortColumnHandlesAnUnallocatedPort(t *testing.T) {
	// A half-created instance has no port yet; "0" would read as a real one.
	if got := itoa(0); got != "-" {
		t.Errorf("itoa(0) = %q, want -", got)
	}
	if got := itoa(15432); got != "15432" {
		t.Errorf("itoa(15432) = %q", got)
	}
}

func TestPasswordColumnDistinguishesItsThreeAnswers(t *testing.T) {
	known := map[string]string{"pg": "hunter2hunter2hunter2hunter2hunt"}
	cases := []struct {
		name string
		inst model.Instance
		want string
	}{
		// Fetched.
		{"known", model.Instance{ID: "pg", Engine: "postgres"}, known["pg"]},
		// Not fetched yet -- the list carries no passwords, so this is the
		// state every instance starts in.
		{"pending", model.Instance{ID: "other", Engine: "postgres"}, "..."},
		// Redis takes no password at all, which is an answer, not a gap.
		{"none", model.Instance{ID: "cache", Engine: "redis"}, "-"},
		// An engine this build does not know.
		{"unknown engine", model.Instance{ID: "x", Engine: "cassandra"}, "-"},
	}
	for _, c := range cases {
		if got := passwordLabel(c.inst, known); got != c.want {
			t.Errorf("%s: passwordLabel = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestPasswordIsShownWholeOrNotAtAll(t *testing.T) {
	// A truncated password can be neither copied nor checked, so the column
	// has to be wide enough for a generated one.
	pw := strings.Repeat("x", 32)
	cols := visibleColumns(500, map[string]string{"pg": pw})
	row := renderRow(cols, model.Instance{ID: "pg", Engine: "postgres"}, styleRow)
	if !strings.Contains(row, pw) {
		t.Errorf("password was truncated in %q", row)
	}
	// The invariant is not a particular width, it is that the column is never
	// a fragment: wherever it survives, the whole password is there, and where
	// it cannot fit it is gone.
	for _, width := range []int{40, 60, 72, 80, 90, 100, 120, 130, 200} {
		cols := visibleColumns(width, map[string]string{"pg": pw})
		row := renderRow(cols, model.Instance{ID: "pg", Engine: "postgres"}, styleRow)
		if has(cols, "PASSWORD") && !strings.Contains(row, pw) {
			t.Errorf("width %d: password truncated in %q", width, row)
		}
	}
	// On a terminal with no room for anything but the essentials, it goes.
	if cols := visibleColumns(50, map[string]string{"pg": pw}); has(cols, "PASSWORD") {
		t.Errorf("password column kept at width 50: %v", headers(cols))
	}
}
