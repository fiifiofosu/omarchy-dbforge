package runtime

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

// Fake is an in-memory Runtime for tests. It is in the non-test build so the
// daemon package can use it too.
type Fake struct {
	mu         sync.Mutex
	Containers map[string]*Container
	Images     map[string]bool
	// PullErr, if set, is returned by PullImage -- used to simulate a
	// nonexistent upstream tag.
	PullErr error
	// CreateErr, if set, is returned by Create -- used to simulate a disk-full
	// or subuid-exhaustion failure partway through a create.
	CreateErr error
	// RemoveErr, if set, is returned by Remove -- used to simulate podman
	// refusing to remove a running container.
	RemoveErr error
	// RemovePathErr, if set, is returned by RemovePath -- used to simulate a
	// failure to delete a subuid-owned data directory.
	RemovePathErr error
	// Pulled records images PullImage was asked for.
	Pulled []string
}

func NewFake() *Fake {
	return &Fake{Containers: map[string]*Container{}, Images: map[string]bool{}}
}

func (f *Fake) Ping(context.Context) error { return nil }

func (f *Fake) ListManaged(_ context.Context, labelKey string) ([]Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Container
	for _, c := range f.Containers {
		if _, ok := c.Labels[labelKey]; ok {
			out = append(out, *c)
		}
	}
	return out, nil
}

func (f *Fake) Inspect(_ context.Context, name string) (Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.Containers[name]
	if !ok {
		return Container{}, ErrNotFound
	}
	return *c, nil
}

func (f *Fake) ImageExists(_ context.Context, image string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Images[image], nil
}

func (f *Fake) PullImage(_ context.Context, image string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Pulled = append(f.Pulled, image)
	if f.PullErr != nil {
		return f.PullErr
	}
	f.Images[image] = true
	return nil
}

func (f *Fake) Create(_ context.Context, spec CreateSpec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.CreateErr != nil {
		return "", f.CreateErr
	}
	if _, exists := f.Containers[spec.Name]; exists {
		return "", fmt.Errorf("container %s already exists", spec.Name)
	}
	id := "ctr-" + spec.Name
	f.Containers[spec.Name] = &Container{
		ID: id, Name: spec.Name, State: StateStopped,
		Labels: spec.Labels, HostPort: spec.HostPort,
	}
	return id, nil
}

func (f *Fake) Start(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.Containers[name]
	if !ok {
		return ErrNotFound
	}
	c.State = StateRunning
	return nil
}

func (f *Fake) Stop(_ context.Context, name string, _ uint) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.Containers[name]
	if !ok {
		return ErrNotFound
	}
	c.State = StateStopped
	return nil
}

func (f *Fake) Remove(_ context.Context, name string, _ bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.RemoveErr != nil {
		return f.RemoveErr
	}
	if _, ok := f.Containers[name]; !ok {
		return ErrNotFound
	}
	delete(f.Containers, name)
	return nil
}

// RemovePath in the fake is a plain delete: tests run without a user namespace.
func (f *Fake) RemovePath(_ context.Context, path string) error {
	if f.RemovePathErr != nil {
		return f.RemovePathErr
	}
	return os.RemoveAll(path)
}

func (f *Fake) Logs(_ context.Context, name string, _ bool, _ int, w io.Writer) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.Containers[name]; !ok {
		return ErrNotFound
	}
	_, err := io.Copy(w, strings.NewReader("log line from "+name+"\n"))
	return err
}
