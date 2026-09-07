package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/fiifiofosu/dbforge/internal/model"
	"github.com/fiifiofosu/dbforge/internal/ports"
	"github.com/fiifiofosu/dbforge/internal/runtime"
	"github.com/fiifiofosu/dbforge/internal/store"
)

func newTestManager(t *testing.T) (*Manager, *runtime.Fake) {
	t.Helper()
	dir := t.TempDir()
	fake := runtime.NewFake()
	st := store.New(filepath.Join(dir, "instances.toml"))
	m := NewManager(fake, st, Config{
		DataRoot:  filepath.Join(dir, "data"),
		PortRange: ports.Range{Low: 15432, High: 15500},
		// Never touch real sockets in unit tests.
		Probe: func(int) error { return nil },
	})
	if _, err := m.Reconcile(context.Background()); err != nil {
		t.Fatalf("initial Reconcile: %v", err)
	}
	return m, fake
}

func mustCreate(t *testing.T, m *Manager, ref, id string) model.Instance {
	t.Helper()
	inst, err := m.Create(context.Background(), CreateOptions{Ref: ref, ID: id, Start: true})
	if err != nil {
		t.Fatalf("Create(%s): %v", ref, err)
	}
	return inst
}

func TestCreateAllocatesPortAndDataDir(t *testing.T) {
	m, _ := newTestManager(t)
	inst := mustCreate(t, m, "postgres:16", "pg16-app")

	if inst.Port == 0 {
		t.Fatal("no port allocated")
	}
	if inst.Status != model.StatusRunning {
		t.Fatalf("status = %q, want running", inst.Status)
	}
	if _, err := os.Stat(inst.DataDir); err != nil {
		t.Fatalf("data directory not created: %v", err)
	}
	// The password must be generated, not blank or fixed.
	if pw := inst.Env["POSTGRES_PASSWORD"]; len(pw) < 16 {
		t.Fatalf("weak or missing generated password: %q", pw)
	}
}

func TestCreateRejectsDuplicateIDBeforeTouchingPodman(t *testing.T) {
	m, fake := newTestManager(t)
	mustCreate(t, m, "postgres:16", "dup")
	before := len(fake.Containers)

	_, err := m.Create(context.Background(), CreateOptions{Ref: "redis:7", ID: "dup"})
	if !errors.Is(err, ErrInstanceExists) {
		t.Fatalf("got %v, want ErrInstanceExists", err)
	}
	if len(fake.Containers) != before {
		t.Fatal("a container was created despite the id conflict")
	}
}

func TestCreateRejectsUnknownEngineAndBadRef(t *testing.T) {
	m, _ := newTestManager(t)
	for _, ref := range []string{"cassandra:5", "postgres", "postgres:"} {
		if _, err := m.Create(context.Background(), CreateOptions{Ref: ref}); err == nil {
			t.Fatalf("Create(%q) succeeded, want an error", ref)
		}
	}
}

func TestCreateFailsFastOnNonexistentTag(t *testing.T) {
	// postgres:99 does not exist upstream. We must fail before creating a
	// container or a data directory (spec 7, phase 1).
	m, fake := newTestManager(t)
	fake.PullErr = errors.New("manifest unknown")

	_, err := m.Create(context.Background(), CreateOptions{Ref: "postgres:99", ID: "typo"})
	if err == nil {
		t.Fatal("expected an error for a nonexistent tag")
	}
	if len(fake.Containers) != 0 {
		t.Fatal("a container was created for a nonexistent tag")
	}
	if _, err := m.Get("typo"); !errors.Is(err, ErrInstanceNotFound) {
		t.Fatal("a failed create left an instance entry behind")
	}
}

func TestCreateRollsBackWhenContainerCreationFails(t *testing.T) {
	// Simulates disk-full or subuid exhaustion at container-create time. The
	// instance must not survive as a phantom marked running.
	m, fake := newTestManager(t)
	fake.Images["docker.io/library/postgres:16"] = true
	fake.CreateErr = errors.New("no space left on device")

	_, err := m.Create(context.Background(), CreateOptions{Ref: "postgres:16", ID: "doomed"})
	if err == nil || !strings.Contains(err.Error(), "no space left") {
		t.Fatalf("got %v, want the underlying disk error", err)
	}
	if _, err := m.Get("doomed"); !errors.Is(err, ErrInstanceNotFound) {
		t.Fatal("failed create left a tracked instance with no container")
	}
	// The freed port must be reusable.
	inst, err := m.Create(context.Background(), func() CreateOptions {
		fake.CreateErr = nil
		return CreateOptions{Ref: "postgres:16", ID: "recovered"}
	}())
	if err != nil {
		t.Fatalf("create after rollback: %v", err)
	}
	if inst.Port == 0 {
		t.Fatal("port was not reusable after rollback")
	}
}

func TestCreateDoesNotMarkRunningWhenStartFails(t *testing.T) {
	m, fake := newTestManager(t)
	fake.Images["docker.io/library/redis:7"] = true
	inst, err := m.Create(context.Background(), CreateOptions{Ref: "redis:7", ID: "r", Start: false})
	if err != nil {
		t.Fatal(err)
	}
	if inst.Status == model.StatusRunning {
		t.Fatal("instance marked running when it was never started")
	}
}

func TestTwoInstancesOfSameEngineGetDistinctPorts(t *testing.T) {
	m, _ := newTestManager(t)
	a := mustCreate(t, m, "postgres:16", "pg-a")
	b := mustCreate(t, m, "postgres:16", "pg-b")
	if a.Port == b.Port {
		t.Fatalf("both instances got port %d", a.Port)
	}
	if a.DataDir == b.DataDir {
		t.Fatal("both instances share a data directory")
	}
}

func TestPinnedPortConflictIsRejected(t *testing.T) {
	m, _ := newTestManager(t)
	a := mustCreate(t, m, "postgres:16", "pg-a")
	_, err := m.Create(context.Background(), CreateOptions{Ref: "postgres:16", ID: "pg-b", Port: a.Port})
	var taken *ports.ErrPortTaken
	if !errors.As(err, &taken) {
		t.Fatalf("got %v, want ErrPortTaken", err)
	}
}

// --- drift detection (spec 7, phase 1) ---

func TestManualPodmanRmShowsAsMissingNotCrash(t *testing.T) {
	m, fake := newTestManager(t)
	inst := mustCreate(t, m, "postgres:16", "pg16-app")

	// The user runs `podman rm dbforge-pg16-app` behind our back.
	delete(fake.Containers, inst.ContainerName())

	list, err := m.List(context.Background())
	if err != nil {
		t.Fatalf("List after external rm: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("got %d instances, want 1 (entry must be kept, not dropped)", len(list))
	}
	if list[0].Status != model.StatusMissing {
		t.Fatalf("status = %q, want missing", list[0].Status)
	}
	// It must not be silently recreated.
	if len(fake.Containers) != 0 {
		t.Fatal("the instance was silently recreated")
	}
}

func TestStartOnMissingContainerGivesActionableError(t *testing.T) {
	m, fake := newTestManager(t)
	inst := mustCreate(t, m, "redis:7", "r")
	delete(fake.Containers, inst.ContainerName())
	m.List(context.Background()) // reconcile

	err := m.Start(context.Background(), "r")
	if !errors.Is(err, ErrContainerMissing) {
		t.Fatalf("got %v, want ErrContainerMissing", err)
	}
}

func TestReconcileAdoptsUntrackedContainer(t *testing.T) {
	// The daemon crashed after creating the container but before saving the
	// config. The container carries our labels, so we must adopt it rather
	// than orphan it.
	m, fake := newTestManager(t)
	fake.Containers["dbforge-orphan"] = &runtime.Container{
		ID: "ctr-orphan", Name: "dbforge-orphan", State: runtime.StateRunning,
		Labels: map[string]string{
			ManagedLabel: "true", "io.dbforge.id": "orphan",
			"io.dbforge.engine": "postgres", "io.dbforge.version": "16",
			"io.dbforge.port": "15440", "io.dbforge.data_dir": "/tmp/orphan",
		},
	}

	rep, err := m.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(rep.Adopted) != 1 || rep.Adopted[0] != "orphan" {
		t.Fatalf("adopted = %v, want [orphan]", rep.Adopted)
	}
	got, err := m.Get("orphan")
	if err != nil {
		t.Fatalf("adopted instance not queryable: %v", err)
	}
	if got.Port != 15440 || got.Engine != "postgres" {
		t.Fatalf("labels not reconstructed: %+v", got)
	}
}

func TestReconcilePrunesHalfCreatedEntry(t *testing.T) {
	// A "creating" entry with no container means a create died partway.
	dir := t.TempDir()
	st := store.New(filepath.Join(dir, "instances.toml"))
	if err := st.Save([]model.Instance{{
		ID: "half", Engine: "postgres", Version: "16",
		Port: 15433, Status: model.StatusCreating,
	}}, false); err != nil {
		t.Fatal(err)
	}

	m := NewManager(runtime.NewFake(), st, Config{
		DataRoot: dir, Probe: func(int) error { return nil },
	})
	rep, err := m.Reconcile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Pruned) != 1 || rep.Pruned[0] != "half" {
		t.Fatalf("pruned = %v, want [half]", rep.Pruned)
	}
	if _, err := m.Get("half"); !errors.Is(err, ErrInstanceNotFound) {
		t.Fatal("half-created entry survived reconciliation")
	}
}

func TestReconcileIsIdempotent(t *testing.T) {
	m, _ := newTestManager(t)
	mustCreate(t, m, "postgres:16", "pg")
	for i := 0; i < 3; i++ {
		rep, err := m.Reconcile(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(rep.Adopted)+len(rep.Missing)+len(rep.Pruned) != 0 {
			t.Fatalf("pass %d reported drift on a steady state: %+v", i, rep)
		}
	}
}

// --- destruction safety (spec 7, phase 3) ---

func TestRemoveKeepsDataByDefault(t *testing.T) {
	m, _ := newTestManager(t)
	inst := mustCreate(t, m, "postgres:16", "keepme")
	marker := filepath.Join(inst.DataDir, "marker")
	os.WriteFile(marker, []byte("x"), 0o600)

	if err := m.Remove(context.Background(), "keepme", RemoveOptions{}); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("data was deleted by a plain remove; it must be kept")
	}
}

func TestRemoveWithWipeDeletesData(t *testing.T) {
	m, _ := newTestManager(t)
	inst := mustCreate(t, m, "postgres:16", "wipeme")
	if err := m.Remove(context.Background(), "wipeme", RemoveOptions{WipeData: true}); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(inst.DataDir); !os.IsNotExist(err) {
		t.Fatal("data directory survived an explicit wipe")
	}
}

func TestRemoveRefusesSuspiciousDataDir(t *testing.T) {
	m, _ := newTestManager(t)
	mustCreate(t, m, "redis:7", "bad")
	m.mu.Lock()
	m.instances["bad"].DataDir = "relative/path"
	m.mu.Unlock()

	err := m.Remove(context.Background(), "bad", RemoveOptions{WipeData: true})
	if err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("got %v, want a refusal to wipe a non-absolute path", err)
	}
}

// --- concurrency (spec 7, phase 2) ---

func TestConcurrentCreatesWithSameIDYieldExactlyOne(t *testing.T) {
	m, _ := newTestManager(t)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var okCount int

	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := m.Create(context.Background(), CreateOptions{Ref: "postgres:16", ID: "race"})
			if err == nil {
				mu.Lock()
				okCount++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if okCount != 1 {
		t.Fatalf("%d creates succeeded, want exactly 1", okCount)
	}
}

func TestConcurrentStartStopDoesNotCorruptState(t *testing.T) {
	m, _ := newTestManager(t)
	mustCreate(t, m, "redis:7", "churn")

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				m.Start(context.Background(), "churn")
			} else {
				m.Stop(context.Background(), "churn", 1)
			}
		}(i)
	}
	wg.Wait()

	got, err := m.Get("churn")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.StatusRunning && got.Status != model.StatusStopped {
		t.Fatalf("status ended in an invalid state: %q", got.Status)
	}
}

func TestConnStringIncludesGeneratedPassword(t *testing.T) {
	m, _ := newTestManager(t)
	inst := mustCreate(t, m, "postgres:16", "pg")
	got, err := m.ConnString("pg")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, inst.Env["POSTGRES_PASSWORD"]) {
		t.Fatalf("conn string %q lacks the generated password", got)
	}
}

func TestWipeDelegatesToRuntimeNotOsRemoveAll(t *testing.T) {
	// Under rootless Podman the data directory is owned by a subuid, so a
	// direct os.RemoveAll fails with EPERM. The wipe must go through the
	// runtime, which can re-enter the user namespace. This test pins the
	// delegation so the call cannot regress back to os.RemoveAll.
	m, fake := newTestManager(t)
	inst := mustCreate(t, m, "postgres:16", "wipe-deleg")

	fake.RemovePathErr = errors.New("simulated userns failure")
	err := m.Remove(context.Background(), "wipe-deleg", RemoveOptions{WipeData: true})
	if err == nil || !strings.Contains(err.Error(), "simulated userns failure") {
		t.Fatalf("got %v; wipe did not go through Runtime.RemovePath", err)
	}
	// The instance must survive a failed wipe rather than being forgotten
	// while its data is still on disk.
	if _, err := m.Get("wipe-deleg"); err != nil {
		t.Fatal("instance was dropped even though its data could not be removed")
	}
	if _, err := os.Stat(inst.DataDir); err != nil {
		t.Fatal("data directory vanished despite the removal failing")
	}
}

func TestRemoveRunningGivesActionableError(t *testing.T) {
	// Podman's own message talks about "container state improper", which tells
	// the user nothing. We must name the two ways out.
	m, fake := newTestManager(t)
	mustCreate(t, m, "redis:7", "busy")
	fake.RemoveErr = errors.New("cannot remove container abc123 as it is running - " +
		"running or paused containers cannot be removed without force: container state improper")

	err := m.Remove(context.Background(), "busy", RemoveOptions{})
	if err == nil {
		t.Fatal("expected an error removing a running instance")
	}
	for _, want := range []string{"dbctl stop busy", "--force"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
}
