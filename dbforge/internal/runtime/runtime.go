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
}

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
	PullImage(ctx context.Context, image string) error
	// Create makes a container and returns its ID.
	Create(ctx context.Context, spec CreateSpec) (string, error)
	Start(ctx context.Context, name string) error
	Stop(ctx context.Context, name string, timeoutSecs uint) error
	Remove(ctx context.Context, name string, force bool) error
	// Logs streams container logs to w. If follow is set it blocks until ctx
	// is cancelled.
	Logs(ctx context.Context, name string, follow bool, tail int, w io.Writer) error
}
