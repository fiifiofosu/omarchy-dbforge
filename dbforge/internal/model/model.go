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

// RestartPolicy says what should happen to an instance across daemon restarts
// and host reboots (spec 7, phase 2).
type RestartPolicy string

const (
	// RestartNo means the instance is only ever started explicitly. Choose
	// this for a database you want off unless you ask for it.
	RestartNo RestartPolicy = "no"
	// RestartOnFailure brings the instance back if it exited uncleanly, but
	// not if you stopped it deliberately.
	RestartOnFailure RestartPolicy = "on-failure"
	// RestartAlways brings the instance back whenever it should be running --
	// after a crash, a daemon restart, or a reboot.
	RestartAlways RestartPolicy = "always"
)

// DefaultRestartPolicy applies when the user does not choose. "always" matches
// the expectation set by DBngin and by docker-compose: a database you created
// is there tomorrow without being told again.
const DefaultRestartPolicy = RestartAlways

// ValidRestartPolicy reports whether p is one we understand.
func ValidRestartPolicy(p RestartPolicy) bool {
	switch p {
	case RestartNo, RestartOnFailure, RestartAlways:
		return true
	}
	return false
}

// DesiredState is what the user last asked for, as opposed to what is
// currently true. Keeping the two apart is what lets the daemon restore
// instances after a reboot without resurrecting ones you deliberately stopped.
type DesiredState string

const (
	// DesiredRunning means the user created or started this instance and has
	// not stopped it since.
	DesiredRunning DesiredState = "running"
	// DesiredStopped means the user explicitly stopped it.
	DesiredStopped DesiredState = "stopped"
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

	// Restart is the policy for bringing this instance back automatically.
	Restart RestartPolicy `json:"restart" toml:"restart"`

	// Desired is what the user last asked for. Unlike Status it *is*
	// authoritative when persisted: only an explicit start or stop changes it,
	// so a container that died on its own does not look like a deliberate stop.
	Desired DesiredState `json:"desired" toml:"desired"`

	// StartedAt is when the container last started. Derived, not persisted as
	// truth; zero when the instance is not running.
	StartedAt time.Time `json:"started_at" toml:"-"`

	// LastExitCode is the container's exit code when it is not running. A
	// nonzero value means an unclean stop, which for engines like Postgres
	// means crash recovery will run on the next boot -- worth surfacing rather
	// than masking (spec 7, phase 2).
	LastExitCode int `json:"last_exit_code" toml:"last_exit_code"`

	// ContainerID is Podman's ID for the container backing this instance.
	ContainerID string `json:"container_id" toml:"container_id"`

	// Scope isolates one DBForge installation from another on the same host.
	// Containers are found by label, so without this a second daemon -- a test
	// run, or a throwaway config -- would adopt the containers belonging to
	// the real one.
	Scope string `json:"scope" toml:"scope"`
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
		"io.dbforge.restart":  string(i.Restart),
		"io.dbforge.desired":  string(i.Desired),
		"io.dbforge.scope":    i.Scope,
	}
}

// ShouldAutoStart reports whether the daemon should bring this instance back
// on its own, given its policy and what the user last asked for.
//
// An instance the user stopped is never resurrected, whatever the policy says
// -- that would make `dbctl stop` meaningless across a reboot.
func (i Instance) ShouldAutoStart() bool {
	if i.Desired != DesiredRunning {
		return false
	}
	switch i.Restart {
	case RestartAlways:
		return true
	case RestartOnFailure:
		// Only if it did not exit cleanly. A clean exit with the user still
		// wanting it running means something stopped it outside dbforge, and
		// on-failure deliberately does not cover that.
		return i.LastExitCode != 0
	default:
		return false
	}
}

// Uptime is how long the instance has been running, or zero if it is not.
func (i Instance) Uptime() time.Duration {
	if i.Status != StatusRunning || i.StartedAt.IsZero() {
		return 0
	}
	return time.Since(i.StartedAt)
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
