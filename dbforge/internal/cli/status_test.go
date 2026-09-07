package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/fiifiofosu/dbforge/internal/model"
)

func inst(id, status string, exit int) model.Instance {
	return model.Instance{
		ID: id, Engine: "postgres", Version: "16", Port: 15432,
		Status: model.Status(status), LastExitCode: exit,
	}
}

// The whole point of the widget's error handling: an unreachable daemon and an
// empty instance list are both "nothing there", and they must not look alike
// (spec 7, phase 4).
func TestOfflineAndEmptyAreDistinct(t *testing.T) {
	offline := buildWaybarStatus(nil, errors.New("cannot reach dbforged"))
	empty := buildWaybarStatus(nil, nil)

	if offline.Class == empty.Class {
		t.Fatalf("offline and empty share the class %q", offline.Class)
	}
	if offline.Text == empty.Text {
		t.Fatalf("offline and empty render the same text %q", offline.Text)
	}
	if !strings.Contains(offline.Tooltip, "not running") {
		t.Errorf("the offline tooltip does not say the daemon is down: %q", offline.Tooltip)
	}
	if !strings.Contains(offline.Tooltip, "systemctl") {
		t.Errorf("the offline tooltip does not say how to fix it: %q", offline.Tooltip)
	}
	if strings.Contains(empty.Tooltip, "not running") {
		t.Errorf("the empty tooltip wrongly claims the daemon is down: %q", empty.Tooltip)
	}
}

func TestRunningCountIsShown(t *testing.T) {
	st := buildWaybarStatus([]model.Instance{
		inst("a", "running", 0), inst("b", "running", 0), inst("c", "stopped", 0),
	}, nil)
	if st.Class != "running" {
		t.Fatalf("class = %q, want running", st.Class)
	}
	if !strings.Contains(st.Text, "2") {
		t.Fatalf("text %q does not show the running count of 2", st.Text)
	}
	if !strings.Contains(st.Tooltip, "2 running") || !strings.Contains(st.Tooltip, "1 stopped") {
		t.Fatalf("tooltip does not summarise counts: %q", st.Tooltip)
	}
}

// A degraded instance should pull the eye rather than being averaged away by
// a healthy running count.
func TestProblemsOverrideTheRunningCount(t *testing.T) {
	st := buildWaybarStatus([]model.Instance{
		inst("ok", "running", 0),
		inst("gone", "missing", 0),
	}, nil)
	if st.Class != "problem" {
		t.Fatalf("class = %q, want problem", st.Class)
	}
	if !strings.Contains(st.Tooltip, "need attention") {
		t.Fatalf("tooltip does not mention the problem: %q", st.Tooltip)
	}
}

func TestUncleanExitCountsAsAProblem(t *testing.T) {
	st := buildWaybarStatus([]model.Instance{inst("crashed", "stopped", 137)}, nil)
	if st.Class != "problem" {
		t.Fatalf("class = %q, want problem for an unclean exit", st.Class)
	}
}

func TestAllStoppedIsNotAProblem(t *testing.T) {
	st := buildWaybarStatus([]model.Instance{
		inst("a", "stopped", 0), inst("b", "stopped", 0),
	}, nil)
	if st.Class != "stopped" {
		t.Fatalf("class = %q, want stopped", st.Class)
	}
}

// With many instances the widget must summarise rather than render them all
// (spec 7, phase 4).
func TestManyInstancesAreSummarised(t *testing.T) {
	var many []model.Instance
	for i := 0; i < 40; i++ {
		many = append(many, inst(fmt.Sprintf("db-%02d", i), "running", 0))
	}
	st := buildWaybarStatus(many, nil)

	rows := strings.Count(st.Tooltip, "\n")
	if rows > maxTooltipRows+4 {
		t.Fatalf("tooltip has %d lines for 40 instances; it should summarise", rows)
	}
	if !strings.Contains(st.Tooltip, "and 28 more") {
		t.Fatalf("tooltip does not say how many were omitted: %q", st.Tooltip)
	}
	if !strings.Contains(st.Text, "40") {
		t.Fatalf("text %q lost the total count", st.Text)
	}
}

// A broken instance must not be the row that falls off the end of a long list.
func TestProblemsAreListedFirstWhenTruncated(t *testing.T) {
	var many []model.Instance
	for i := 0; i < 30; i++ {
		many = append(many, inst(fmt.Sprintf("z-%02d", i), "running", 0))
	}
	many = append(many, inst("zzz-broken", "missing", 0))

	st := buildWaybarStatus(many, nil)
	if !strings.Contains(st.Tooltip, "zzz-broken") {
		t.Fatal("the broken instance was truncated out of the tooltip")
	}
}

func TestWaybarOutputIsValidJSON(t *testing.T) {
	for _, tc := range []struct {
		name string
		list []model.Instance
		err  error
	}{
		{"offline", nil, errors.New("boom")},
		{"empty", nil, nil},
		{"running", []model.Instance{inst("a", "running", 0)}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(buildWaybarStatus(tc.list, tc.err))
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var back map[string]any
			if err := json.Unmarshal(b, &back); err != nil {
				t.Fatalf("waybar would fail to parse this: %v", err)
			}
			if _, ok := back["text"]; !ok {
				t.Error("waybar requires a text field")
			}
		})
	}
}

// --- right-click menu ---

func TestMenuLinesImplyTheActionFromState(t *testing.T) {
	lines := MenuLines([]model.Instance{
		inst("up", "running", 0),
		inst("down", "stopped", 0),
		inst("gone", "missing", 0),
	})
	joined := strings.Join(lines, "\n")

	for _, want := range []string{"Stop up", "Start down", "Forget gone"} {
		if !strings.Contains(joined, want) {
			t.Errorf("menu is missing %q\n%s", want, joined)
		}
	}
}

func TestMenuLinesAreParseableByTheShellScript(t *testing.T) {
	// The script does `awk '{print $2}'` for the verb and $3 for the id.
	for _, line := range MenuLines([]model.Instance{inst("app-db", "running", 0)}) {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			t.Fatalf("line %q has too few fields for the script to parse", line)
		}
		if fields[1] != "Stop" || fields[2] != "app-db" {
			t.Fatalf("field 2/3 = %q/%q, want Stop/app-db", fields[1], fields[2])
		}
	}
}

func TestMenuLinesAreSortedAndStable(t *testing.T) {
	lines := MenuLines([]model.Instance{
		inst("c", "running", 0), inst("a", "running", 0), inst("b", "running", 0),
	})
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3", len(lines))
	}
	if !strings.Contains(lines[0], " a ") || !strings.Contains(lines[2], " c ") {
		t.Fatalf("menu is not sorted by id: %v", lines)
	}
}

func TestMenuLinesEmptyForNoInstances(t *testing.T) {
	if got := MenuLines(nil); len(got) != 0 {
		t.Fatalf("got %v, want no lines", got)
	}
}
