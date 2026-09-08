// Package store persists the instance index to ~/.config/dbforge/instances.toml.
//
// The file is an index, not the source of truth -- Podman is (spec 6). The
// store therefore optimises for two things: never corrupting the file, and
// never silently clobbering edits the user made by hand while the daemon was
// running (spec 7, phase 2).
package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/BurntSushi/toml"
	"github.com/fiifiofosu/dbforge/internal/model"
)

// SchemaVersion is the schema this build writes. It is stamped into the file
// so an upgrade can migrate an older config rather than misreading it, and so
// a downgrade refuses rather than corrupting it (spec 7, phase 5).
//
// Version history:
//
//	0  Pre-versioned. No schema_version key. Instances have no restart
//	   policy, desired state or scope; the daemon fills those in.
//	1  Current. schema_version, restart, desired and scope are present.
//	   The optional "suspended" key was added later within this version: it is
//	   additive and defaults to false, so an older build reading a newer file
//	   simply ignores it and loses nothing but the auto-resume after a
//	   shutdown. That does not warrant a bump, which would make every older
//	   build refuse the file outright.
//
// Adding a version means adding a case to migrate() and a round-trip test
// that starts from a real file written by the older build.
const SchemaVersion = 1

// ErrSchemaTooNew is returned when the file was written by a newer DBForge.
var ErrSchemaTooNew = errors.New("config was written by a newer version of dbforge")

// ErrChangedOnDisk is returned by Save when the file changed underneath us
// since it was loaded. The caller must reload and reconcile rather than
// overwrite the user's edit.
var ErrChangedOnDisk = errors.New("config file changed on disk since it was loaded")

type file struct {
	SchemaVersion int              `toml:"schema_version"`
	Instance      []model.Instance `toml:"instance"`
}

// Store reads and writes the instance index.
type Store struct {
	path string
	// digest is the hash of the bytes we last read or wrote, used to detect
	// external edits.
	digest string
	// loadedVersion is the schema version of the file we last read, so Save
	// knows whether it is about to rewrite an older file in a newer format.
	loadedVersion int
	// backedUp records that the pre-migration backup has already been taken,
	// so a long-running daemon writes it once rather than on every save.
	backedUp bool
}

// New returns a Store for the given path.
func New(path string) *Store { return &Store{path: path} }

// DefaultPath is ~/.config/dbforge/instances.toml, honouring XDG_CONFIG_HOME.
func DefaultPath() (string, error) {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "dbforge", "instances.toml"), nil
}

// DefaultDataRoot is ~/.local/share/dbforge, honouring XDG_DATA_HOME and the
// DBFORGE_DATA_ROOT override. Data lives outside anything a package manager
// owns so uninstalling never deletes a database (spec 7, phase 5).
//
// It sits beside DefaultPath so the two path policies cannot drift: the daemon,
// the CLI and `dbctl doctor` all have to agree on where state lives, and doctor
// reporting a different directory from the one in use would be worse than no
// check at all.
func DefaultDataRoot() (string, error) {
	if d := os.Getenv("DBFORGE_DATA_ROOT"); d != "" {
		return d, nil
	}
	dir := os.Getenv("XDG_DATA_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dir, "dbforge"), nil
}

// Path returns the file this store manages.
func (s *Store) Path() string { return s.path }

// Load reads the index. A missing file is not an error: it is an empty index.
func (s *Store) Load() ([]model.Instance, error) {
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		s.digest = ""
		s.loadedVersion = SchemaVersion // a file we create is current by definition
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", s.path, err)
	}

	var f file
	if err := toml.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", s.path, err)
	}
	if f.SchemaVersion > SchemaVersion {
		return nil, fmt.Errorf("%w: %s is schema v%d but this build understands v%d. "+
			"Upgrade dbforge, or move the file aside to start fresh -- your databases "+
			"are in podman and are not affected either way",
			ErrSchemaTooNew, s.path, f.SchemaVersion, SchemaVersion)
	}
	// Capture the version before migrating, since migrate() rewrites it: Save
	// needs to know what was on disk in order to back it up.
	onDisk := f.SchemaVersion
	if err := migrate(&f); err != nil {
		return nil, fmt.Errorf("migrating %s from schema v%d: %w", s.path, onDisk, err)
	}

	s.loadedVersion = onDisk
	s.digest = digest(raw)
	sort.Slice(f.Instance, func(i, j int) bool { return f.Instance[i].ID < f.Instance[j].ID })
	return f.Instance, nil
}

// LoadedVersion is the schema version of the file last read. It equals
// SchemaVersion for a file this build wrote, and is lower for one written by
// an older build that Save is about to upgrade in place.
func (s *Store) LoadedVersion() int { return s.loadedVersion }

// BackupPath is where Save stashes the original file before rewriting it in a
// newer schema.
func (s *Store) BackupPath(fromVersion int) string {
	return fmt.Sprintf("%s.v%d.bak", s.path, fromVersion)
}

// migrate brings a parsed file up to SchemaVersion in memory.
//
// Nothing here rewrites the file; Save does that, after taking a backup. The
// v0 case is deliberately a no-op on the instance fields: the daemon already
// fills in restart, desired and scope from what it can observe about the live
// container, which is better evidence than anything this layer could invent.
// Its job is only to stop a v0 file being mistaken for a v1 one.
func migrate(f *file) error {
	for f.SchemaVersion < SchemaVersion {
		switch f.SchemaVersion {
		case 0:
			f.SchemaVersion = 1
		default:
			return fmt.Errorf("no migration from v%d", f.SchemaVersion)
		}
	}
	return nil
}

// ChangedOnDisk reports whether the file differs from what we last read or
// wrote. A file that has since been deleted counts as changed.
func (s *Store) ChangedOnDisk() (bool, error) {
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s.digest != "", nil
	}
	if err != nil {
		return false, err
	}
	return digest(raw) != s.digest, nil
}

// Save writes the index atomically. It refuses to write if the file changed
// externally since Load, unless force is set.
func (s *Store) Save(instances []model.Instance, force bool) error {
	if !force {
		changed, err := s.ChangedOnDisk()
		if err != nil {
			return err
		}
		if changed {
			return ErrChangedOnDisk
		}
	}

	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}

	// Rewriting an older file in the current schema is the one write that
	// cannot be undone by reinstalling the old version, so keep the original.
	if s.loadedVersion < SchemaVersion && !s.backedUp {
		if err := s.backup(); err != nil {
			return err
		}
	}

	sorted := append([]model.Instance(nil), instances...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })

	var buf []byte
	{
		w := &byteWriter{}
		enc := toml.NewEncoder(w)
		if err := enc.Encode(file{SchemaVersion: SchemaVersion, Instance: sorted}); err != nil {
			return fmt.Errorf("encoding config: %w", err)
		}
		buf = w.b
	}

	// Write to a temp file in the same directory, fsync, then rename. The
	// rename is atomic, so a crash or a full disk mid-write leaves the old
	// file intact rather than a truncated one (spec 7, phase 1).
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".instances-*.toml")
	if err != nil {
		return fmt.Errorf("creating temp config: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if err := tmp.Chmod(0o600); err != nil { // contains generated passwords
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(buf); err != nil {
		tmp.Close()
		return fmt.Errorf("writing temp config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("syncing temp config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("replacing config: %w", err)
	}

	s.digest = digest(buf)
	s.loadedVersion = SchemaVersion
	return nil
}

// backup copies the current file aside before a schema upgrade rewrites it.
// A missing file needs no backup; an existing backup is never overwritten, so
// the oldest -- and so the most original -- copy is the one that survives.
func (s *Store) backup() error {
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		s.backedUp = true
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading config for backup: %w", err)
	}
	dst := s.BackupPath(s.loadedVersion)
	if _, err := os.Stat(dst); err == nil {
		s.backedUp = true
		return nil
	}
	if err := os.WriteFile(dst, raw, 0o600); err != nil {
		return fmt.Errorf("writing schema backup %s: %w", dst, err)
	}
	s.backedUp = true
	return nil
}

type byteWriter struct{ b []byte }

func (w *byteWriter) Write(p []byte) (int, error) { w.b = append(w.b, p...); return len(p), nil }

func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
