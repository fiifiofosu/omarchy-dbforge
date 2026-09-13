package cli

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/fiifiofosu/dbforge/internal/doctor"
)

func TestPrintReportShowsFixesForProblemsOnly(t *testing.T) {
	rep := doctor.Report{Checks: []doctor.Check{
		{Name: "podman installed", Status: doctor.StatusOK, Detail: "/usr/bin/podman"},
		{Name: "user lingering", Status: doctor.StatusWarn, Detail: "off",
			Fix: "sudo loginctl enable-linger dv"},
		{Name: "podman socket", Status: doctor.StatusFail, Detail: "refused",
			Fix: "systemctl --user enable --now podman.socket"},
	}}

	out := capture(t, func(w *os.File) { printReport(w, rep) })

	for _, want := range []string{
		"podman installed", "/usr/bin/podman",
		"sudo loginctl enable-linger dv",
		"systemctl --user enable --now podman.socket",
		"1 failed, 1 warnings",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output is missing %q:\n%s", want, out)
		}
	}
}

// The all-clear has to be unmistakable: someone running doctor on a healthy
// machine should not have to read thirteen lines to learn nothing is wrong.
func TestPrintReportSaysSoWhenEverythingPasses(t *testing.T) {
	rep := doctor.Report{Checks: []doctor.Check{
		{Name: "podman installed", Status: doctor.StatusOK, Detail: "/usr/bin/podman"},
	}}
	if out := capture(t, func(w *os.File) { printReport(w, rep) }); !strings.Contains(out, "All checks passed") {
		t.Fatalf("no all-clear in:\n%s", out)
	}
}

// Warnings alone must not read as failure, or people start ignoring them.
func TestWarningsAloneDoNotReadAsFailure(t *testing.T) {
	rep := doctor.Report{Checks: []doctor.Check{
		{Name: "user lingering", Status: doctor.StatusWarn, Detail: "off", Fix: "enable it"},
	}}
	out := capture(t, func(w *os.File) { printReport(w, rep) })
	if !strings.Contains(out, "No failures") {
		t.Fatalf("a warning-only report should say there are no failures:\n%s", out)
	}
}

// The JSON shape is a contract: it is what a bug-report template or a script
// would parse.
func TestReportMarshalsToStableJSON(t *testing.T) {
	rep := doctor.Report{Checks: []doctor.Check{
		{Name: "podman socket", Status: doctor.StatusFail, Detail: "refused", Fix: "start it"},
		{Name: "running rootless", Status: doctor.StatusOK, Detail: "uid 1000"},
	}}
	raw, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}

	var back struct {
		Checks []struct {
			Name, Status, Detail, Fix string
		}
	}
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("round trip: %v", err)
	}
	if len(back.Checks) != 2 {
		t.Fatalf("got %d checks, want 2", len(back.Checks))
	}
	if back.Checks[0].Status != "fail" || back.Checks[0].Fix != "start it" {
		t.Fatalf("first check did not survive: %+v", back.Checks[0])
	}
	// An ok check has nothing to fix, and the field should be absent rather
	// than an empty string a consumer has to special-case.
	if strings.Contains(string(raw), `"fix":""`) {
		t.Fatalf("empty fix should be omitted:\n%s", raw)
	}
}

// capture runs f with a pipe standing in for a terminal and returns what was
// written. printReport takes an *os.File so it can be pointed at one.
func capture(t *testing.T, f func(*os.File)) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan string, 1)
	go func() {
		var b strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			b.Write(buf[:n])
			if err != nil {
				break
			}
		}
		done <- b.String()
	}()
	f(w)
	w.Close()
	out := <-done
	r.Close()
	return out
}
