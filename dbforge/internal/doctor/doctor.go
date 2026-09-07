// Package doctor diagnoses a DBForge installation.
//
// Everything here answers one question: why is this not working? Rootless
// containers fail in a handful of specific, non-obvious ways -- a missing
// subuid range, a systemd user manager that was never told to linger, cgroup
// controllers that were not delegated -- and each of them produces a symptom
// far away from its cause. Each check below exists because it was, at some
// point, the actual answer.
//
// The checks never mutate anything. `dbctl doctor` is safe to run at any time,
// including on a machine where nothing works.
package doctor

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fiifiofosu/dbforge/internal/ports"
)

// Status is the outcome of one check.
type Status string

const (
	// StatusOK means the check found what it wanted.
	StatusOK Status = "ok"
	// StatusWarn means something is not ideal but DBForge will work. A warning
	// never fails the command, or `doctor` becomes noise people stop reading.
	StatusWarn Status = "warn"
	// StatusFail means DBForge is broken, or will be, until this is fixed.
	StatusFail Status = "fail"
	// StatusSkip means the check could not run -- usually because something it
	// depends on already failed. Reporting it as a failure would turn one real
	// problem into a screen of derived ones.
	StatusSkip Status = "skip"
)

// Check is one diagnosis.
type Check struct {
	Name   string `json:"name"`
	Status Status `json:"status"`
	// Detail is what was actually observed, not a restatement of the name.
	Detail string `json:"detail"`
	// Fix is the command or change that resolves it. Empty when there is
	// nothing to do.
	Fix string `json:"fix,omitempty"`
}

// Report is the full run.
type Report struct {
	Checks []Check `json:"checks"`
}

// Failed reports how many checks failed.
func (r Report) Failed() int { return r.count(StatusFail) }

// Warned reports how many checks raised a warning.
func (r Report) Warned() int { return r.count(StatusWarn) }

func (r Report) count(s Status) int {
	n := 0
	for _, c := range r.Checks {
		if c.Status == s {
			n++
		}
	}
	return n
}

// Env is everything doctor touches outside its own process, injected so the
// checks can be tested against a machine that is broken in specific ways --
// which is otherwise the one situation impossible to reproduce on purpose.
type Env struct {
	// Username and UID identify the user whose rootless setup is in question.
	Username string
	UID      int
	// HomeDir is needed to resolve systemd's %h specifier in a unit file.
	HomeDir string

	// LookPath resolves a command, like exec.LookPath.
	LookPath func(string) (string, error)
	// ReadFile reads a file, like os.ReadFile.
	ReadFile func(string) ([]byte, error)
	// Stat stats a path, like os.Stat.
	Stat func(string) (os.FileInfo, error)

	// ConfigPath and DataRoot are where DBForge keeps its state.
	ConfigPath string
	DataRoot   string
	// DaemonSocket is the dbforged socket.
	DaemonSocket string
	// PodmanSocket is the podman socket, empty for podman's own default.
	PodmanSocket string

	// PortRange is the span instances are allocated from.
	PortRange ports.Range
	// PortFree proves a single port is bindable.
	PortFree ports.Prober

	// PingPodman reports whether the podman API answers.
	PingPodman func(context.Context) error
	// PingDaemon reports whether dbforged answers.
	PingDaemon func(context.Context) error
	// Linger reports whether systemd user lingering is on for the user.
	Linger func(string) (bool, error)
	// CgroupControllers lists the controllers delegated to the user manager.
	CgroupControllers func(uid int) ([]string, error)
	// UnitPaths are the systemd user unit locations to look for dbforged.service
	// in, most specific first.
	UnitPaths []string
}

// Run executes every check in order. Ordering is deliberate: the things
// everything else depends on come first, so the first failure in the list is
// the one worth fixing.
func Run(ctx context.Context, env Env) Report {
	var r Report
	add := func(c Check) { r.Checks = append(r.Checks, c) }

	podmanOK := checkPodmanBinary(env, add)
	socketOK := checkPodmanSocket(ctx, env, podmanOK, add)
	checkRootless(env, add)
	checkSubIDs(env, add)
	checkIDMapHelpers(env, add)
	checkCgroups(env, add)
	checkDataRoot(env, add)
	checkConfig(env, add)
	checkPorts(env, add)
	checkUnit(env, add)
	checkLingering(env, add)
	checkDaemon(ctx, env, socketOK, add)

	return r
}

func checkPodmanBinary(env Env, add func(Check)) bool {
	path, err := env.LookPath("podman")
	if err != nil {
		add(Check{
			Name: "podman installed", Status: StatusFail,
			Detail: "not found on PATH",
			Fix:    "sudo pacman -S podman",
		})
		return false
	}
	add(Check{Name: "podman installed", Status: StatusOK, Detail: path})
	return true
}

func checkPodmanSocket(ctx context.Context, env Env, podmanOK bool, add func(Check)) bool {
	const name = "podman socket"
	if !podmanOK {
		add(Check{Name: name, Status: StatusSkip, Detail: "podman is not installed"})
		return false
	}
	if env.PingPodman == nil {
		add(Check{Name: name, Status: StatusSkip, Detail: "no probe configured"})
		return false
	}
	if err := env.PingPodman(ctx); err != nil {
		where := env.PodmanSocket
		if where == "" {
			where = "podman's default socket"
		}
		add(Check{
			Name: name, Status: StatusFail,
			Detail: fmt.Sprintf("%s did not answer: %v", where, err),
			Fix:    "systemctl --user enable --now podman.socket",
		})
		return false
	}
	add(Check{Name: name, Status: StatusOK, Detail: "responding"})
	return true
}

func checkRootless(env Env, add func(Check)) {
	const name = "running rootless"
	if env.UID == 0 {
		add(Check{
			Name: name, Status: StatusFail,
			Detail: "running as root",
			Fix: "run dbforge as your own user. Rootless is the whole point: " +
				"instance data stays in your home and nothing needs sudo",
		})
		return
	}
	add(Check{Name: name, Status: StatusOK, Detail: fmt.Sprintf("uid %d", env.UID)})
}

// minSubIDRange is the conventional allocation. Containers map their own uid
// range into it, so a short range means images whose users have high uids --
// which is most of them -- simply cannot start.
const minSubIDRange = 65536

func checkSubIDs(env Env, add func(Check)) {
	for _, f := range []struct{ file, name string }{
		{"/etc/subuid", "subuid range"},
		{"/etc/subgid", "subgid range"},
	} {
		raw, err := env.ReadFile(f.file)
		if err != nil {
			add(Check{
				Name: f.name, Status: StatusFail,
				Detail: fmt.Sprintf("cannot read %s: %v", f.file, err),
				Fix:    "sudo usermod --add-subuids 100000-165535 --add-subgids 100000-165535 " + env.Username,
			})
			continue
		}
		start, size, found := subIDFor(raw, env.Username, env.UID)
		switch {
		case !found:
			add(Check{
				Name: f.name, Status: StatusFail,
				Detail: fmt.Sprintf("no entry for %q in %s", env.Username, f.file),
				Fix:    "sudo usermod --add-subuids 100000-165535 --add-subgids 100000-165535 " + env.Username,
			})
		case size < minSubIDRange:
			add(Check{
				Name: f.name, Status: StatusWarn,
				Detail: fmt.Sprintf("%d ids from %d; images with high uids may fail to start", size, start),
				Fix:    fmt.Sprintf("widen the range in %s to at least %d ids", f.file, minSubIDRange),
			})
		default:
			add(Check{
				Name: f.name, Status: StatusOK,
				Detail: fmt.Sprintf("%d ids from %d", size, start),
			})
		}
	}
}

// subIDFor finds the user's entry in an /etc/subuid-style file. Entries may be
// keyed by name or by numeric uid; both are valid and both appear in the wild.
func subIDFor(raw []byte, username string, uid int) (start, size int, found bool) {
	sc := bufio.NewScanner(bytes.NewReader(raw))
	uidStr := strconv.Itoa(uid)
	for sc.Scan() {
		fields := strings.Split(strings.TrimSpace(sc.Text()), ":")
		if len(fields) != 3 {
			continue
		}
		if fields[0] != username && fields[0] != uidStr {
			continue
		}
		s, err1 := strconv.Atoi(fields[1])
		n, err2 := strconv.Atoi(fields[2])
		if err1 != nil || err2 != nil {
			continue
		}
		// Multiple entries are additive; report the largest, since that is the
		// one that decides whether a given image can start.
		if n > size {
			start, size, found = s, n, true
		}
	}
	return start, size, found
}

func checkIDMapHelpers(env Env, add func(Check)) {
	const name = "newuidmap/newgidmap"
	var missing []string
	for _, bin := range []string{"newuidmap", "newgidmap"} {
		if _, err := env.LookPath(bin); err != nil {
			missing = append(missing, bin)
		}
	}
	if len(missing) > 0 {
		add(Check{
			Name: name, Status: StatusFail,
			Detail: "missing: " + strings.Join(missing, ", "),
			Fix:    "sudo pacman -S shadow",
		})
		return
	}
	// Deliberately not checking the setuid bit: on Arch these carry file
	// capabilities (cap_setuid/cap_setgid) instead, so a setuid check would
	// report a broken system on a correctly configured one.
	add(Check{Name: name, Status: StatusOK, Detail: "present"})
}

// wantedControllers are the ones instances actually need. Without delegation,
// podman cannot apply per-instance limits and a runaway database can starve
// the host.
var wantedControllers = []string{"cpu", "memory", "pids"}

func checkCgroups(env Env, add func(Check)) {
	const name = "cgroup delegation"
	if env.CgroupControllers == nil {
		add(Check{Name: name, Status: StatusSkip, Detail: "no probe configured"})
		return
	}
	have, err := env.CgroupControllers(env.UID)
	if err != nil {
		add(Check{
			Name: name, Status: StatusWarn,
			Detail: fmt.Sprintf("could not read delegated controllers: %v", err),
			Fix:    "instances will still run, but resource limits may be ignored",
		})
		return
	}
	set := make(map[string]bool, len(have))
	for _, c := range have {
		set[c] = true
	}
	var missing []string
	for _, c := range wantedControllers {
		if !set[c] {
			missing = append(missing, c)
		}
	}
	if len(missing) > 0 {
		add(Check{
			Name: name, Status: StatusWarn,
			Detail: fmt.Sprintf("not delegated: %s (have: %s)",
				strings.Join(missing, ", "), strings.Join(have, " ")),
			Fix: "sudo systemctl edit user@.service, add:\n" +
				"      [Service]\n      Delegate=cpu memory pids\n" +
				"    then reboot. Without it, per-instance resource limits are ignored.",
		})
		return
	}
	add(Check{Name: name, Status: StatusOK, Detail: strings.Join(have, " ")})
}

func checkDataRoot(env Env, add func(Check)) {
	const name = "data directory"
	info, err := env.Stat(env.DataRoot)
	if err != nil {
		if os.IsNotExist(err) {
			// Not a problem: it is created on first use, and saying so is more
			// useful than an error about a directory nobody asked for yet.
			add(Check{
				Name: name, Status: StatusOK,
				Detail: env.DataRoot + " (will be created on first instance)",
			})
			return
		}
		add(Check{
			Name: name, Status: StatusFail,
			Detail: fmt.Sprintf("cannot stat %s: %v", env.DataRoot, err),
		})
		return
	}
	if !info.IsDir() {
		add(Check{
			Name: name, Status: StatusFail,
			Detail: env.DataRoot + " exists but is not a directory",
			Fix:    "move it aside; dbforge needs that path",
		})
		return
	}
	add(Check{Name: name, Status: StatusOK, Detail: env.DataRoot})
}

func checkConfig(env Env, add func(Check)) {
	const name = "config file"
	raw, err := env.ReadFile(env.ConfigPath)
	if err != nil {
		if os.IsNotExist(err) {
			add(Check{
				Name: name, Status: StatusOK,
				Detail: env.ConfigPath + " (not created yet)",
			})
			return
		}
		add(Check{
			Name: name, Status: StatusFail,
			Detail: fmt.Sprintf("cannot read %s: %v", env.ConfigPath, err),
		})
		return
	}
	// The config holds generated database passwords, so its mode matters.
	if info, err := env.Stat(env.ConfigPath); err == nil && info.Mode().Perm()&0o077 != 0 {
		add(Check{
			Name: name, Status: StatusWarn,
			Detail: fmt.Sprintf("%s is mode %04o and holds generated passwords",
				env.ConfigPath, info.Mode().Perm()),
			Fix: "chmod 600 " + env.ConfigPath,
		})
		return
	}
	add(Check{
		Name: name, Status: StatusOK,
		Detail: fmt.Sprintf("%s (%d bytes)", env.ConfigPath, len(raw)),
	})
}

// portSampleLimit caps how many ports doctor will actually bind. A range of a
// thousand ports would otherwise mean a thousand syscalls and a visible pause,
// for a number that only needs to be approximately right.
const portSampleLimit = 64

func checkPorts(env Env, add func(Check)) {
	const name = "port range"
	probe := env.PortFree
	if probe == nil {
		probe = ports.BindProbe
	}
	total := env.PortRange.High - env.PortRange.Low + 1
	if total <= 0 {
		add(Check{
			Name: name, Status: StatusFail,
			Detail: fmt.Sprintf("range %d-%d is empty", env.PortRange.Low, env.PortRange.High),
		})
		return
	}

	sample := total
	if sample > portSampleLimit {
		sample = portSampleLimit
	}
	free := 0
	for i := 0; i < sample; i++ {
		if probe(env.PortRange.Low+i) == nil {
			free++
		}
	}

	scope := fmt.Sprintf("%d-%d", env.PortRange.Low, env.PortRange.High)
	if sample < total {
		scope = fmt.Sprintf("%s (sampled %d)", scope, sample)
	}
	switch {
	case free == 0:
		add(Check{
			Name: name, Status: StatusFail,
			Detail: fmt.Sprintf("no free ports in %s", scope),
			Fix:    "stop whatever is holding them, or widen the range",
		})
	case free*4 < sample:
		add(Check{
			Name: name, Status: StatusWarn,
			Detail: fmt.Sprintf("%d of %d probed ports free in %s", free, sample, scope),
			Fix:    "new instances will still be created, but the range is filling up",
		})
	default:
		add(Check{
			Name: name, Status: StatusOK,
			Detail: fmt.Sprintf("%d of %d probed ports free in %s", free, sample, scope),
		})
	}
}

func checkUnit(env Env, add func(Check)) {
	const name = "systemd unit"
	for _, path := range env.UnitPaths {
		raw, err := env.ReadFile(path)
		if err != nil {
			continue
		}
		exec := expandSpecifiers(execStart(raw), env)
		if exec == "" {
			add(Check{
				Name: name, Status: StatusWarn,
				Detail: path + " has no ExecStart",
				Fix:    "reinstall: ./packaging/install.sh",
			})
			return
		}
		// The failure this catches: installing from source, then from a
		// package. The per-user unit wins and still points at a ~/.local/bin
		// that no longer exists, and the service fails with a bare 203.
		if _, err := env.Stat(exec); err != nil {
			add(Check{
				Name: name, Status: StatusFail,
				Detail: fmt.Sprintf("%s runs %s, which does not exist", path, exec),
				Fix: "the unit points at an old install. Either reinstall, or\n" +
					"    rm " + path + " && systemctl --user daemon-reload",
			})
			return
		}
		add(Check{Name: name, Status: StatusOK, Detail: path + " -> " + exec})
		return
	}
	add(Check{
		Name: name, Status: StatusWarn,
		Detail: "dbforged.service not installed",
		Fix:    "./packaging/install.sh (instances will not start at login without it)",
	})
}

// expandSpecifiers resolves the systemd specifiers that can appear in a unit's
// ExecStart. Without this, a perfectly good unit written as
// `ExecStart=%h/.local/bin/dbforged` -- which is how DBForge's own unit was
// written before Phase 5 -- is reported as pointing at a file that does not
// exist, because %h is not a directory.
//
// Only the specifiers that plausibly appear in a path are handled. An
// unrecognised one is left alone, and the check below then reports a missing
// file, which is the safe direction: a confusing failure beats a silent pass.
func expandSpecifiers(cmd string, env Env) string {
	if cmd == "" {
		return ""
	}
	return strings.NewReplacer(
		"%h", env.HomeDir,
		"%u", env.Username,
		"%U", strconv.Itoa(env.UID),
		"%%", "%",
	).Replace(cmd)
}

// execStart pulls the program out of a unit's ExecStart line.
func execStart(raw []byte) string {
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "ExecStart=") {
			continue
		}
		cmd := strings.TrimPrefix(line, "ExecStart=")
		// Strip systemd's prefix characters (-, @, +, !) and take argv[0].
		cmd = strings.TrimLeft(cmd, "-@+!:")
		if f := strings.Fields(cmd); len(f) > 0 {
			return f[0]
		}
	}
	return ""
}

func checkLingering(env Env, add func(Check)) {
	const name = "user lingering"
	if env.Linger == nil {
		add(Check{Name: name, Status: StatusSkip, Detail: "no probe configured"})
		return
	}
	on, err := env.Linger(env.Username)
	if err != nil {
		add(Check{
			Name: name, Status: StatusSkip,
			Detail: fmt.Sprintf("could not determine: %v", err),
		})
		return
	}
	if !on {
		add(Check{
			Name: name, Status: StatusWarn,
			Detail: "off: your databases stop when you log out",
			Fix:    "sudo loginctl enable-linger " + env.Username,
		})
		return
	}
	add(Check{Name: name, Status: StatusOK, Detail: "enabled"})
}

func checkDaemon(ctx context.Context, env Env, podmanOK bool, add func(Check)) {
	const name = "dbforged"
	if env.PingDaemon == nil {
		add(Check{Name: name, Status: StatusSkip, Detail: "no probe configured"})
		return
	}
	if err := env.PingDaemon(ctx); err != nil {
		fix := "systemctl --user start dbforged"
		if !podmanOK {
			// Telling someone to start a daemon that cannot possibly work is
			// how a diagnosis wastes their time.
			fix = "fix the podman socket first; dbforged cannot run without it"
		}
		add(Check{
			Name: name, Status: StatusWarn,
			Detail: fmt.Sprintf("not responding on %s", env.DaemonSocket),
			Fix:    fix,
		})
		return
	}
	add(Check{Name: name, Status: StatusOK, Detail: "responding on " + env.DaemonSocket})
}

// DefaultEnv builds the Env for a real machine.
func DefaultEnv(configPath, dataRoot, daemonSocket string, rng ports.Range) Env {
	name := os.Getenv("USER")
	if name == "" {
		if u, err := os.UserHomeDir(); err == nil {
			name = filepath.Base(u)
		}
	}
	home, _ := os.UserHomeDir()
	return Env{
		Username:     name,
		UID:          os.Getuid(),
		HomeDir:      home,
		LookPath:     exec.LookPath,
		ReadFile:     os.ReadFile,
		Stat:         os.Stat,
		ConfigPath:   configPath,
		DataRoot:     dataRoot,
		DaemonSocket: daemonSocket,
		PodmanSocket: os.Getenv("DBFORGE_PODMAN_SOCKET"),
		PortRange:    rng,
		PortFree:     ports.BindProbe,
		Linger:       lingerEnabled,
		CgroupControllers: func(uid int) ([]string, error) {
			return delegatedControllers(uid)
		},
		UnitPaths: []string{
			filepath.Join(home, ".config/systemd/user/dbforged.service"),
			"/etc/systemd/user/dbforged.service",
			"/usr/lib/systemd/user/dbforged.service",
		},
	}
}

// lingerEnabled asks logind whether the user's session manager outlives logout.
func lingerEnabled(username string) (bool, error) {
	out, err := exec.Command("loginctl", "show-user", username, "-p", "Linger", "--value").Output()
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(out)) == "yes", nil
}

// delegatedControllers reads the controllers systemd handed to the user
// manager. This is the cgroup the user's services -- including podman's
// containers -- actually live under, so it is the one that matters.
func delegatedControllers(uid int) ([]string, error) {
	path := fmt.Sprintf("/sys/fs/cgroup/user.slice/user-%d.slice/user@%d.service/cgroup.controllers", uid, uid)
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	ctrls := strings.Fields(string(raw))
	sort.Strings(ctrls)
	return ctrls, nil
}

// Timeout bounds a single probe, so one hung socket cannot hang the whole
// diagnosis -- which is precisely the situation doctor is run in.
const Timeout = 3 * time.Second
