package runtime

import (
	"reflect"
	"testing"
)

// collect runs the writer over a script of Write calls and returns the events.
func collect(t *testing.T, writes ...string) []PullEvent {
	t.Helper()
	var got []PullEvent
	w := &pullProgressWriter{emit: func(ev PullEvent) { got = append(got, ev) }}
	for _, s := range writes {
		if _, err := w.Write([]byte(s)); err != nil {
			t.Fatal(err)
		}
	}
	return got
}

func TestPullProgressClassifiesPodmansPhases(t *testing.T) {
	got := collect(t,
		"Trying to pull docker.io/library/redis:8...\n",
		"Getting image source signatures\n",
		"Copying blob sha256:6310eb16bf42\n",
		"Copying blob sha256:9b30195f536d\n",
		"Copying config sha256:923b31bc216c\n",
		"Writing manifest to image destination\n",
	)
	want := []PullEvent{
		{Message: "contacting registry"},
		{Message: "checking signatures"},
		{Message: "downloading layers", Layer: 1},
		{Message: "downloading layers", Layer: 2},
		{Message: "downloading config", Layer: 2},
		{Message: "writing image", Layer: 2},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
}

// Nothing guarantees one Write per line. A line split across writes must not
// produce an event with half a phase name in it.
func TestPullProgressReassemblesSplitLines(t *testing.T) {
	got := collect(t, "Copying bl", "ob sha256:abc\nCopying blob sha256:def\n")
	want := []PullEvent{
		{Message: "downloading layers", Layer: 1},
		{Message: "downloading layers", Layer: 2},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
}

// A write with no newline yet must emit nothing rather than a partial phase.
func TestPullProgressHoldsAnIncompleteLine(t *testing.T) {
	if got := collect(t, "Copying blob sha25"); len(got) != 0 {
		t.Fatalf("emitted %+v before the line was complete", got)
	}
}

// Podman knows more about what it is doing than the switch does, so an
// unrecognised line is still worth showing rather than swallowing.
func TestPullProgressPassesUnknownLinesThrough(t *testing.T) {
	got := collect(t, "Copying blob sha256:abc\n", "Some future podman phase\n")
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2: %+v", len(got), got)
	}
	if got[1].Message != "Some future podman phase" {
		t.Fatalf("unknown line was not passed through: %q", got[1].Message)
	}
	// ...and it keeps the layer count, so the counter does not reset to zero
	// the moment podman says something new.
	if got[1].Layer != 1 {
		t.Fatalf("layer counter lost on an unknown line: %+v", got[1])
	}
}

func TestPullProgressIgnoresBlankLines(t *testing.T) {
	if got := collect(t, "\n", "   \n", "Writing manifest to image destination\n"); len(got) != 1 {
		t.Fatalf("blank lines produced events: %+v", got)
	}
}
