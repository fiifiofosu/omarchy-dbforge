// Package model defines the core types shared by the daemon, the CLI and the
// on-disk config. These types are the wire format as well as the storage
// format, so changes here are schema changes -- see store.SchemaVersion.
package model

import "time"

// Status is the lifecycle state of an instance. It is always derived at read
// time by reconciling against Podman; it is never trusted from disk (spec 6).
type Status string

const (
	// StatusRunning means the container exists and is running.
	StatusRunning Status = "running"
	// StatusStopped means the container exists but is not running.
	StatusStopped Status = "stopped"
	// StatusMissing means we have a config entry but Podman has no matching
	// container -- e.g. the user ran `podman rm` behind our back. We surface
	// the drift rather than silently recreating it (spec 7, phase 1).
	StatusMissing Status = "missing"
	// StatusError means the container exists but is in a failed state.
	StatusError Status = "error"
	// StatusCreating is a transient state persisted before the container
	// exists, so that a daemon crash mid-create leaves a breadcrumb the
	// startup reconciliation can clean up (spec 7, phase 1).
	StatusCreating Status = "creating"
)

// Instance is one managed database instance.
type Instance struct {
	ID        string            `json:"id" toml:"id"`
	Engine    string            `json:"engine" toml:"engine"`
	Version   string            `json:"version" toml:"version"`
	Image     string            `json:"image" toml:"image"`
	Port      int               `json:"port" toml:"port"`
	DataDir   string            `json:"data_dir" toml:"data_dir"`
	Env       map[string]string `json:"-" toml:"env"`
	CreatedAt time.Time         `json:"created_at" toml:"created_at"`

	// Status is derived, not persisted as truth. It is written to the file
	// only as a hint for crash recovery.
	Status Status `json:"status" toml:"status"`

	// ContainerID is Podman's ID for the container backing this instance.
	ContainerID string `json:"container_id" toml:"container_id"`
}

// ContainerName is the Podman container name for an instance. The dbforge-
// prefix is what lets reconciliation find our containers among the user's own.
func (i Instance) ContainerName() string { return "dbforge-" + i.ID }

// Labels are stamped onto the container so the daemon can reconstruct its
// entire state from Podman alone -- Podman is ground truth, the TOML file is
// only an index (spec 6).
func (i Instance) Labels() map[string]string {
	return map[string]string{
		"io.dbforge.managed":  "true",
		"io.dbforge.id":       i.ID,
		"io.dbforge.engine":   i.Engine,
		"io.dbforge.version":  i.Version,
		"io.dbforge.port":     itoa(i.Port),
		"io.dbforge.data_dir": i.DataDir,
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	p := len(b)
	for n > 0 {
		p--
		b[p] = byte('0' + n%10)
		n /= 10
	}
	return string(b[p:])
}
