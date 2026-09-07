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
