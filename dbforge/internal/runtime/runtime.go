// Package runtime abstracts the container engine.
//
// Everything above this package is written against the Runtime interface, so
// the daemon's logic can be tested without a live Podman -- and so the Podman
// binding can be swapped (for the REST socket, or for Docker) without touching
// lifecycle code.
package runtime

import (
	"context"
	"errors"
	"io"
	"time"
)

// ErrNotFound means the container or image does not exist.
var ErrNotFound = errors.New("not found")

// State is the container's state as the engine reports it.
type State string

const (
	StateRunning State = "running"
	StateStopped State = "stopped"
	StateFailed  State = "failed"
	StateOther   State = "other"
)

// Container is the subset of container detail DBForge cares about.
type Container struct {
	ID     string
	Name   string
	State  State
	Labels map[string]string
	// ExitCode is meaningful only when the container has stopped.
	ExitCode int
	// HostPort is the published host port, or 0 if none.
	HostPort int
	// StartedAt is when the container last started, used for uptime.
	StartedAt time.Time
}

// CreateSpec describes a container to create.
type CreateSpec struct {
	Name          string
	Image         string
	Env           map[string]string
	Labels        map[string]string
	HostPort      int
	ContainerPort int
	// HostDataDir is bind-mounted at ContainerDataDir.
	HostDataDir      string
	ContainerDataDir string
	// MemoryLimitBytes and NanoCPUs cap the instance so a runaway engine
	// cannot starve the host (spec 8). Zero means unlimited.
	MemoryLimitBytes int64
	NanoCPUs         int64
	// TZ is passed through so container log timestamps match the host (spec 8).
	TZ string
	// StopTimeoutSecs is baked into the container so that a stop issued
	// outside dbforge (podman stop, or a host shutdown) also gets the engine's
	// full shutdown budget rather than podman's 10s default.
	StopTimeoutSecs uint
	// RestartPolicy is podman's own policy ("no", "on-failure", "always").
	// It covers the container dying while the host stays up; bringing
	// instances back after a reboot is the daemon's job, since rootless
	// containers are not started by podman on boot unless podman-restart is
	// enabled (spec 7, phase 2).
	RestartPolicy string
}

// PullEvent is one step of an image pull, as reported by the runtime.
//
// Podman's HTTP API reports pull progress as human-readable phase lines --
// "Copying blob sha256:...", "Writing manifest to image destination" -- with
// no byte counts and no percentage. That is a hard limit of the API, not a
// simplification made here: there is no total to divide by, so anything
// claiming to be a percentage would be invented. What can be reported
// honestly is the phase, and how many layers have started.
type PullEvent struct {
	// Message is a short human-readable description of the current phase.
	Message string `json:"message"`
	// Layer counts layers whose download has begun, 1-based. Zero outside the
	// copying phase. There is no known total: the API announces each layer as
	// it starts, so the last one is only recognisable in hindsight.
	Layer int `json:"layer,omitempty"`
}

// PullProgress receives PullEvents as a pull proceeds.
type PullProgress func(PullEvent)

// Runtime is the container engine DBForge drives.
type Runtime interface {
	// Ping verifies the engine is reachable.
	Ping(ctx context.Context) error
	// ListManaged returns containers carrying the given label key.
	ListManaged(ctx context.Context, labelKey string) ([]Container, error)
	// Inspect returns one container by name.
	Inspect(ctx context.Context, name string) (Container, error)
	// ImageExists reports whether the image is present locally.
	ImageExists(ctx context.Context, image string) (bool, error)
	// PullImage fetches an image, returning a clear error for a bad tag.
	//
	// onProgress, when non-nil, is called as the pull proceeds. It is called
	// from the calling goroutine, so an implementation must not assume it is
	// cheap -- but it must also never block forever, or the pull stalls.
	PullImage(ctx context.Context, image string, onProgress PullProgress) error
	// Create makes a container and returns its ID.
	Create(ctx context.Context, spec CreateSpec) (string, error)
	Start(ctx context.Context, name string) error
	Stop(ctx context.Context, name string, timeoutSecs uint) error
	Remove(ctx context.Context, name string, force bool) error
	// Logs streams container logs to w. If follow is set it blocks until ctx
	// is cancelled.
	Logs(ctx context.Context, name string, follow bool, tail int, w io.Writer) error

	// RemovePath deletes a host path that may be owned by a subordinate UID.
	//
	// Under rootless Podman a container process running as a non-root user
	// (postgres runs as uid 999) has its files land on the host owned by a
	// subuid -- 100998, not our 1000. We cannot unlink those directly, so a
	// plain os.RemoveAll fails with EPERM. This must run inside the user
	// namespace instead.
	RemovePath(ctx context.Context, path string) error
}
