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

// SchemaVersion is written into the file so a future release can migrate an
// older config rather than misreading it (spec 7, phase 5).
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

// Path returns the file this store manages.
func (s *Store) Path() string { return s.path }

// Load reads the index. A missing file is not an error: it is an empty index.
func (s *Store) Load() ([]model.Instance, error) {
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		s.digest = ""
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
		return nil, fmt.Errorf("%w: file is v%d, this build understands v%d",
			ErrSchemaTooNew, f.SchemaVersion, SchemaVersion)
	}

	s.digest = digest(raw)
	sort.Slice(f.Instance, func(i, j int) bool { return f.Instance[i].ID < f.Instance[j].ID })
	return f.Instance, nil
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
	return nil
}

type byteWriter struct{ b []byte }

func (w *byteWriter) Write(p []byte) (int, error) { w.b = append(w.b, p...); return len(p), nil }

func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
