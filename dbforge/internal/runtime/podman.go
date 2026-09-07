// Podman implementation of Runtime, using the official bindings.
//
// Note the module path: as of Podman 6 the bindings moved from
// github.com/containers/podman/v5 to go.podman.io/podman/v6.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	specs "github.com/opencontainers/runtime-spec/specs-go"
	nettypes "go.podman.io/common/libnetwork/types"
	"go.podman.io/podman/v6/pkg/bindings"
	"go.podman.io/podman/v6/pkg/bindings/containers"
	"go.podman.io/podman/v6/pkg/bindings/images"
	"go.podman.io/podman/v6/pkg/domain/entities/types"
	"go.podman.io/podman/v6/pkg/errorhandling"
	"go.podman.io/podman/v6/pkg/specgen"
)

// Podman drives a rootless Podman via its REST socket.
type Podman struct{ conn context.Context }

// DefaultSocket returns the rootless Podman socket path for this user,
// honouring the conventional XDG_RUNTIME_DIR location.
func DefaultSocket() string {
	if s := os.Getenv("DBFORGE_PODMAN_SOCKET"); s != "" {
		return s
	}
	if s := os.Getenv("CONTAINER_HOST"); s != "" {
		return s
	}
	run := os.Getenv("XDG_RUNTIME_DIR")
	if run == "" {
		run = fmt.Sprintf("/run/user/%d", os.Getuid())
	}
	return "unix://" + run + "/podman/podman.sock"
}

// NewPodman connects to the Podman socket. The returned error is deliberately
// verbose: "cannot reach podman" is the single most common support question,
// and `dbctl doctor` surfaces this text directly.
func NewPodman(ctx context.Context, socket string) (*Podman, error) {
	if socket == "" {
		socket = DefaultSocket()
	}
	conn, err := bindings.NewConnection(ctx, socket)
	if err != nil {
		return nil, fmt.Errorf("connecting to podman at %s: %w\n"+
			"hint: enable the rootless socket with `systemctl --user enable --now podman.socket`", socket, err)
	}
	return &Podman{conn: conn}, nil
}

func (p *Podman) Ping(context.Context) error {
	_, err := images.List(p.conn, &images.ListOptions{})
	return err
}

func (p *Podman) ListManaged(_ context.Context, labelKey string) ([]Container, error) {
	all := true
	filters := map[string][]string{"label": {labelKey}}
	list, err := containers.List(p.conn, &containers.ListOptions{All: &all, Filters: filters})
	if err != nil {
		return nil, err
	}
	out := make([]Container, 0, len(list))
	for _, c := range list {
		out = append(out, fromListContainer(c))
	}
	return out, nil
}

func (p *Podman) Inspect(_ context.Context, name string) (Container, error) {
	data, err := containers.Inspect(p.conn, name, nil)
	if err != nil {
		if isNotFound(err) {
			return Container{}, ErrNotFound
		}
		return Container{}, err
	}
	c := Container{
		ID:     data.ID,
		Name:   strings.TrimPrefix(data.Name, "/"),
		State:  mapState(data.State.Status),
		Labels: data.Config.Labels,
	}
	if data.State != nil {
		c.ExitCode = int(data.State.ExitCode)
	}
	return c, nil
}

func (p *Podman) ImageExists(_ context.Context, image string) (bool, error) {
	return images.Exists(p.conn, image, nil)
}

func (p *Podman) PullImage(_ context.Context, image string) error {
	_, err := images.Pull(p.conn, image, &images.PullOptions{})
	if err != nil {
		// A nonexistent tag surfaces here. Keep the registry's own wording --
		// it is more informative than anything we would invent.
		return fmt.Errorf("pulling %s: %w", image, err)
	}
	return nil
}

func (p *Podman) Create(_ context.Context, spec CreateSpec) (string, error) {
	s := specgen.NewSpecGenerator(spec.Image, false)
	s.Name = spec.Name
	s.Labels = spec.Labels
	s.Env = spec.Env
	s.Remove = boolPtr(false)

	if spec.TZ != "" {
		s.Timezone = spec.TZ
	}

	s.PortMappings = []nettypes.PortMapping{{
		HostIP:        "127.0.0.1", // never expose an instance beyond loopback
		HostPort:      uint16(spec.HostPort),
		ContainerPort: uint16(spec.ContainerPort),
		Protocol:      "tcp",
	}}

	if spec.HostDataDir != "" {
		s.Mounts = []specs.Mount{{
			Type:        "bind",
			Source:      spec.HostDataDir,
			Destination: spec.ContainerDataDir,
			Options:     []string{"rbind", "rw"},
		}}
	}

	if spec.MemoryLimitBytes > 0 || spec.NanoCPUs > 0 {
		s.ResourceLimits = &specs.LinuxResources{}
		if spec.MemoryLimitBytes > 0 {
			s.ResourceLimits.Memory = &specs.LinuxMemory{Limit: &spec.MemoryLimitBytes}
		}
		if spec.NanoCPUs > 0 {
			quota := spec.NanoCPUs / 1000
			period := uint64(100000)
			s.ResourceLimits.CPU = &specs.LinuxCPU{Quota: &quota, Period: &period}
		}
	}

	resp, err := containers.CreateWithSpec(p.conn, s, nil)
	if err != nil {
		return "", err
	}
	return resp.ID, nil
}

func (p *Podman) Start(_ context.Context, name string) error {
	err := containers.Start(p.conn, name, nil)
	if isNotFound(err) {
		return ErrNotFound
	}
	return err
}

func (p *Podman) Stop(_ context.Context, name string, timeoutSecs uint) error {
	opts := &containers.StopOptions{}
	if timeoutSecs > 0 {
		opts.Timeout = &timeoutSecs
	}
	err := containers.Stop(p.conn, name, opts)
	if isNotFound(err) {
		return ErrNotFound
	}
	return err
}

func (p *Podman) Remove(_ context.Context, name string, force bool) error {
	_, err := containers.Remove(p.conn, name, &containers.RemoveOptions{Force: &force})
	if isNotFound(err) {
		return ErrNotFound
	}
	return err
}

func (p *Podman) Logs(ctx context.Context, name string, follow bool, tail int, w io.Writer) error {
	stdout := make(chan string, 64)
	stderr := make(chan string, 64)

	done := make(chan error, 1)
	go func() {
		opts := &containers.LogOptions{
			Follow: &follow,
			Stdout: boolPtr(true),
			Stderr: boolPtr(true),
		}
		if tail > 0 {
			opts.Tail = strPtr(strconv.Itoa(tail))
		}
		err := containers.Logs(p.conn, name, opts, stdout, stderr)
		close(stdout)
		close(stderr)
		if isNotFound(err) {
			err = ErrNotFound
		}
		done <- err
	}()

	for {
		select {
		case <-ctx.Done():
			// Backing out of a log tail must not leak the goroutine's writes
			// into a closed writer (spec 7, phase 3).
			return ctx.Err()
		case line, ok := <-stdout:
			if !ok {
				stdout = nil
				break
			}
			io.WriteString(w, line)
		case line, ok := <-stderr:
			if !ok {
				stderr = nil
				break
			}
			io.WriteString(w, line)
		case err := <-done:
			return err
		}
	}
}

func fromListContainer(c types.ListContainer) Container {
	name := ""
	if len(c.Names) > 0 {
		name = c.Names[0]
	}
	out := Container{
		ID: c.ID, Name: name, State: mapState(c.State),
		Labels: c.Labels, ExitCode: int(c.ExitCode),
	}
	for _, pm := range c.Ports {
		if pm.HostPort != 0 {
			out.HostPort = int(pm.HostPort)
			break
		}
	}
	return out
}

func mapState(s string) State {
	switch strings.ToLower(s) {
	case "running":
		return StateRunning
	case "exited", "stopped", "created", "configured":
		return StateStopped
	case "paused", "removing", "stopping":
		return StateOther
	default:
		return StateOther
	}
}

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	var e *errorhandling.ErrorModel
	if errors.As(err, &e) {
		return e.ResponseCode == 404
	}
	return strings.Contains(strings.ToLower(err.Error()), "no such container")
}

func boolPtr(b bool) *bool    { return &b }
func strPtr(s string) *string { return &s }

// RemovePath deletes a host path from inside the rootless user namespace.
//
// This is the one place DBForge shells out rather than using the bindings.
// `podman unshare` re-executes a command inside the same UID mapping the
// containers use, which is the only way to unlink files a container created
// as a non-root user: on the host those are owned by a subuid (uid 999 in the
// container becomes 100998 here) that we have no permission to touch.
//
// The bindings expose no equivalent, because the operation is fundamentally
// about re-executing a process in a namespace rather than an API call.
func (p *Podman) RemovePath(ctx context.Context, path string) error {
	if !filepath.IsAbs(path) || path == "/" {
		return fmt.Errorf("refusing to remove non-absolute or root path %q", path)
	}

	// Fast path: if it is already ours, no namespace juggling needed.
	if err := os.RemoveAll(path); err == nil {
		return nil
	}

	cmd := exec.CommandContext(ctx, "podman", "unshare", "rm", "-rf", "--", path)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("removing %s via podman unshare: %w: %s",
			path, err, strings.TrimSpace(string(out)))
	}
	if _, statErr := os.Stat(path); statErr == nil {
		return fmt.Errorf("path %s still exists after removal", path)
	}
	return nil
}
