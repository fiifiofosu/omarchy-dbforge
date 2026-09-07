//go:build integration

package integration

import (
	"os/exec"
	"strings"
	"testing"
	"time"
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
