package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fiifiofosu/dbforge/internal/model"
	"github.com/fiifiofosu/dbforge/internal/registry"
)

// "Daemon unreachable" and "no instances" must not render the same. They look
// identical otherwise, and the fixes are completely different.
func TestDaemonOfflineRendersDistinctlyFromEmpty(t *testing.T) {
	offline := New(nil)
	offline.loaded = true
	offline.daemonErr = errors.New("cannot reach dbforged at /run/user/1000/dbforge/dbforged.sock")
	offlineView := offline.viewList()

	empty := New(nil)
	empty.loaded = true
	emptyView := empty.viewList()

	if !strings.Contains(offlineView, "daemon unreachable") {
		t.Error("the offline view does not say the daemon is unreachable")
	}
	if strings.Contains(emptyView, "unreachable") {
		t.Error("the empty view wrongly claims the daemon is unreachable")
	}
	if !strings.Contains(emptyView, "No instances") {
		t.Error("the empty view does not say there are no instances")
	}
	if offlineView == emptyView {
		t.Fatal("offline and empty render identically")
	}
}

func TestListShowsStatusRestartAndUptime(t *testing.T) {
	m := New(nil)
	m.loaded = true
	m.instances = []model.Instance{{
		ID: "app-db", Engine: "postgres", Version: "16", Port: 15432,
		Status: model.StatusRunning, Restart: model.RestartAlways,
		StartedAt: time.Now().Add(-90 * time.Minute),
	}}

	out := m.viewList()
	for _, want := range []string{"app-db", "postgres", "16", "15432", "running", "always", "1h30m"} {
		if !strings.Contains(out, want) {
			t.Errorf("list view is missing %q\n%s", want, out)
		}
	}
}

func TestUncleanExitIsVisibleInTheList(t *testing.T) {
	m := New(nil)
	m.loaded = true
	m.instances = []model.Instance{{
		ID: "crashed", Engine: "redis", Version: "7", Port: 15379,
		Status: model.StatusStopped, LastExitCode: 137, Restart: model.RestartAlways,
	}}
	if out := m.viewList(); !strings.Contains(out, "exited(137)") {
		t.Errorf("an unclean exit is not surfaced in the list:\n%s", out)
	}
}

func TestUptimeLabel(t *testing.T) {
	cases := []struct {
		name string
		inst model.Instance
		want string
	}{
		{"stopped", model.Instance{Status: model.StatusStopped}, "-"},
		{"no start time", model.Instance{Status: model.StatusRunning}, "-"},
		{"seconds", model.Instance{Status: model.StatusRunning, StartedAt: time.Now().Add(-30 * time.Second)}, "30s"},
		{"minutes", model.Instance{Status: model.StatusRunning, StartedAt: time.Now().Add(-5 * time.Minute)}, "5m"},
		{"days", model.Instance{Status: model.StatusRunning, StartedAt: time.Now().Add(-50 * time.Hour)}, "2d2h"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := uptimeLabel(c.inst); got != c.want {
				t.Errorf("uptimeLabel = %q, want %q", got, c.want)
			}
		})
	}
}

func TestCursorStaysInRangeWhenInstancesDisappear(t *testing.T) {
	m := New(nil)
	m.instances = make([]model.Instance, 5)
	m.cursor = 4

	out, _ := m.Update(instancesMsg{instances: make([]model.Instance, 2)})
	m = out.(Model)
	if m.cursor >= len(m.instances) {
		t.Fatalf("cursor = %d with %d instances; it must be clamped", m.cursor, len(m.instances))
	}
}

func TestPollingPausesUnderAModal(t *testing.T) {
	// Re-polling under a confirmation dialog would move the selection out from
	// under the user mid-decision.
	m := New(nil)
	m.view = viewConfirmDestroy
	m.instances = []model.Instance{testInstance()}

	out, _ := m.Update(tickMsg(time.Now()))
	got := out.(Model)
	if got.view != viewConfirmDestroy {
		t.Fatal("a tick changed the view out from under a modal")
	}
}

// Creating while offline must show a cached list and say so (spec 7 phase 3).
func TestOfflineVersionListIsLabelled(t *testing.T) {
	m := New(nil)
	m.view = viewCreate
	m.create = newCreateModel()
	m.create.step = stepVersion
	m.create.applyVersions(m.create.engine(), registry.Result{
		Versions:  []string{"16", "15"},
		Source:    registry.SourceCache,
		FetchedAt: time.Now().Add(-48 * time.Hour),
		Err:       errors.New("dial tcp: no route to host"),
	})

	out := m.viewCreate()
	if !strings.Contains(out, "offline") {
		t.Errorf("a cached version list is not labelled as offline:\n%s", out)
	}
	if !strings.Contains(out, "cached") {
		t.Errorf("the view does not say the list is cached:\n%s", out)
	}
	if !strings.Contains(out, "no route to host") {
		t.Errorf("the view does not explain why it fell back:\n%s", out)
	}
}

func TestLiveVersionListHasNoOfflineWarning(t *testing.T) {
	m := New(nil)
	m.view = viewCreate
	m.create = newCreateModel()
	m.create.step = stepVersion
	m.create.applyVersions(m.create.engine(), registry.Result{
		Versions: []string{"16"}, Source: registry.SourceLive, FetchedAt: time.Now(),
	})
	if out := m.viewCreate(); strings.Contains(out, "offline") {
		t.Errorf("a live version list was labelled offline:\n%s", out)
	}
}

// A version list arriving for an engine the user has moved away from must be
// discarded, or the form shows postgres versions under redis.
func TestStaleVersionResponseIsIgnored(t *testing.T) {
	c := newCreateModel()
	current := c.engine()
	c.applyVersions("some-other-engine", registry.Result{
		Versions: []string{"999"}, Source: registry.SourceLive,
	})
	if len(c.versions) != 0 {
		t.Fatalf("versions for a different engine were applied: %v", c.versions)
	}
	c.applyVersions(current, registry.Result{Versions: []string{"16"}, Source: registry.SourceLive})
	if len(c.versions) != 1 {
		t.Fatal("versions for the current engine were not applied")
	}
}

func TestCreateFormRejectsDuplicateNameBeforeCallingTheDaemon(t *testing.T) {
	m := New(nil)
	m.instances = []model.Instance{{ID: "taken"}}
	m.create = newCreateModel()
	m.create.step = stepName
	m.create.name = "taken"

	out, cmd := m.advanceCreate()
	got := out.(Model)
	if cmd != nil {
		t.Fatal("a duplicate name was sent to the daemon instead of being caught locally")
	}
	if got.create.err == nil {
		t.Fatal("no error shown for a duplicate name")
	}
}

func TestCreateFormRejectsEmptyName(t *testing.T) {
	m := New(nil)
	m.create = newCreateModel()
	m.create.step = stepName
	m.create.name = "   "

	out, cmd := m.advanceCreate()
	if cmd != nil {
		t.Fatal("an empty name advanced the form")
	}
	if out.(Model).create.err == nil {
		t.Fatal("no error shown for an empty name")
	}
}

func TestCreateFormRejectsOutOfRangePort(t *testing.T) {
	for _, port := range []string{"0", "70000"} {
		m := New(nil)
		m.create = newCreateModel()
		m.create.step = stepPort
		m.create.port = port

		out, cmd := m.advanceCreate()
		if cmd != nil {
			t.Fatalf("port %q advanced the form", port)
		}
		if out.(Model).create.err == nil {
			t.Fatalf("no error shown for port %q", port)
		}
	}
}

func TestCreateFormAcceptsEmptyPortAsAuto(t *testing.T) {
	m := New(nil)
	m.create = newCreateModel()
	m.create.step = stepPort
	m.create.port = ""

	out, _ := m.advanceCreate()
	got := out.(Model)
	if got.create.err != nil {
		t.Fatalf("an empty port was rejected: %v", got.create.err)
	}
	if got.create.step != stepRestart {
		t.Fatal("the form did not advance past the port step")
	}
}

func TestFailedCreateKeepsTheForm(t *testing.T) {
	// Losing everything the user typed because the daemon said no would be
	// hostile; the form must stay put with the error shown.
	m := New(nil)
	m.view = viewCreate
	m.create = newCreateModel()
	m.create.step = stepSubmitting
	m.create.name = "app-db"

	out, _ := m.Update(createdMsg{err: errors.New("port 5432 is in use")})
	got := out.(Model)

	if got.view != viewCreate {
		t.Fatal("the form was closed after a failed create")
	}
	if got.create.name != "app-db" {
		t.Fatal("the typed name was lost")
	}
	if got.create.err == nil {
		t.Fatal("the failure was not shown on the form")
	}
}

func TestSuccessfulCreateReturnsToTheList(t *testing.T) {
	m := New(nil)
	m.view = viewCreate
	m.create.step = stepSubmitting

	out, _ := m.Update(createdMsg{inst: model.Instance{ID: "new-db", Port: 15432}})
	got := out.(Model)
	if got.view != viewList {
		t.Fatal("a successful create did not return to the list")
	}
	if !strings.Contains(got.status, "new-db") {
		t.Fatalf("status = %q, want it to mention the new instance", got.status)
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("short", 10); got != "short" {
		t.Errorf("truncate widened a short string: %q", got)
	}
	if got := truncate("averylongidentifier", 8); len([]rune(got)) != 8 {
		t.Errorf("truncate produced %q, want 8 runes", got)
	}
}
