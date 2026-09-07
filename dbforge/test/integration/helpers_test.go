//go:build integration

package integration

import (
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fiifiofosu/dbforge/internal/daemon"
	"github.com/fiifiofosu/dbforge/internal/ports"
)

// restartPodmanSocket bounces the user-level podman socket, approximating what
// a suspend/resume cycle does to it. Skips when systemd is not managing it.
func restartPodmanSocket(t *testing.T) {
	t.Helper()
	if err := exec.Command("systemctl", "--user", "is-active", "podman.socket").Run(); err != nil {
		t.Skip("podman.socket is not systemd-managed here")
	}
	if out, err := exec.Command("systemctl", "--user", "restart", "podman.socket").CombinedOutput(); err != nil {
		t.Skipf("cannot restart podman.socket: %v: %s", err, out)
	}
	time.Sleep(2 * time.Second)
}

// testScope gives each test its own DBForge scope, so a test run never adopts
// containers belonging to the developer's real installation.
func testScope(t *testing.T) string {
	t.Helper()
	return "test-" + strings.ToLower(strings.NewReplacer("/", "-", " ", "-").Replace(t.Name()))
}

// purgeScope removes any container left behind by a previous run of this test.
//
// Scopes are derived from the test name and so are stable across runs, which
// means an interrupted run leaves containers that the next run adopts -- and
// then fails with "instance already exists", or worse, cascades into unrelated
// tests by holding their ports. Tests must not depend on the previous run
// having exited cleanly.
func purgeScope(t *testing.T, scope string) {
	t.Helper()
	out, err := exec.Command("podman", "ps", "-aq",
		"--filter", "label=io.dbforge.scope="+scope).Output()
	if err != nil {
		return
	}
	for _, id := range strings.Fields(string(out)) {
		if err := exec.Command("podman", "rm", "-f", id).Run(); err != nil {
			t.Logf("could not remove stale container %s: %v", id, err)
		}
	}
}

// portBlock hands each manager its own slice of the test port range.
//
// Every allocator starts scanning at its range floor, so managers sharing a
// range keep handing out the same few ports. Sequential tests then race the
// previous test's teardown: podman's rootlessport forwarder can still be
// listening on a port whose container is already gone, which shows up as a
// connection that is accepted and then immediately reset. Disjoint blocks
// remove the whole class of failure.
var nextPortLow = int32(testPortLow)

const (
	testPortLow  = 15700
	blockSize    = 20
	testPortHigh = 15999
)

func portBlock(t *testing.T) ports.Range {
	t.Helper()
	low := int(atomic.AddInt32(&nextPortLow, blockSize)) - blockSize
	if low+blockSize-1 > testPortHigh {
		t.Fatalf("test port range %d-%d exhausted", testPortLow, testPortHigh)
	}
	return ports.Range{Low: low, High: low + blockSize - 1}
}

// TestMain clears out containers left by any earlier run of this suite before
// the first test starts. Scopes are derived from test names and so are stable
// across runs: an interrupted run leaves containers that the next run would
// otherwise adopt, or that would hold ports unrelated tests need. Only scopes
// prefixed "test-" are touched, never a developer's real "default" scope.
func TestMain(m *testing.M) {
	purgeTestScopes()
	code := m.Run()
	// Also on the way out, so a failed run does not poison the next one.
	purgeTestScopes()
	os.Exit(code)
}

func purgeTestScopes() {
	out, err := exec.Command("podman", "ps", "-aq",
		"--filter", "label="+daemon.ScopeLabel).Output()
	if err != nil {
		return
	}
	for _, id := range strings.Fields(string(out)) {
		scope, err := exec.Command("podman", "inspect", id,
			"--format", "{{index .Config.Labels \""+daemon.ScopeLabel+"\"}}").Output()
		if err != nil || !strings.HasPrefix(strings.TrimSpace(string(scope)), "test-") {
			continue
		}
		_ = exec.Command("podman", "rm", "-f", id).Run()
	}
}
