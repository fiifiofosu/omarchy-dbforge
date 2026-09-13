package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// buildTags mirrors GOTAGS in the Makefile. Keep them in step.
const buildTags = "containers_image_openpgp,exclude_graphdriver_btrfs"

// The CLI must not link the TUI.
//
// Bubble Tea's package init calls lipgloss.HasDarkBackground(), which queries
// the terminal and blocks for termenv's 5-second OSCTimeout when the terminal
// does not answer. Because that runs at import time, simply linking the TUI
// into this binary made `dbctl list` take 5.03s on such a terminal instead of
// 0.05s. The TUI therefore lives in cmd/dbforge-tui, and this test keeps it
// there.
func TestCLIDoesNotLinkTheTUI(t *testing.T) {
	// Inspect the graph the real build produces: different tags, different
	// dependencies, and this test would be answering about a binary nobody
	// ships. buildTags mirrors GOTAGS in the Makefile.
	out, err := exec.Command("go", "list", "-deps", "-tags", buildTags, ".").Output()
	if err != nil {
		t.Skipf("cannot run go list: %v", err)
	}
	for _, banned := range []string{
		"github.com/charmbracelet/bubbletea",
		"github.com/fiifiofosu/dbforge/internal/tui",
	} {
		if strings.Contains(string(out), banned) {
			t.Errorf("cmd/dbforge depends on %s; that costs every CLI invocation "+
				"a terminal query at startup. Keep the TUI in cmd/dbforge-tui.", banned)
		}
	}
}

// TestBuildsWithoutCgo is the guard for the tags in buildTags.
//
// Podman's bindings pull in gpgme and the btrfs graph driver, both cgo
// packages needing C headers (gpgme.h, btrfs/version.h). A developer machine
// with podman installed has those headers, so dropping the tags breaks
// nothing locally and then fails on every CI runner and in every clean build
// chroot. Building with cgo disabled proves the tags are still doing their
// job, on any machine.
func TestBuildsWithoutCgo(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles the whole module")
	}
	cmd := exec.Command("go", "build", "-tags", buildTags, "-o", os.DevNull, "./...")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	cmd.Dir = ".."
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the build needs C headers, which a CI runner will not have.\n"+
			"Check the tags in buildTags (%s) against the Makefile's GOTAGS.\n%s",
			buildTags, out)
	}
}

// TestNoTelemetryIsLinked backs the promise in the README (spec 7, phase 6:
// telemetry-free by default, opt-in only if ever added).
//
// It cannot assert OpenTelemetry is absent: podman's bindings instrument their
// HTTP client with it, so otel's *API* is in the graph either way. What makes
// that harmless is the absence of an exporter and of the SDK's tracer
// provider -- without those, every span is recorded into a no-op and nothing
// can leave the machine. That is the property worth pinning, because it is the
// one a careless dependency bump could quietly reverse.
func TestNoTelemetryIsLinked(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "-tags", buildTags, "./...").Output()
	if err != nil {
		t.Skipf("cannot run go list: %v", err)
	}
	for _, banned := range []string{
		"go.opentelemetry.io/otel/exporters", // anything that ships spans off-box
		"go.opentelemetry.io/otel/sdk/trace", // the provider that would drive one
		"github.com/getsentry/sentry-go",
		"gopkg.in/segmentio/analytics-go",
		"github.com/posthog/posthog-go",
	} {
		if strings.Contains(string(out), banned) {
			t.Errorf("%s is linked into dbforge. DBForge sends nothing anywhere; "+
				"if that is changing it must be opt-in and documented.", banned)
		}
	}
}
