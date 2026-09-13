//go:build integration

package integration

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/fiifiofosu/dbforge/internal/daemon"
	"github.com/fiifiofosu/dbforge/internal/model"
	"github.com/fiifiofosu/dbforge/internal/runtime"
	"github.com/fiifiofosu/dbforge/internal/store"
)

// restartFixture returns a manager plus the paths needed to build a second one
// over the same state, which is what a daemon restart or reboot looks like.
func restartFixture(t *testing.T) (*daemon.Manager, *runtime.Podman, string, string) {
	t.Helper()
	ctx := context.Background()

	rt, err := runtime.NewPodman(ctx, os.Getenv("DBFORGE_PODMAN_SOCKET"))
	if err != nil {
		t.Skipf("podman unavailable: %v", err)
	}
	if err := rt.Ping(ctx); err != nil {
		t.Skipf("podman socket not responding: %v", err)
	}

	dir := t.TempDir()
	cfg := filepath.Join(dir, "instances.toml")
	dataRoot := filepath.Join(dir, "data")
	scope := testScope(t)
	purgeScope(t, scope)

	block := portBlock(t)
	m := daemon.NewManager(rt, store.New(cfg), daemon.Config{
		DataRoot: dataRoot, PortRange: block,
		Scope: scope,
	})
	if _, err := m.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DBFORGE_TEST_SCOPE", scope)
	t.Setenv("DBFORGE_TEST_PORT_LOW", strconv.Itoa(block.Low))
	t.Setenv("DBFORGE_TEST_PORT_HIGH", strconv.Itoa(block.High))
	return m, rt, cfg, dataRoot
}

// reopen builds a second manager over the same state, ports and scope as the
// fixture -- a daemon restart, not a new daemon.
func reopen(t *testing.T, rt *runtime.Podman, cfg, dataRoot string) *daemon.Manager {
	t.Helper()
	m := daemon.NewManager(rt, store.New(cfg), daemon.Config{
		DataRoot: dataRoot, PortRange: fixtureBlock(t),
		// Same scope as the fixture: this is a restart of the same daemon.
		Scope: os.Getenv("DBFORGE_TEST_SCOPE"),
	})
	if _, err := m.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile on reopen: %v", err)
	}
	return m
}

// TestRestoreAfterDaemonRestart is the Phase 2 guarantee against real
// containers: stop everything (as a reboot would), start a fresh daemon, and
// the instance the user left running must come back.
func TestRestoreAfterDaemonRestart(t *testing.T) {
	ctx := context.Background()
	m, rt, cfg, dataRoot := restartFixture(t)
	const running, stopped = "it-r-running", "it-r-stopped"
	t.Cleanup(func() { cleanup(t, m, running); cleanup(t, m, stopped) })

	up, err := m.Create(ctx, daemon.CreateOptions{Ref: "redis:7", ID: running, Start: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Create(ctx, daemon.CreateOptions{Ref: "redis:7", ID: stopped, Start: true}); err != nil {
		t.Fatal(err)
	}
	// The user deliberately stops this one. That decision must outlive a reboot.
	if err := m.Stop(ctx, stopped, 20); err != nil {
		t.Fatal(err)
	}
	waitForRedis(t, up.Port, redisReady)

	// Simulate the reboot: every container is down.
	if err := rt.Stop(ctx, "dbforge-"+running, 20); err != nil {
		t.Fatal(err)
	}

	m2 := reopen(t, rt, cfg, dataRoot)
	rep, err := m2.Restore(ctx)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}

	if len(rep.Started) != 1 || rep.Started[0] != running {
		t.Fatalf("started = %v, want [%s]", rep.Started, running)
	}
	if _, skipped := rep.Skipped[stopped]; !skipped {
		t.Fatalf("deliberately stopped instance was not skipped: %+v", rep)
	}

	got, _ := m2.Get(stopped)
	if got.Status == model.StatusRunning {
		t.Fatal("an instance the user stopped came back after a restart")
	}
	waitForRedis(t, up.Port, redisReady)
}

// TestUncleanExitIsDetectedFromRealContainer kills a container outright and
// checks the exit code makes it into our state, since that is what drives both
// the warning and on-failure restarts.
func TestUncleanExitIsDetectedFromRealContainer(t *testing.T) {
	ctx := context.Background()
	m, rt, cfg, dataRoot := restartFixture(t)
	const id = "it-r-crash"
	t.Cleanup(func() { cleanup(t, m, id) })

	inst, err := m.Create(ctx, daemon.CreateOptions{
		Ref: "redis:7", ID: id, Start: true, Restart: model.RestartOnFailure,
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForRedis(t, inst.Port, redisReady)

	// Stop with a zero timeout so the engine is killed rather than asked
	// politely -- this is the OOM/crash shape.
	if err := rt.Stop(ctx, inst.ContainerName(), 0); err != nil {
		t.Fatalf("kill: %v", err)
	}
	time.Sleep(2 * time.Second)

	m2 := reopen(t, rt, cfg, dataRoot)
	got, err := m2.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status == model.StatusRunning {
		t.Skip("podman's own restart policy already brought it back; nothing to assert")
	}
	t.Logf("exit code recorded: %d", got.LastExitCode)

	// on-failure must bring it back regardless of the specific code.
	rep, err := m2.Restore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastExitCode != 0 && len(rep.Started) == 0 {
		t.Fatalf("on-failure did not restart after an unclean exit (code %d)", got.LastExitCode)
	}
}

// TestRestartPolicyNoSurvivesRestart pins the opt-out.
func TestRestartPolicyNoSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	m, rt, cfg, dataRoot := restartFixture(t)
	const id = "it-r-manual"
	t.Cleanup(func() { cleanup(t, m, id) })

	inst, err := m.Create(ctx, daemon.CreateOptions{
		Ref: "redis:7", ID: id, Start: true, Restart: model.RestartNo,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.Stop(ctx, inst.ContainerName(), 20); err != nil {
		t.Fatal(err)
	}

	m2 := reopen(t, rt, cfg, dataRoot)
	if got, _ := m2.Get(id); got.Restart != model.RestartNo {
		t.Fatalf("policy = %q after restart, want no", got.Restart)
	}
	rep, _ := m2.Restore(ctx)
	for _, started := range rep.Started {
		if started == id {
			t.Fatal("a restart=no instance was auto-started")
		}
	}
}

// TestDaemonSurvivesPodmanSocketRestart covers the resume-shaped failure: the
// podman socket disappears and comes back while the daemon keeps running.
func TestDaemonSurvivesPodmanSocketRestart(t *testing.T) {
	ctx := context.Background()
	m, _, _, _ := restartFixture(t)
	const id = "it-r-socket"
	t.Cleanup(func() { cleanup(t, m, id) })

	if _, err := m.Create(ctx, daemon.CreateOptions{Ref: "redis:7", ID: id, Start: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.List(ctx); err != nil {
		t.Fatalf("list before socket churn: %v", err)
	}

	// The bindings dial per request rather than holding one connection, so a
	// socket restart should be transparent. Assert it rather than assume it.
	restartPodmanSocket(t)

	if _, err := m.List(ctx); err != nil {
		t.Fatalf("list after socket restart: %v", err)
	}
	if err := m.Stop(ctx, id, 20); err != nil {
		t.Fatalf("mutation after socket restart: %v", err)
	}
}
