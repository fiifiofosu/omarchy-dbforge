package tui

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fiifiofosu/dbforge/internal/engines"
	"github.com/fiifiofosu/dbforge/internal/model"
	"github.com/fiifiofosu/dbforge/internal/update"
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

// The version list is the set of images pinned to a reviewed digest, so it is
// the same on every machine and needs no network. The form used to fetch it
// from Docker Hub and label a stale cache as offline; there is nothing left to
// be offline about.
func TestVersionListComesFromThePinnedSet(t *testing.T) {
	m := New(nil)
	m.view = viewCreate
	m.create = newCreateModel()
	m.create.step = stepVersion
	m.create.loadVersions(m.create.engine())

	want := engines.ApprovedVersions(m.create.engine())
	if len(want) == 0 {
		t.Fatalf("engine %q has no pinned versions", m.create.engine())
	}
	if !slices.Equal(m.create.versions, want) {
		t.Errorf("versions = %v, want the pinned set %v", m.create.versions, want)
	}

	out := m.viewCreate()
	if strings.Contains(out, "offline") || strings.Contains(out, "out of date") {
		t.Errorf("the form still warns about a stale list it can no longer have:\n%s", out)
	}
	for _, v := range want {
		if !strings.Contains(out, v) {
			t.Errorf("pinned version %q is not offered:\n%s", v, out)
		}
	}
}

// Every version the form offers must be one Create will accept. If these two
// ever disagree the user picks a version from a list and is then told it is
// not approved, which reads as a bug in the approval rather than in the list.
func TestEveryOfferedVersionIsCreatable(t *testing.T) {
	c := newCreateModel()
	for _, engine := range c.engines {
		c.engineIndex = slices.Index(c.engines, engine)
		c.loadVersions(engine)
		if len(c.versions) == 0 {
			t.Errorf("engine %q offers no versions", engine)
		}
		for _, v := range c.versions {
			if _, _, err := engines.ParseRef(engine + ":" + v); err != nil {
				t.Errorf("the form offers %s:%s but creating it fails: %v", engine, v, err)
			}
		}
	}
}

// A list loaded for an engine the user has moved away from must be discarded,
// or the form shows postgres versions under redis.
func TestVersionsForAnotherEngineAreIgnored(t *testing.T) {
	c := newCreateModel()
	current := c.engine()
	c.loadVersions("some-other-engine")
	if len(c.versions) != 0 {
		t.Fatalf("versions for a different engine were applied: %v", c.versions)
	}
	c.loadVersions(current)
	if len(c.versions) == 0 {
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

// The update screen reports; it must never offer to install. DBForge stopped
// replacing its own binaries because the release it would have installed was
// selected by a mutable pointer, and a "y to update" prompt is how that would
// quietly come back.
func TestUpdateScreenOffersNoInstall(t *testing.T) {
	m := New(nil)
	m.view = viewUpdate
	m.update = &updateState{step: updateChecking}
	m.width = 80

	next, _, handled := m.applyUpdateMessages(updateCheckedMsg{
		release: update.Release{Tag: "v99.0.0", Version: "99.0.0", URL: "https://example.test/r"},
	})
	if !handled {
		t.Fatal("the update screen ignored its own message")
	}
	m = next

	out := m.viewUpdate()
	if !strings.Contains(out, "99.0.0") {
		t.Errorf("the newer release is not reported:\n%s", out)
	}
	for _, forbidden := range []string{"y update", "installing", "Downloads", "SHA256SUMS"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("the update screen offers to install (%q):\n%s", forbidden, out)
		}
	}
	// It has to say what to do instead, or the user is told there is an update
	// and left with no way to get it.
	if !strings.Contains(out, "paru") && !strings.Contains(out, "pacman") &&
		!strings.Contains(out, "make install") {
		t.Errorf("the update screen names no way to install the release:\n%s", out)
	}

	// Every key on this screen goes back. There is nothing else to do here.
	for _, k := range []string{"y", "enter", "esc", "q"} {
		out, _ := m.updateUpdate(key(k))
		if out.(Model).view != viewList {
			t.Errorf("%q did not leave the update screen", k)
		}
	}
}
