package daemon

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/fiifiofosu/dbforge/internal/engines"
	"github.com/fiifiofosu/dbforge/internal/model"
	"github.com/fiifiofosu/dbforge/internal/ports"
	"github.com/fiifiofosu/dbforge/internal/runtime"
	"github.com/fiifiofosu/dbforge/internal/store"
)

// simulateReboot rebuilds a Manager over the same config file and container
// set, which is what a daemon restart or a host reboot looks like from here.
func simulateReboot(t *testing.T, cfgPath, dataRoot string, fake *runtime.Fake) *Manager {
	t.Helper()
	m := NewManager(fake, store.New(cfgPath), Config{
		DataRoot:  dataRoot,
		PortRange: ports.Range{Low: 15432, High: 15500},
		Probe:     func(int) error { return nil },
		Log:       testLogger(),
	})
	if _, err := m.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile after reboot: %v", err)
	}
	return m
}

func rebootFixture(t *testing.T) (*Manager, *runtime.Fake, string, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "instances.toml")
	dataRoot := filepath.Join(dir, "data")
	fake := runtime.NewFake()
	m := NewManager(fake, store.New(cfg), Config{
		DataRoot: dataRoot, PortRange: ports.Range{Low: 15432, High: 15500},
		Probe: func(int) error { return nil }, Log: testLogger(),
	})
	if _, err := m.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	return m, fake, cfg, dataRoot
}

// stopAllContainers simulates a host reboot: the containers exist but nothing
// is running, which is exactly the state rootless podman leaves behind.
func stopAllContainers(fake *runtime.Fake, exitCode int) {
	for name := range fake.Containers {
		fake.SetState(name, runtime.StateStopped, exitCode)
	}
}

func TestDefaultRestartPolicyIsAlways(t *testing.T) {
	m, _, _, _ := rebootFixture(t)
	inst, err := m.Create(context.Background(), CreateOptions{Ref: "postgres:16", ID: "pg", Start: true})
	if err != nil {
		t.Fatal(err)
	}
	if inst.Restart != model.RestartAlways {
		t.Fatalf("restart = %q, want always", inst.Restart)
	}
	if inst.Desired != model.DesiredRunning {
		t.Fatalf("desired = %q, want running", inst.Desired)
	}
}

func TestCreateRejectsInvalidRestartPolicy(t *testing.T) {
	m, _, _, _ := rebootFixture(t)
	_, err := m.Create(context.Background(), CreateOptions{
		Ref: "redis:7", ID: "r", Restart: model.RestartPolicy("sometimes"),
	})
	if err == nil {
		t.Fatal("an invalid restart policy was accepted")
	}
}

// The headline Phase 2 guarantee.
func TestInstanceComesBackAfterReboot(t *testing.T) {
	m, fake, cfg, dataRoot := rebootFixture(t)
	if _, err := m.Create(context.Background(), CreateOptions{
		Ref: "postgres:16", ID: "pg", Start: true,
	}); err != nil {
		t.Fatal(err)
	}

	stopAllContainers(fake, 0)
	m2 := simulateReboot(t, cfg, dataRoot, fake)

	rep, err := m2.Restore(context.Background())
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if len(rep.Started) != 1 || rep.Started[0] != "pg" {
		t.Fatalf("started = %v, want [pg]", rep.Started)
	}
	got, _ := m2.Get("pg")
	if got.Status != model.StatusRunning {
		t.Fatalf("status after restore = %q, want running", got.Status)
	}
}

// The counterpart, and the one that matters more: a deliberate stop must
// outlive a reboot, or `dbctl stop` is meaningless.
func TestDeliberatelyStoppedInstanceStaysStoppedAfterReboot(t *testing.T) {
	m, fake, cfg, dataRoot := rebootFixture(t)
	if _, err := m.Create(context.Background(), CreateOptions{
		Ref: "postgres:16", ID: "pg", Start: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := m.Stop(context.Background(), "pg", 10); err != nil {
		t.Fatal(err)
	}

	m2 := simulateReboot(t, cfg, dataRoot, fake)
	rep, err := m2.Restore(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Started) != 0 {
		t.Fatalf("restore started %v; a deliberately stopped instance was resurrected", rep.Started)
	}
	got, _ := m2.Get("pg")
	if got.Status == model.StatusRunning {
		t.Fatal("instance is running after a reboot despite being stopped by the user")
	}
}

func TestRestartNoIsNeverAutoStarted(t *testing.T) {
	m, fake, cfg, dataRoot := rebootFixture(t)
	if _, err := m.Create(context.Background(), CreateOptions{
		Ref: "redis:7", ID: "manual", Start: true, Restart: model.RestartNo,
	}); err != nil {
		t.Fatal(err)
	}
	stopAllContainers(fake, 0)

	m2 := simulateReboot(t, cfg, dataRoot, fake)
	rep, _ := m2.Restore(context.Background())
	if len(rep.Started) != 0 {
		t.Fatalf("restart=no instance was auto-started: %v", rep.Started)
	}
	if _, ok := rep.Skipped["manual"]; !ok {
		t.Fatal("skip was not reported for a restart=no instance")
	}
}

func TestRestartOnFailureOnlyAfterUncleanExit(t *testing.T) {
	// Clean exit: leave it alone.
	m, fake, cfg, dataRoot := rebootFixture(t)
	if _, err := m.Create(context.Background(), CreateOptions{
		Ref: "redis:7", ID: "onfail", Start: true, Restart: model.RestartOnFailure,
	}); err != nil {
		t.Fatal(err)
	}
	stopAllContainers(fake, 0)
	m2 := simulateReboot(t, cfg, dataRoot, fake)
	if rep, _ := m2.Restore(context.Background()); len(rep.Started) != 0 {
		t.Fatalf("on-failure restarted after a clean exit: %v", rep.Started)
	}

	// Unclean exit: bring it back.
	stopAllContainers(fake, 137) // OOM-killed
	m3 := simulateReboot(t, cfg, dataRoot, fake)
	rep, err := m3.Restore(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Started) != 1 {
		t.Fatalf("on-failure did not restart after exit 137: %+v", rep)
	}
}

// An unclean exit means the engine will run crash recovery. Surface it.
func TestUncleanExitIsSurfaced(t *testing.T) {
	m, fake, cfg, dataRoot := rebootFixture(t)
	if _, err := m.Create(context.Background(), CreateOptions{
		Ref: "postgres:16", ID: "crashed", Start: true, Restart: model.RestartNo,
	}); err != nil {
		t.Fatal(err)
	}
	stopAllContainers(fake, 137)

	m2 := simulateReboot(t, cfg, dataRoot, fake)
	rep, err := m2.Reconcile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Unclean) != 1 || rep.Unclean[0] != "crashed" {
		t.Fatalf("unclean = %v, want [crashed]", rep.Unclean)
	}
	got, _ := m2.Get("crashed")
	if got.LastExitCode != 137 {
		t.Fatalf("LastExitCode = %d, want 137", got.LastExitCode)
	}
}

func TestCleanExitIsNotReportedAsUnclean(t *testing.T) {
	m, fake, cfg, dataRoot := rebootFixture(t)
	m.Create(context.Background(), CreateOptions{Ref: "redis:7", ID: "clean", Start: true})
	stopAllContainers(fake, 0)

	m2 := simulateReboot(t, cfg, dataRoot, fake)
	rep, _ := m2.Reconcile(context.Background())
	if len(rep.Unclean) != 0 {
		t.Fatalf("clean exit reported as unclean: %v", rep.Unclean)
	}
}

// Restarting an instance must clear the stale exit code, or it looks crashed
// forever.
func TestStartClearsLastExitCode(t *testing.T) {
	m, fake, cfg, dataRoot := rebootFixture(t)
	m.Create(context.Background(), CreateOptions{Ref: "redis:7", ID: "r", Start: true})
	stopAllContainers(fake, 137)

	m2 := simulateReboot(t, cfg, dataRoot, fake)
	if err := m2.Start(context.Background(), "r"); err != nil {
		t.Fatal(err)
	}
	got, _ := m2.Get("r")
	if got.LastExitCode != 0 {
		t.Fatalf("LastExitCode = %d after a successful start, want 0", got.LastExitCode)
	}
}

func TestRestoreSkipsMissingContainer(t *testing.T) {
	m, fake, cfg, dataRoot := rebootFixture(t)
	inst, _ := m.Create(context.Background(), CreateOptions{Ref: "redis:7", ID: "gone", Start: true})
	delete(fake.Containers, inst.ContainerName())

	m2 := simulateReboot(t, cfg, dataRoot, fake)
	rep, err := m2.Restore(context.Background())
	if err != nil {
		t.Fatalf("restore must not fail on a missing container: %v", err)
	}
	if len(rep.Started) != 0 {
		t.Fatal("restore tried to start a missing container")
	}
	if _, ok := rep.Skipped["gone"]; !ok {
		t.Fatalf("missing container was not reported as skipped: %+v", rep)
	}
}

func TestRestoreIsIdempotent(t *testing.T) {
	m, fake, cfg, dataRoot := rebootFixture(t)
	m.Create(context.Background(), CreateOptions{Ref: "postgres:16", ID: "pg", Start: true})
	stopAllContainers(fake, 0)
	m2 := simulateReboot(t, cfg, dataRoot, fake)

	first, _ := m2.Restore(context.Background())
	if len(first.Started) != 1 {
		t.Fatalf("first restore started %v, want 1", first.Started)
	}
	second, _ := m2.Restore(context.Background())
	if len(second.Started) != 0 {
		t.Fatalf("second restore restarted an already-running instance: %v", second.Started)
	}
}

func TestSetRestartPolicyPersistsAcrossReboot(t *testing.T) {
	m, fake, cfg, dataRoot := rebootFixture(t)
	m.Create(context.Background(), CreateOptions{Ref: "redis:7", ID: "r", Start: true})
	if err := m.SetRestartPolicy(context.Background(), "r", model.RestartNo); err != nil {
		t.Fatal(err)
	}
	stopAllContainers(fake, 0)

	m2 := simulateReboot(t, cfg, dataRoot, fake)
	got, _ := m2.Get("r")
	if got.Restart != model.RestartNo {
		t.Fatalf("restart = %q after reboot, want no", got.Restart)
	}
	if rep, _ := m2.Restore(context.Background()); len(rep.Started) != 0 {
		t.Fatal("instance with restart=no was started after a reboot")
	}
}

func TestSetRestartPolicyRejectsInvalid(t *testing.T) {
	m, _, _, _ := rebootFixture(t)
	m.Create(context.Background(), CreateOptions{Ref: "redis:7", ID: "r", Start: true})
	if err := m.SetRestartPolicy(context.Background(), "r", "maybe"); err == nil {
		t.Fatal("invalid policy accepted")
	}
}

// A config written before restart policies existed must not silently become
// "never restart".
func TestLegacyConfigGetsDefaultPolicy(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "instances.toml")
	st := store.New(cfg)
	if err := st.Save([]model.Instance{{
		ID: "legacy", Engine: "redis", Version: "7", Port: 15440,
		DataDir: filepath.Join(dir, "d"), Status: model.StatusRunning,
		// No Restart, no Desired -- as an older DBForge would have written it.
	}}, false); err != nil {
		t.Fatal(err)
	}

	fake := runtime.NewFake()
	fake.Containers["dbforge-legacy"] = &runtime.Container{
		ID: "c", Name: "dbforge-legacy", State: runtime.StateRunning,
		Labels: map[string]string{ManagedLabel: "true", "io.dbforge.id": "legacy"},
	}
	m := simulateReboot(t, cfg, dir, fake)

	got, err := m.Get("legacy")
	if err != nil {
		t.Fatal(err)
	}
	if got.Restart != model.DefaultRestartPolicy {
		t.Fatalf("legacy instance restart = %q, want the default %q", got.Restart, model.DefaultRestartPolicy)
	}
	if got.Desired != model.DesiredRunning {
		t.Fatalf("legacy running instance desired = %q, want running", got.Desired)
	}
}

// Two DBForge installations on one host must not adopt each other's
// containers. Without scoping a test daemon steals the real daemon's
// instances, and vice versa.
func TestScopeIsolatesInstallations(t *testing.T) {
	dir := t.TempDir()
	fake := runtime.NewFake()

	mkManager := func(scope, cfgName string) *Manager {
		m := NewManager(fake, store.New(filepath.Join(dir, cfgName)), Config{
			DataRoot: filepath.Join(dir, scope),
			Scope:    scope,
			Probe:    func(int) error { return nil },
			Log:      testLogger(),
		})
		if _, err := m.Reconcile(context.Background()); err != nil {
			t.Fatal(err)
		}
		return m
	}

	a := mkManager("alpha", "a.toml")
	if _, err := a.Create(context.Background(), CreateOptions{Ref: "redis:7", ID: "mine", Start: true}); err != nil {
		t.Fatal(err)
	}

	b := mkManager("beta", "b.toml")
	if _, err := b.Get("mine"); err == nil {
		t.Fatal("a daemon in scope beta adopted an instance owned by scope alpha")
	}

	list, err := b.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("scope beta sees %d foreign instances: %+v", len(list), list)
	}

	// And the original must still own it.
	if _, err := a.Get("mine"); err != nil {
		t.Fatalf("scope alpha lost its own instance: %v", err)
	}
}

// Containers created before scoping existed carry no scope label and must
// still be adopted, or an upgrade would orphan every existing instance.
func TestUnscopedContainerIsStillAdopted(t *testing.T) {
	dir := t.TempDir()
	fake := runtime.NewFake()
	fake.Containers["dbforge-legacy"] = &runtime.Container{
		ID: "c", Name: "dbforge-legacy", State: runtime.StateRunning,
		Labels: map[string]string{ManagedLabel: "true", "io.dbforge.id": "legacy"},
	}
	m := NewManager(fake, store.New(filepath.Join(dir, "c.toml")), Config{
		DataRoot: dir, Scope: "default", Probe: func(int) error { return nil }, Log: testLogger(),
	})
	rep, err := m.Reconcile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Adopted) != 1 {
		t.Fatalf("unscoped legacy container was not adopted: %+v", rep)
	}
}

// The container-level restart policy must always be "no". Podman re-evaluates
// its own policy when the podman service restarts, and it cannot tell a crash
// from a deliberate `dbctl stop` -- so a container marked "always" comes back
// and silently undoes the user's decision. Restart policy is the daemon's job.
func TestContainerRestartPolicyIsAlwaysNo(t *testing.T) {
	dir := t.TempDir()
	fake := runtime.NewFake()
	m := NewManager(fake, store.New(filepath.Join(dir, "c.toml")), Config{
		DataRoot: dir, Probe: func(int) error { return nil }, Log: testLogger(),
	})
	if _, err := m.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}

	for _, policy := range []model.RestartPolicy{model.RestartAlways, model.RestartOnFailure, model.RestartNo} {
		id := "pol-" + string(policy)
		if _, err := m.Create(context.Background(), CreateOptions{
			Ref: "redis:7", ID: id, Start: true, Restart: policy,
		}); err != nil {
			t.Fatal(err)
		}
		got := fake.CreateSpecFor()
		if got.RestartPolicy != "no" {
			t.Fatalf("dbforge policy %q produced container RestartPolicy %q, want \"no\"",
				policy, got.RestartPolicy)
		}
	}
}

// Supervision must bring back a crashed instance while the host stays up, not
// only at daemon startup.
func TestSuperviseRestartsCrashedInstance(t *testing.T) {
	m, fake, _, _ := rebootFixture(t)
	inst, err := m.Create(context.Background(), CreateOptions{
		Ref: "redis:7", ID: "crashy", Start: true, Restart: model.RestartAlways,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Supervise(ctx, 20*time.Millisecond)

	// The container dies on its own.
	fake.SetState(inst.ContainerName(), runtime.StateStopped, 137)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if st, ok := fake.GetState(inst.ContainerName()); ok && st == runtime.StateRunning {
			return // restored
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("supervision did not restart a crashed instance with restart=always")
}

// The counterpart: supervision must never resurrect a deliberate stop.
func TestSuperviseLeavesStoppedInstanceAlone(t *testing.T) {
	m, fake, _, _ := rebootFixture(t)
	inst, err := m.Create(context.Background(), CreateOptions{
		Ref: "redis:7", ID: "quiet", Start: true, Restart: model.RestartAlways,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Stop(context.Background(), "quiet", 5); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Supervise(ctx, 20*time.Millisecond)
	time.Sleep(500 * time.Millisecond)

	if st, ok := fake.GetState(inst.ContainerName()); ok && st == runtime.StateRunning {
		t.Fatal("supervision restarted an instance the user deliberately stopped")
	}
}

func TestSuperviseStopsOnContextCancel(t *testing.T) {
	m, _, _, _ := rebootFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m.Supervise(ctx, 10*time.Millisecond); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Supervise did not return after its context was cancelled")
	}
}

// A caller passing 0 must get the engine's own shutdown budget, not podman's
// short default.
func TestStopUsesEngineTimeoutWhenUnspecified(t *testing.T) {
	m, fake, _, _ := rebootFixture(t)
	if _, err := m.Create(context.Background(), CreateOptions{
		Ref: "postgres:16", ID: "pg", Start: true,
	}); err != nil {
		t.Fatal(err)
	}

	if err := m.Stop(context.Background(), "pg", 0); err != nil {
		t.Fatal(err)
	}
	got := fake.LastStopTimeout()
	want, _ := engines.Get("postgres")
	if got != want.StopTimeoutSecs {
		t.Fatalf("stop timeout = %d, want the engine's %d", got, want.StopTimeoutSecs)
	}
}

func TestExplicitStopTimeoutWins(t *testing.T) {
	m, fake, _, _ := rebootFixture(t)
	m.Create(context.Background(), CreateOptions{Ref: "postgres:16", ID: "pg", Start: true})
	if err := m.Stop(context.Background(), "pg", 5); err != nil {
		t.Fatal(err)
	}
	if got := fake.LastStopTimeout(); got != 5 {
		t.Fatalf("stop timeout = %d, want the explicit 5", got)
	}
}

// The timeout is also baked into the container, so a `podman stop` or a host
// shutdown gets the same budget.
func TestStopTimeoutIsBakedIntoTheContainer(t *testing.T) {
	m, fake, _, _ := rebootFixture(t)
	m.Create(context.Background(), CreateOptions{Ref: "postgres:16", ID: "pg", Start: true})
	want, _ := engines.Get("postgres")
	if got := fake.CreateSpecFor().StopTimeoutSecs; got != want.StopTimeoutSecs {
		t.Fatalf("container stop timeout = %d, want %d", got, want.StopTimeoutSecs)
	}
}
