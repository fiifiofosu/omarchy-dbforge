package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fiifiofosu/dbforge/internal/model"
)

func tempStore(t *testing.T) *Store {
	t.Helper()
	return New(filepath.Join(t.TempDir(), "instances.toml"))
}

func sample() []model.Instance {
	return []model.Instance{{
		ID: "pg16-app", Engine: "postgres", Version: "16",
		Image: "docker.io/library/postgres:16", Port: 5433,
		DataDir: "/home/u/.local/share/dbforge/postgres/16/pg16-app",
		Env:     map[string]string{"POSTGRES_PASSWORD": "secret"},
		Status:  model.StatusRunning, CreatedAt: time.Unix(0, 0).UTC(),
	}}
}

func TestLoadMissingFileIsEmptyNotError(t *testing.T) {
	got, err := tempStore(t).Load()
	if err != nil {
		t.Fatalf("Load on missing file: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d instances, want 0", len(got))
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	s := tempStore(t)
	if err := s.Save(sample(), false); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 1 || got[0].ID != "pg16-app" || got[0].Port != 5433 {
		t.Fatalf("round trip mismatch: %+v", got)
	}
	if got[0].Env["POSTGRES_PASSWORD"] != "secret" {
		t.Fatalf("env did not survive round trip: %+v", got[0].Env)
	}
}

func TestSaveUsesRestrictivePermissions(t *testing.T) {
	// The file holds generated passwords, so it must not be world-readable.
	s := tempStore(t)
	if err := s.Save(sample(), false); err != nil {
		t.Fatalf("Save: %v", err)
	}
	fi, err := os.Stat(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("permissions = %o, want 600", perm)
	}
}

func TestSaveRefusesToClobberExternalEdit(t *testing.T) {
	// The user hand-edits the TOML while the daemon is running (spec 7,
	// phase 2). The next Save must refuse rather than overwrite it.
	s := tempStore(t)
	if err := s.Save(sample(), false); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := os.WriteFile(s.Path(), []byte("schema_version = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(sample(), false); !errors.Is(err, ErrChangedOnDisk) {
		t.Fatalf("got %v, want ErrChangedOnDisk", err)
	}
}

func TestSaveForceOverridesExternalEdit(t *testing.T) {
	s := tempStore(t)
	if err := s.Save(sample(), false); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(s.Path(), []byte("schema_version = 1\n"), 0o600)
	if err := s.Save(sample(), true); err != nil {
		t.Fatalf("forced Save: %v", err)
	}
	got, _ := s.Load()
	if len(got) != 1 {
		t.Fatalf("got %d instances after forced save, want 1", len(got))
	}
}

func TestChangedOnDiskIsFalseAfterOwnWrite(t *testing.T) {
	s := tempStore(t)
	if err := s.Save(sample(), false); err != nil {
		t.Fatal(err)
	}
	changed, err := s.ChangedOnDisk()
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("store reported its own write as an external change")
	}
}

func TestChangedOnDiskDetectsDeletion(t *testing.T) {
	s := tempStore(t)
	s.Save(sample(), false)
	os.Remove(s.Path())
	changed, err := s.ChangedOnDisk()
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("deletion of the config file was not detected as a change")
	}
}

func TestLoadRejectsNewerSchema(t *testing.T) {
	s := tempStore(t)
	os.MkdirAll(filepath.Dir(s.Path()), 0o700)
	os.WriteFile(s.Path(), []byte("schema_version = 99\n"), 0o600)
	if _, err := s.Load(); !errors.Is(err, ErrSchemaTooNew) {
		t.Fatalf("got %v, want ErrSchemaTooNew", err)
	}
}

func TestSaveLeavesNoTempFilesBehind(t *testing.T) {
	s := tempStore(t)
	for i := 0; i < 3; i++ {
		if err := s.Save(sample(), false); err != nil {
			t.Fatal(err)
		}
	}
	entries, _ := os.ReadDir(filepath.Dir(s.Path()))
	for _, e := range entries {
		if e.Name() != "instances.toml" {
			t.Fatalf("leftover file after save: %s", e.Name())
		}
	}
}

// TestLoadMigratesPreVersionedFile covers upgrading over a config written by a
// build from before schema_version existed (spec 7, phase 5).
func TestLoadMigratesPreVersionedFile(t *testing.T) {
	s := tempStore(t)
	v0 := `[[instance]]
  id = "pg16-app"
  engine = "postgres"
  version = "16"
  image = "docker.io/library/postgres:16"
  port = 5433
  data_dir = "/home/u/.local/share/dbforge/postgres/16/pg16-app"
  status = "running"
`
	if err := os.WriteFile(s.Path(), []byte(v0), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := s.Load()
	if err != nil {
		t.Fatalf("Load on a pre-versioned file: %v", err)
	}
	if len(got) != 1 || got[0].ID != "pg16-app" {
		t.Fatalf("instances = %+v, want the one from the file", got)
	}
	if s.LoadedVersion() != 0 {
		t.Fatalf("LoadedVersion = %d, want 0", s.LoadedVersion())
	}
}

func TestSaveBacksUpBeforeUpgradingSchema(t *testing.T) {
	s := tempStore(t)
	v0 := "[[instance]]\n  id = \"old\"\n  engine = \"redis\"\n"
	if err := os.WriteFile(s.Path(), []byte(v0), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Save(loaded, false); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// The original bytes must still be recoverable after the upgrade.
	backup, err := os.ReadFile(s.BackupPath(0))
	if err != nil {
		t.Fatalf("reading backup: %v", err)
	}
	if string(backup) != v0 {
		t.Fatalf("backup = %q, want the pre-migration bytes", backup)
	}

	// And the live file must now be current.
	raw, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "schema_version = 1") {
		t.Fatalf("upgraded file has no schema version:\n%s", raw)
	}
	if s.LoadedVersion() != SchemaVersion {
		t.Fatalf("LoadedVersion after save = %d, want %d", s.LoadedVersion(), SchemaVersion)
	}
}

// A daemon saves constantly. The backup must be the file as it was before the
// very first upgrade write, not whatever the previous save happened to leave.
func TestBackupIsTakenOnceAndKeepsTheOriginal(t *testing.T) {
	s := tempStore(t)
	v0 := "[[instance]]\n  id = \"first\"\n  engine = \"redis\"\n"
	if err := os.WriteFile(s.Path(), []byte(v0), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, _ := s.Load()
	for i := 0; i < 3; i++ {
		if err := s.Save(loaded, true); err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
	}
	backup, err := os.ReadFile(s.BackupPath(0))
	if err != nil {
		t.Fatal(err)
	}
	if string(backup) != v0 {
		t.Fatalf("backup was overwritten by a later save:\n%s", backup)
	}
}

// A file this build wrote must never produce a backup: there is nothing to
// migrate, and a stray .bak would look like an upgrade that never happened.
func TestSaveOfCurrentSchemaWritesNoBackup(t *testing.T) {
	s := tempStore(t)
	if err := s.Save(sample(), false); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(sample(), false); err != nil {
		t.Fatal(err)
	}
	for _, v := range []int{0, 1} {
		if _, err := os.Stat(s.BackupPath(v)); !os.IsNotExist(err) {
			t.Fatalf("unexpected backup at %s", s.BackupPath(v))
		}
	}
}
