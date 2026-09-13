package doctor

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/fiifiofosu/dbforge/internal/ports"
)

// healthy builds an Env describing a correctly configured machine. Each test
// breaks exactly one thing, which is the only way to be sure a check reports
// on what it claims to and not on a side effect of something else.
func healthy() Env {
	files := fstest.MapFS{
		"etc/subuid":                             {Data: []byte("dv:100000:65536\n")},
		"etc/subgid":                             {Data: []byte("dv:100000:65536\n")},
		"home/dv/.config/dbforge/instances.toml": {Data: []byte("schema_version = 1\n"), Mode: 0o600},
		"home/dv/.config/systemd/user/dbforged.service": {
			Data: []byte("[Service]\nExecStart=%h/.local/bin/dbforged\n"),
		},
		"home/dv/.local/bin/dbforged":  {Data: []byte("#!/bin/sh\n"), Mode: 0o755},
		"home/dv/.local/share/dbforge": {Mode: fs.ModeDir | 0o700},
	}
	return Env{
		Username: "dv",
		UID:      1000,
		HomeDir:  "/home/dv",
		LookPath: func(name string) (string, error) {
			switch name {
			case "podman", "newuidmap", "newgidmap":
				return "/usr/bin/" + name, nil
			}
			return "", errors.New("not found")
		},
		ReadFile: func(path string) ([]byte, error) { return readMap(files, path) },
		Stat:     func(path string) (os.FileInfo, error) { return statMap(files, path) },

		ConfigPath:   "/home/dv/.config/dbforge/instances.toml",
		DataRoot:     "/home/dv/.local/share/dbforge",
		DaemonSocket: "/run/user/1000/dbforge/dbforged.sock",

		PortRange: ports.Range{Low: 15000, High: 15099},
		PortFree:  func(int) error { return nil },

		PingPodman:        func(context.Context) error { return nil },
		PingDaemon:        func(context.Context) error { return nil },
		Linger:            func(string) (bool, error) { return true, nil },
		CgroupControllers: func(int) ([]string, error) { return []string{"cpu", "memory", "pids"}, nil },
		UnitPaths:         []string{"/home/dv/.config/systemd/user/dbforged.service"},
	}
}

// readMap and statMap adapt an fstest.MapFS to absolute paths, since every
// path doctor is given is absolute.
func readMap(m fstest.MapFS, path string) ([]byte, error) {
	f, ok := m[strings.TrimPrefix(path, "/")]
	if !ok {
		return nil, &os.PathError{Op: "open", Path: path, Err: os.ErrNotExist}
	}
	return f.Data, nil
}

func statMap(m fstest.MapFS, path string) (os.FileInfo, error) {
	name := strings.TrimPrefix(path, "/")
	if _, ok := m[name]; !ok {
		return nil, &os.PathError{Op: "stat", Path: path, Err: os.ErrNotExist}
	}
	return m.Stat(name)
}

// find returns the named check, failing the test if it is absent -- a check
// that silently stopped running is worse than one that reports wrongly.
func find(t *testing.T, r Report, name string) Check {
	t.Helper()
	for _, c := range r.Checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no check named %q in report; have: %s", name, names(r))
	return Check{}
}

func names(r Report) string {
	var out []string
	for _, c := range r.Checks {
		out = append(out, c.Name)
	}
	return strings.Join(out, ", ")
}

func run(env Env) Report { return Run(context.Background(), env) }

func TestHealthyMachinePassesEverything(t *testing.T) {
	rep := run(healthy())
	if rep.Failed() != 0 || rep.Warned() != 0 {
		for _, c := range rep.Checks {
			if c.Status != StatusOK {
				t.Errorf("%s: %s -- %s", c.Name, c.Status, c.Detail)
			}
		}
		t.Fatalf("a correctly configured machine reported %d failures and %d warnings",
			rep.Failed(), rep.Warned())
	}
}

func TestMissingPodmanFailsAndSkipsWhatDependsOnIt(t *testing.T) {
	env := healthy()
	env.LookPath = func(string) (string, error) { return "", errors.New("not found") }

	rep := run(env)
	if got := find(t, rep, "podman installed"); got.Status != StatusFail {
		t.Fatalf("status = %s, want fail", got.Status)
	}
	// The socket check must not add a second failure: there is one problem
	// here, and reporting it twice sends people down the wrong path.
	if got := find(t, rep, "podman socket"); got.Status != StatusSkip {
		t.Fatalf("podman socket = %s, want skip when podman is absent", got.Status)
	}
}

func TestDeadPodmanSocketSuggestsEnablingIt(t *testing.T) {
	env := healthy()
	env.PingPodman = func(context.Context) error { return errors.New("connection refused") }

	got := find(t, run(env), "podman socket")
	if got.Status != StatusFail {
		t.Fatalf("status = %s, want fail", got.Status)
	}
	if !strings.Contains(got.Fix, "podman.socket") {
		t.Fatalf("fix does not mention the socket unit: %q", got.Fix)
	}
}

// A dead daemon is only worth starting if podman works. Telling someone to
// start dbforged when podman is down wastes the one instruction they will read.
func TestDeadDaemonPointsAtPodmanFirstWhenPodmanIsDown(t *testing.T) {
	env := healthy()
	env.PingPodman = func(context.Context) error { return errors.New("refused") }
	env.PingDaemon = func(context.Context) error { return errors.New("refused") }

	got := find(t, run(env), "dbforged")
	if !strings.Contains(got.Fix, "podman") {
		t.Fatalf("fix should point at podman first, got %q", got.Fix)
	}

	env.PingPodman = func(context.Context) error { return nil }
	got = find(t, run(env), "dbforged")
	if !strings.Contains(got.Fix, "systemctl --user start dbforged") {
		t.Fatalf("with podman healthy the fix should start the daemon, got %q", got.Fix)
	}
}

func TestRunningAsRootFails(t *testing.T) {
	env := healthy()
	env.UID = 0
	if got := find(t, run(env), "running rootless"); got.Status != StatusFail {
		t.Fatalf("status = %s, want fail", got.Status)
	}
}

func TestMissingSubuidEntryFails(t *testing.T) {
	env := healthy()
	env.ReadFile = func(path string) ([]byte, error) {
		if path == "/etc/subuid" {
			return []byte("someoneelse:100000:65536\n"), nil
		}
		return healthy().ReadFile(path)
	}
	got := find(t, run(env), "subuid range")
	if got.Status != StatusFail {
		t.Fatalf("status = %s, want fail", got.Status)
	}
	if !strings.Contains(got.Fix, "usermod") {
		t.Fatalf("fix should say how to add the range, got %q", got.Fix)
	}
}

// A range that exists but is too short is a warning, not a failure: plenty of
// images run fine in it, and a hard failure would be wrong for those users.
func TestShortSubuidRangeWarns(t *testing.T) {
	env := healthy()
	env.ReadFile = func(path string) ([]byte, error) {
		if path == "/etc/subuid" {
			return []byte("dv:100000:1000\n"), nil
		}
		return healthy().ReadFile(path)
	}
	if got := find(t, run(env), "subuid range"); got.Status != StatusWarn {
		t.Fatalf("status = %s, want warn", got.Status)
	}
}

// Entries keyed by numeric uid are valid and do appear in the wild.
func TestSubuidByNumericUIDIsAccepted(t *testing.T) {
	env := healthy()
	env.ReadFile = func(path string) ([]byte, error) {
		if path == "/etc/subuid" || path == "/etc/subgid" {
			return []byte("1000:100000:65536\n"), nil
		}
		return healthy().ReadFile(path)
	}
	if got := find(t, run(env), "subuid range"); got.Status != StatusOK {
		t.Fatalf("status = %s, want ok: %s", got.Status, got.Detail)
	}
}

func TestUndelegatedCgroupsWarnRatherThanFail(t *testing.T) {
	env := healthy()
	env.CgroupControllers = func(int) ([]string, error) { return []string{"pids"}, nil }

	got := find(t, run(env), "cgroup delegation")
	// Instances still run without delegation; only resource limits are lost.
	// Failing here would tell people their install is broken when it is not.
	if got.Status != StatusWarn {
		t.Fatalf("status = %s, want warn", got.Status)
	}
	for _, want := range []string{"cpu", "memory"} {
		if !strings.Contains(got.Detail, want) {
			t.Errorf("detail should name the missing controller %q: %s", want, got.Detail)
		}
	}
}

func TestExhaustedPortRangeFails(t *testing.T) {
	env := healthy()
	env.PortFree = func(int) error { return errors.New("address in use") }
	if got := find(t, run(env), "port range"); got.Status != StatusFail {
		t.Fatalf("status = %s, want fail", got.Status)
	}
}

func TestMostlyFullPortRangeWarns(t *testing.T) {
	env := healthy()
	env.PortFree = func(p int) error {
		if p%10 == 0 { // one in ten free
			return nil
		}
		return errors.New("address in use")
	}
	if got := find(t, run(env), "port range"); got.Status != StatusWarn {
		t.Fatalf("status = %s, want warn", got.Status)
	}
}

// The Phase 5 trap: install from source, then install the package. The
// per-user unit wins and still points into ~/.local/bin, and systemd reports
// only a bare status 203.
func TestUnitPointingAtAMissingBinaryFails(t *testing.T) {
	env := healthy()
	env.Stat = func(path string) (os.FileInfo, error) {
		if path == "/home/dv/.local/bin/dbforged" {
			return nil, &os.PathError{Op: "stat", Path: path, Err: os.ErrNotExist}
		}
		return healthy().Stat(path)
	}
	got := find(t, run(env), "systemd unit")
	if got.Status != StatusFail {
		t.Fatalf("status = %s, want fail", got.Status)
	}
	if !strings.Contains(got.Detail, "does not exist") {
		t.Fatalf("detail should say what is missing: %q", got.Detail)
	}
}

// %h is not a directory. A unit written with specifiers -- as DBForge's own
// was before Phase 5 -- must not be reported as broken.
func TestUnitWithSystemdSpecifiersResolves(t *testing.T) {
	if got := find(t, run(healthy()), "systemd unit"); got.Status != StatusOK {
		t.Fatalf("status = %s, want ok: %s", got.Status, got.Detail)
	}
}

func TestMissingUnitWarns(t *testing.T) {
	env := healthy()
	env.UnitPaths = []string{"/nowhere/dbforged.service"}
	// Not installing the unit is a choice; dbctl works fine without it. Only
	// starting at login is lost.
	if got := find(t, run(env), "systemd unit"); got.Status != StatusWarn {
		t.Fatalf("status = %s, want warn", got.Status)
	}
}

func TestLingeringOffWarnsWithTheFix(t *testing.T) {
	env := healthy()
	env.Linger = func(string) (bool, error) { return false, nil }

	got := find(t, run(env), "user lingering")
	if got.Status != StatusWarn {
		t.Fatalf("status = %s, want warn", got.Status)
	}
	if !strings.Contains(got.Fix, "enable-linger") {
		t.Fatalf("fix should give the command: %q", got.Fix)
	}
}

func TestWorldReadableConfigWarns(t *testing.T) {
	env := healthy()
	env.Stat = func(path string) (os.FileInfo, error) {
		if path == env.ConfigPath {
			return fakeInfo{mode: 0o644}, nil
		}
		return healthy().Stat(path)
	}
	got := find(t, run(env), "config file")
	if got.Status != StatusWarn {
		t.Fatalf("status = %s, want warn", got.Status)
	}
	if !strings.Contains(got.Fix, "chmod") {
		t.Fatalf("fix should give the chmod: %q", got.Fix)
	}
}

// A missing config or data directory is normal before the first instance.
func TestNothingCreatedYetIsNotAProblem(t *testing.T) {
	env := healthy()
	env.ReadFile = func(path string) ([]byte, error) {
		if path == env.ConfigPath {
			return nil, &os.PathError{Op: "open", Path: path, Err: os.ErrNotExist}
		}
		return healthy().ReadFile(path)
	}
	env.Stat = func(path string) (os.FileInfo, error) {
		if path == env.DataRoot {
			return nil, &os.PathError{Op: "stat", Path: path, Err: os.ErrNotExist}
		}
		return healthy().Stat(path)
	}
	rep := run(env)
	for _, name := range []string{"config file", "data directory"} {
		if got := find(t, rep, name); got.Status != StatusOK {
			t.Errorf("%s = %s on a fresh install, want ok: %s", name, got.Status, got.Detail)
		}
	}
}

// Every non-ok check must say what to do about it. A diagnosis without a fix
// is just a complaint.
func TestEveryProblemCarriesAFix(t *testing.T) {
	env := healthy()
	env.LookPath = func(string) (string, error) { return "", errors.New("nope") }
	env.UID = 0
	env.PortFree = func(int) error { return errors.New("in use") }
	env.Linger = func(string) (bool, error) { return false, nil }
	env.CgroupControllers = func(int) ([]string, error) { return nil, nil }

	for _, c := range run(env).Checks {
		if c.Status == StatusFail || c.Status == StatusWarn {
			if strings.TrimSpace(c.Fix) == "" {
				t.Errorf("check %q is %s but offers no fix", c.Name, c.Status)
			}
		}
	}
}

type fakeInfo struct{ mode os.FileMode }

func (f fakeInfo) Name() string       { return "instances.toml" }
func (f fakeInfo) Size() int64        { return 0 }
func (f fakeInfo) Mode() os.FileMode  { return f.mode }
func (f fakeInfo) ModTime() time.Time { return time.Time{} }
func (f fakeInfo) IsDir() bool        { return f.mode.IsDir() }
func (f fakeInfo) Sys() any           { return nil }
