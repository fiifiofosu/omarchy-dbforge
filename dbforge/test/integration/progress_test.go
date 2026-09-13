//go:build integration

package integration

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/fiifiofosu/dbforge/internal/daemon"
	"github.com/fiifiofosu/dbforge/internal/runtime"
)

// TestPullReportsRealProgress covers the part the unit tests cannot: that
// podman actually sends progress over the socket, and that the parser matches
// the wording it really uses.
//
// The parser is tested against lines captured by hand; this is what catches
// podman changing them. An image is removed first so the pull is real -- an
// already-present image reports nothing because nothing is downloaded.
func TestPullReportsRealProgress(t *testing.T) {
	ctx := context.Background()
	rt, err := runtime.NewPodman(ctx, os.Getenv("DBFORGE_PODMAN_SOCKET"))
	if err != nil {
		t.Skipf("podman unavailable: %v", err)
	}

	// Small, and not one of the engine images, so removing it cannot disturb
	// a developer's cached postgres or redis. Removed via the CLI rather than
	// growing the Runtime interface with a method only a test needs.
	const image = "docker.io/library/alpine:3.20"
	rmi := func() { _ = exec.Command("podman", "rmi", "-f", image).Run() }
	t.Cleanup(rmi)
	rmi()

	var events []runtime.PullEvent
	start := time.Now()
	if err := rt.PullImage(ctx, image, func(ev runtime.PullEvent) {
		events = append(events, ev)
	}); err != nil {
		t.Fatalf("pull: %v", err)
	}
	t.Logf("%d events in %s", len(events), time.Since(start).Round(time.Millisecond))
	for _, ev := range events {
		t.Logf("  layer=%d %s", ev.Layer, ev.Message)
	}

	if len(events) == 0 {
		t.Fatal("a real pull reported no progress at all")
	}

	// Every event must be something worth showing a user: a classified phase,
	// never an empty string or a raw digest line.
	for _, ev := range events {
		if strings.TrimSpace(ev.Message) == "" {
			t.Error("empty progress message")
		}
		if strings.Contains(ev.Message, "sha256:") {
			t.Errorf("digest leaked into a user-facing message: %q", ev.Message)
		}
	}

	// The wording the parser recognises. If podman changes these, the phases
	// degrade to raw lines and this is where that gets noticed.
	var sawRegistry, sawLayer bool
	for _, ev := range events {
		switch ev.Message {
		case "contacting registry":
			sawRegistry = true
		case "downloading layers":
			sawLayer = true
		}
	}
	if !sawRegistry {
		t.Errorf("no 'contacting registry' phase; podman's first line may have changed")
	}
	if !sawLayer {
		t.Errorf("no 'downloading layers' phase; podman's blob wording may have changed")
	}

	// The layer counter is what the interface shows during a long download.
	var maxLayer int
	for _, ev := range events {
		if ev.Layer > maxLayer {
			maxLayer = ev.Layer
		}
	}
	if maxLayer < 1 {
		t.Errorf("no layers were counted across %d events", len(events))
	}
}

// A pull of an image already present must still say something, or the
// interface sits silent and then jumps to done.
func TestCreateWithCachedImageStillReportsProgress(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newManager(t)
	const id = "it-progress-cached"
	t.Cleanup(func() { cleanup(t, m, id) })

	var messages []string
	inst, err := m.Create(ctx, daemon.CreateOptions{
		Ref: "redis:7", ID: id, Start: true,
		OnProgress: func(ev runtime.PullEvent) { messages = append(messages, ev.Message) },
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	waitForRedis(t, inst.Port, redisReady)

	if len(messages) == 0 {
		t.Fatal("a create reported no progress at all")
	}
	t.Logf("messages: %v", messages)
}
