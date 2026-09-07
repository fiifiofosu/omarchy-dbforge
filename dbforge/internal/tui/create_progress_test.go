package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/fiifiofosu/dbforge/internal/runtime"
)

// The submitting step used to say "creating... (pulling the image can take a
// while)" and nothing else, for however many minutes the pull took. It has to
// show what is happening now.
func TestProgressLineNamesThePhase(t *testing.T) {
	c := createModel{pullPhase: "downloading layers", pullLayers: 3, pullStarted: time.Now().Add(-42 * time.Second)}
	got := c.progressLine()

	for _, want := range []string{"downloading layers", "layer 3", "42s"} {
		if !strings.Contains(got, want) {
			t.Errorf("progress line is missing %q: %q", want, got)
		}
	}
}

// No percentage is claimed anywhere, because podman's API does not report one.
// An invented bar would be worse than none.
func TestProgressLineClaimsNoPercentage(t *testing.T) {
	c := createModel{pullPhase: "downloading layers", pullLayers: 2, pullStarted: time.Now()}
	if got := c.progressLine(); strings.Contains(got, "%") {
		t.Fatalf("progress line implies a percentage podman never reports: %q", got)
	}
}

// Before the first event arrives there is still a spinner and a word, rather
// than an empty line that reads as a hang.
func TestProgressLineSaysSomethingBeforeAnyEvent(t *testing.T) {
	got := createModel{}.progressLine()
	if strings.TrimSpace(got) == "" {
		t.Fatal("progress line is empty before the first event")
	}
	if !strings.Contains(got, "working") {
		t.Fatalf("expected a placeholder phase, got %q", got)
	}
}

// The layer counter is what tells a user a long download is advancing, so it
// must not go backwards when podman reports a phase without one.
func TestLayerCounterOnlyMovesForward(t *testing.T) {
	m := Model{}
	m.create.step = stepSubmitting

	for _, ev := range []runtime.PullEvent{
		{Message: "downloading layers", Layer: 1},
		{Message: "downloading layers", Layer: 2},
		{Message: "writing image"}, // no layer
	} {
		updated, _ := m.Update(pullMsg{ev: ev})
		m = updated.(Model)
	}

	if m.create.pullLayers != 2 {
		t.Fatalf("layer counter = %d, want 2 (it must not reset)", m.create.pullLayers)
	}
	if m.create.pullPhase != "writing image" {
		t.Fatalf("phase = %q, want the latest one", m.create.pullPhase)
	}
}

// The spinner must stop once the create is over, rather than ticking forever
// behind whatever view replaced the form.
func TestSpinnerStopsWhenNoLongerSubmitting(t *testing.T) {
	m := Model{}
	m.create.step = stepSubmitting
	if _, cmd := m.Update(spinMsg(time.Now())); cmd == nil {
		t.Fatal("spinner stopped while still submitting")
	}

	m.create.step = stepRestart
	if _, cmd := m.Update(spinMsg(time.Now())); cmd != nil {
		t.Fatal("spinner kept ticking after the create finished")
	}
}

// A TUI left open across an upgrade keeps running the old code while talking
// to the new daemon, so everything the upgrade added appears not to work. The
// window has to say so.
func TestStaleWindowIsCalledOut(t *testing.T) {
	m := Model{}
	Version = "0.6.1"
	t.Cleanup(func() { Version = "dev" })

	if got := m.staleNote(); got != "" {
		t.Fatalf("warned before the daemon version was known: %q", got)
	}

	updated, _ := m.Update(staleMsg{daemon: "0.7.0"})
	m = updated.(Model)

	note := m.staleNote()
	for _, want := range []string{"0.6.1", "0.7.0", "quit and reopen"} {
		if !strings.Contains(note, want) {
			t.Errorf("stale note is missing %q: %q", want, note)
		}
	}
}

// The note is persistent, not a transient status line: the consequence lasts
// until the window is reopened, so a message that scrolls away would be read
// once and puzzled over later.
func TestStaleNoteAppearsInTheListView(t *testing.T) {
	m := New(nil)
	Version = "0.6.1"
	t.Cleanup(func() { Version = "dev" })
	m.daemonVersion = "0.7.0"
	m.loaded = true

	if !strings.Contains(m.viewList(), "quit and reopen") {
		t.Fatalf("stale note absent from the list view:\n%s", m.viewList())
	}
}
