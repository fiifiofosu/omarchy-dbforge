//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/fiifiofosu/dbforge/internal/daemon"
)

// dbctlPath finds the installed CLI, skipping when it is not built.
func dbctlPath(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("dbctl")
	if err != nil {
		t.Skip("dbctl is not on PATH; run make install")
	}
	return p
}

// The widget's payload must reflect what the daemon actually reports, against
// real containers rather than a fake.
func TestWidgetReflectsRealInstanceState(t *testing.T) {
	ctx := context.Background()
	m, _, _, _ := restartFixture(t)
	const id = "it-widget"
	t.Cleanup(func() { cleanup(t, m, id) })

	if _, err := m.Create(ctx, daemon.CreateOptions{Ref: "redis:7", ID: id, Start: true}); err != nil {
		t.Fatal(err)
	}

	// The CLI talks to the *installed* daemon, which uses the default scope,
	// so only assert on the shape of the payload rather than on this specific
	// instance appearing in it.
	out, err := exec.Command(dbctlPath(t), "status", "--waybar").Output()
	if err != nil {
		t.Fatalf("dbctl status --waybar: %v", err)
	}

	var payload struct {
		Text    string `json:"text"`
		Alt     string `json:"alt"`
		Class   string `json:"class"`
		Tooltip string `json:"tooltip"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("waybar could not parse this: %v\n%s", err, out)
	}
	if payload.Text == "" {
		t.Fatal("waybar requires a non-empty text field")
	}
	if payload.Class == "" {
		t.Fatal("no class; the stylesheet could not distinguish states")
	}
}

// The daemon-offline payload must be valid JSON and exit 0, or waybar renders
// nothing at all and the user sees an empty bar rather than a warning.
func TestWidgetOfflinePayloadIsStillValid(t *testing.T) {
	cmd := exec.Command(dbctlPath(t), "status", "--waybar")
	cmd.Env = append(cmd.Environ(), "DBFORGE_SOCKET=/nonexistent/dbforge.sock")

	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("status must exit 0 even when the daemon is down, got: %v", err)
	}

	var payload struct {
		Text    string `json:"text"`
		Class   string `json:"class"`
		Tooltip string `json:"tooltip"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("offline payload is not valid JSON: %v\n%s", err, out)
	}
	if payload.Class != "offline" {
		t.Fatalf("class = %q, want offline", payload.Class)
	}
	if !strings.Contains(payload.Tooltip, "systemctl") {
		t.Errorf("the offline tooltip does not say how to recover: %q", payload.Tooltip)
	}
}

// The right-click menu parses `dbctl status --menu` with awk, so the columns
// must stay where the script expects them.
func TestMenuOutputStaysParseable(t *testing.T) {
	out, err := exec.Command(dbctlPath(t), "status", "--menu").Output()
	if err != nil {
		t.Skipf("daemon unreachable: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			t.Fatalf("line %q has fewer than 3 fields; the menu script would mis-parse it", line)
		}
		switch fields[1] {
		case "Start", "Stop", "Forget":
		default:
			t.Fatalf("unexpected verb %q in %q", fields[1], line)
		}
	}
}

// A widget that takes seconds to answer stalls the whole bar, since waybar
// runs custom modules synchronously.
func TestWidgetRespondsQuickly(t *testing.T) {
	start := time.Now()
	if _, err := exec.Command(dbctlPath(t), "status", "--waybar").Output(); err != nil {
		t.Skipf("daemon unreachable: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("the widget took %s; waybar runs this synchronously", elapsed)
	}
}
