//go:build integration

// Package integration exercises DBForge against a real rootless Podman.
//
// Run with:
//
//	go test -tags integration -timeout 15m ./test/integration/
//
// These tests pull real images and start real databases. They are excluded
// from the default build because they need a working Podman socket and take
// minutes rather than milliseconds.
package integration

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fiifiofosu/dbforge/internal/daemon"
	"github.com/fiifiofosu/dbforge/internal/model"
	"github.com/fiifiofosu/dbforge/internal/ports"
	"github.com/fiifiofosu/dbforge/internal/runtime"
	"github.com/fiifiofosu/dbforge/internal/store"
)

func newManager(t *testing.T) (*daemon.Manager, *runtime.Podman, string) {
	t.Helper()
	ctx := context.Background()

	rt, err := runtime.NewPodman(ctx, os.Getenv("DBFORGE_PODMAN_SOCKET"))
	if err != nil {
		t.Skipf("podman unavailable: %v", err)
	}
	if err := rt.Ping(ctx); err != nil {
		t.Skipf("podman socket not responding: %v", err)
	}

	dir := t.TempDir()
	dataRoot := filepath.Join(dir, "data")
	m := daemon.NewManager(rt, store.New(filepath.Join(dir, "instances.toml")), daemon.Config{
		DataRoot:  dataRoot,
		PortRange: ports.Range{Low: 15700, High: 15799},
		// Own scope, so these tests never touch a real installation's
		// containers -- and a developer's running instances never break them.
		Scope: testScope(t),
	})
	if _, err := m.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	return m, rt, dataRoot
}

// cleanup removes an instance and its data, tolerating an already-gone one.
func cleanup(t *testing.T, m *daemon.Manager, id string) {
	t.Helper()
	ctx := context.Background()
	if err := m.Remove(ctx, id, daemon.RemoveOptions{WipeData: true, Force: true}); err != nil &&
		!strings.Contains(err.Error(), "not found") {
		t.Logf("cleanup of %s: %v", id, err)
	}
}

// waitForPort waits until something accepts connections, or fails the test.
func waitForPort(t *testing.T, port int, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	var last error
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 2*time.Second)
		if err == nil {
			c.Close()
			return
		}
		last = err
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("port %d never became reachable within %s: %v", port, within, last)
}

func TestPostgresLifecycle(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newManager(t)
	const id = "it-postgres"
	t.Cleanup(func() { cleanup(t, m, id) })

	inst, err := m.Create(ctx, daemon.CreateOptions{Ref: "postgres:16", ID: id, Start: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if inst.Status != model.StatusRunning {
		t.Fatalf("status = %q, want running", inst.Status)
	}

	// Reachable from the host, which is the whole point -- the container
	// running is not the same as the user being able to connect.
	waitForPort(t, inst.Port, 90*time.Second)

	// The data directory must exist and be non-empty once initdb has run.
	entries, err := os.ReadDir(inst.DataDir)
	if err != nil {
		// Postgres chowns the mount to its own subuid, so the host user may
		// not be able to list it. That is expected, not a failure.
		t.Logf("data dir not listable by host user (expected under rootless): %v", err)
	} else if len(entries) == 0 {
		t.Fatal("data directory is empty after initdb")
	}

	if err := m.Stop(ctx, id, 30); err != nil {
		t.Fatalf("stop: %v", err)
	}
	got, _ := m.Get(id)
	if got.Status != model.StatusStopped {
		t.Fatalf("after stop, status = %q, want stopped", got.Status)
	}

	if err := m.Start(ctx, id); err != nil {
		t.Fatalf("restart: %v", err)
	}
	waitForPort(t, inst.Port, 90*time.Second)
}

func TestRedisLifecycleAndSideBySide(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newManager(t)
	const a, b = "it-redis-a", "it-redis-b"
	t.Cleanup(func() { cleanup(t, m, a); cleanup(t, m, b) })

	ia, err := m.Create(ctx, daemon.CreateOptions{Ref: "redis:7", ID: a, Start: true})
	if err != nil {
		t.Fatalf("create %s: %v", a, err)
	}
	ib, err := m.Create(ctx, daemon.CreateOptions{Ref: "redis:7", ID: b, Start: true})
	if err != nil {
		t.Fatalf("create %s: %v", b, err)
	}

	// Two instances of the same engine and version must not collide.
	if ia.Port == ib.Port {
		t.Fatalf("both instances got port %d", ia.Port)
	}
	if ia.DataDir == ib.DataDir {
		t.Fatal("both instances share a data directory")
	}

	waitForPort(t, ia.Port, 60*time.Second)
	waitForPort(t, ib.Port, 60*time.Second)

	// Both must answer independently.
	for _, p := range []int{ia.Port, ib.Port} {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", p), 3*time.Second)
		if err != nil {
			t.Fatalf("dial %d: %v", p, err)
		}
		c.SetDeadline(time.Now().Add(3 * time.Second))
		c.Write([]byte("PING\r\n"))
		buf := make([]byte, 16)
		n, err := c.Read(buf)
		c.Close()
		if err != nil || !strings.Contains(string(buf[:n]), "PONG") {
			t.Fatalf("redis on %d did not PONG: %q %v", p, buf[:n], err)
		}
	}
}

// TestDataSurvivesStopStart is the guarantee the whole tool rests on.
func TestDataSurvivesStopStart(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newManager(t)
	const id = "it-persist"
	t.Cleanup(func() { cleanup(t, m, id) })

	inst, err := m.Create(ctx, daemon.CreateOptions{Ref: "redis:7", ID: id, Start: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	waitForPort(t, inst.Port, 60*time.Second)

	// Write, then force a save so the value is on disk before we stop.
	redis(t, inst.Port, "SET", "persisted", "yes")
	redis(t, inst.Port, "SAVE")

	if err := m.Stop(ctx, id, 30); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := m.Start(ctx, id); err != nil {
		t.Fatalf("start: %v", err)
	}
	waitForPort(t, inst.Port, 60*time.Second)

	if got := redis(t, inst.Port, "GET", "persisted"); !strings.Contains(got, "yes") {
		t.Fatalf("value did not survive stop/start: %q", got)
	}
}

// TestWipeRemovesSubuidOwnedData covers the rootless ownership trap: a
// container running as a non-root user leaves files owned by a subuid that the
// host user cannot unlink, so a plain os.RemoveAll fails with EPERM.
func TestWipeRemovesSubuidOwnedData(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newManager(t)
	const id = "it-wipe"

	inst, err := m.Create(ctx, daemon.CreateOptions{Ref: "postgres:16", ID: id, Start: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	waitForPort(t, inst.Port, 90*time.Second)

	// Plain removal is expected to fail; that is the trap being covered.
	if err := os.RemoveAll(inst.DataDir); err == nil {
		if _, statErr := os.Stat(inst.DataDir); os.IsNotExist(statErr) {
			t.Skip("data dir was host-owned; this environment does not exercise the subuid path")
		}
	}

	if err := m.Remove(ctx, id, daemon.RemoveOptions{WipeData: true, Force: true}); err != nil {
		t.Fatalf("wipe: %v", err)
	}
	if _, err := os.Stat(inst.DataDir); !os.IsNotExist(err) {
		t.Fatalf("data directory survived the wipe: %v", err)
	}
}

// TestDriftAfterExternalRemoval covers a user running `podman rm` directly.
func TestDriftAfterExternalRemoval(t *testing.T) {
	ctx := context.Background()
	m, rt, _ := newManager(t)
	const id = "it-drift"
	t.Cleanup(func() { cleanup(t, m, id) })

	inst, err := m.Create(ctx, daemon.CreateOptions{Ref: "redis:7", ID: id, Start: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := rt.Remove(ctx, inst.ContainerName(), true); err != nil {
		t.Fatalf("external remove: %v", err)
	}

	list, err := m.List(ctx)
	if err != nil {
		t.Fatalf("list after external removal: %v", err)
	}
	var found *model.Instance
	for i := range list {
		if list[i].ID == id {
			found = &list[i]
		}
	}
	if found == nil {
		t.Fatal("instance disappeared; it should be kept and marked missing")
	}
	if found.Status != model.StatusMissing {
		t.Fatalf("status = %q, want missing", found.Status)
	}
}

func TestNonexistentTagFailsFast(t *testing.T) {
	ctx := context.Background()
	m, _, dataRoot := newManager(t)

	start := time.Now()
	_, err := m.Create(ctx, daemon.CreateOptions{Ref: "postgres:99999", ID: "it-badtag"})
	if err == nil {
		cleanup(t, m, "it-badtag")
		t.Fatal("creating an instance from a nonexistent tag succeeded")
	}
	t.Logf("failed in %s: %v", time.Since(start).Round(time.Millisecond), err)

	// Nothing should be left behind.
	if _, err := m.Get("it-badtag"); err == nil {
		t.Fatal("a failed create left a tracked instance")
	}
	if entries, err := os.ReadDir(filepath.Join(dataRoot, "postgres")); err == nil && len(entries) > 0 {
		t.Fatalf("a failed create left data directories: %v", entries)
	}
}

// redis sends a command using the inline protocol and returns the reply.
func redis(t *testing.T, port int, args ...string) string {
	t.Helper()
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 3*time.Second)
	if err != nil {
		t.Fatalf("dial redis: %v", err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Write([]byte(strings.Join(args, " ") + "\r\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 256)
	n, err := c.Read(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(buf[:n])
}
