//go:build integration

package integration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fiifiofosu/dbforge/internal/daemon"
	"github.com/fiifiofosu/dbforge/internal/model"
	"github.com/fiifiofosu/dbforge/internal/store"
)

// TestUpgradeLeavesRunningInstancesAlone covers what `pacman -Syu` does to a
// developer mid-afternoon: the binaries are replaced and dbforged is
// restarted, while their databases are up and in use (spec 7, phase 5).
//
// The daemon must reattach to the containers that are already running. If it
// instead stopped and started them, an upgrade would silently drop every open
// connection -- and for Postgres, take the write-ahead log through a recovery
// it never needed.
func TestUpgradeLeavesRunningInstancesAlone(t *testing.T) {
	ctx := context.Background()
	m, rt, cfg, dataRoot := restartFixture(t)
	const id = "it-upgrade"
	t.Cleanup(func() { cleanup(t, m, id) })

	inst, err := m.Create(ctx, daemon.CreateOptions{Ref: "redis:7", ID: id, Start: true})
	if err != nil {
		t.Fatal(err)
	}
	waitForRedis(t, inst.Port, 30*time.Second)

	// Something only this container knows. If it survives, the process did.
	redis(t, inst.Port, "SET", "upgrade-witness", "alive")

	before, err := rt.Inspect(ctx, inst.ContainerName())
	if err != nil {
		t.Fatal(err)
	}

	// The upgrade: the old daemon exits, a new one starts over the same state.
	// Restore runs too, since that is what the real startup path does.
	m2 := reopen(t, rt, cfg, dataRoot)
	rep, err := m2.Restore(ctx)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if len(rep.Started) != 0 {
		t.Fatalf("restore started %v; a running instance needs no starting", rep.Started)
	}

	after, err := rt.Inspect(ctx, inst.ContainerName())
	if err != nil {
		t.Fatalf("inspect after upgrade: %v", err)
	}
	if after.ID != before.ID {
		t.Fatalf("container was replaced: %s -> %s", before.ID, after.ID)
	}
	if !after.StartedAt.Equal(before.StartedAt) {
		t.Fatalf("container was restarted: started at %s, now %s",
			before.StartedAt, after.StartedAt)
	}

	got, err := m2.Get(id)
	if err != nil {
		t.Fatalf("new daemon lost track of the instance: %v", err)
	}
	if got.Status != model.StatusRunning {
		t.Fatalf("status after upgrade = %q, want running", got.Status)
	}
	if got.Port != inst.Port {
		t.Fatalf("port moved across the upgrade: %d -> %d", inst.Port, got.Port)
	}

	// The connection story, not just the bookkeeping.
	if v := redis(t, inst.Port, "GET", "upgrade-witness"); !strings.Contains(v, "alive") {
		t.Fatalf("in-memory state did not survive the upgrade: %q", v)
	}
}

// TestUpgradeMigratesPreVersionedConfig covers upgrading over a config written
// before schema_version existed. The daemon must read it, rewrite it in the
// current schema, and leave the original recoverable.
func TestUpgradeMigratesPreVersionedConfig(t *testing.T) {
	ctx := context.Background()
	m, rt, cfg, dataRoot := restartFixture(t)
	const id = "it-migrate"
	t.Cleanup(func() { cleanup(t, m, id) })

	inst, err := m.Create(ctx, daemon.CreateOptions{Ref: "redis:7", ID: id, Start: true})
	if err != nil {
		t.Fatal(err)
	}
	waitForRedis(t, inst.Port, 30*time.Second)

	// Rewrite the config the way an older build would have left it: no schema
	// version, and none of the fields added since.
	raw, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	old := stripV1Fields(string(raw))
	if err := os.WriteFile(cfg, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}

	st := store.New(cfg)
	loaded, err := st.Load()
	if err != nil {
		t.Fatalf("loading a pre-versioned config: %v", err)
	}
	if st.LoadedVersion() != 0 {
		t.Fatalf("LoadedVersion = %d, want 0 for a file with no schema_version", st.LoadedVersion())
	}
	if len(loaded) != 1 {
		t.Fatalf("read %d instances from the old config, want 1", len(loaded))
	}

	m2 := daemon.NewManager(rt, st, daemon.Config{
		DataRoot: dataRoot, PortRange: fixtureBlock(t),
		Scope: os.Getenv("DBFORGE_TEST_SCOPE"),
	})
	if _, err := m2.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile over a migrated config: %v", err)
	}

	// The instance is still there, still running, and still ours.
	got, err := m2.Get(id)
	if err != nil {
		t.Fatalf("instance lost across the migration: %v", err)
	}
	if got.Status != model.StatusRunning {
		t.Fatalf("status = %q, want running", got.Status)
	}
	if got.Restart == "" || got.Desired == "" || got.Scope == "" {
		t.Fatalf("migrated instance has empty fields: restart=%q desired=%q scope=%q",
			got.Restart, got.Desired, got.Scope)
	}

	// The original file must still be recoverable.
	backup := st.BackupPath(0)
	saved, err := os.ReadFile(backup)
	if err != nil {
		t.Fatalf("no backup at %s: %v", backup, err)
	}
	if string(saved) != old {
		t.Fatalf("backup does not match the pre-migration file")
	}
	if filepath.Dir(backup) != filepath.Dir(cfg) {
		t.Fatalf("backup landed outside the config directory: %s", backup)
	}
}
