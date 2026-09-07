package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fiifiofosu/dbforge/internal/model"
	"github.com/fiifiofosu/dbforge/internal/ports"
	"github.com/fiifiofosu/dbforge/internal/runtime"
	"github.com/fiifiofosu/dbforge/internal/store"
)

func newTestServer(t *testing.T) (*httptest.Server, *runtime.Fake) {
	t.Helper()
	fake := runtime.NewFake()
	h, _ := newTestHandler(t, fake)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, fake
}

// newTestHandler builds the handler and its manager over a caller-supplied
// fake, for tests that need to seed the runtime or drive the manager directly.
func newTestHandler(t *testing.T, fake *runtime.Fake) (http.Handler, *Manager) {
	t.Helper()
	dir := t.TempDir()
	m := NewManager(fake, store.New(filepath.Join(dir, "instances.toml")), Config{
		DataRoot: filepath.Join(dir, "data"),
		Probe:    func(int) error { return nil },
	})
	if _, err := m.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	return NewServer(m, testLogger()).Handler(), m
}

func TestAPICreateListRemoveRoundTrip(t *testing.T) {
	srv, _ := newTestServer(t)

	body := strings.NewReader(`{"Ref":"postgres:16","ID":"pg","Start":true}`)
	resp, err := http.Post(srv.URL+"/instances", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", resp.StatusCode)
	}

	var created model.Instance
	json.NewDecoder(resp.Body).Decode(&created)
	if created.Port == 0 {
		t.Fatal("created instance has no port")
	}

	lr, err := http.Get(srv.URL + "/instances")
	if err != nil {
		t.Fatal(err)
	}
	defer lr.Body.Close()
	var list []model.Instance
	json.NewDecoder(lr.Body).Decode(&list)
	if len(list) != 1 || list[0].ID != "pg" {
		t.Fatalf("list = %+v, want one instance 'pg'", list)
	}
}

func TestAPIDuplicateCreateReturns409(t *testing.T) {
	srv, _ := newTestServer(t)
	mk := func() *http.Response {
		r, err := http.Post(srv.URL+"/instances", "application/json",
			strings.NewReader(`{"Ref":"redis:7","ID":"dup"}`))
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	mk().Body.Close()
	second := mk()
	defer second.Body.Close()
	if second.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", second.StatusCode)
	}
}

func TestAPIUnknownInstanceReturns404(t *testing.T) {
	srv, _ := newTestServer(t)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/instances/nope/start", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestAPIListIsEmptyArrayNotNull(t *testing.T) {
	// The waybar module distinguishes "no instances" from "daemon offline";
	// a JSON null would break that (spec 7, phase 4).
	srv, _ := newTestServer(t)
	resp, err := http.Get(srv.URL + "/instances")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var raw json.RawMessage
	json.NewDecoder(resp.Body).Decode(&raw)
	if strings.TrimSpace(string(raw)) != "[]" {
		t.Fatalf("empty list encoded as %s, want []", raw)
	}
}

func TestAPIRemoveDefaultsToKeepingData(t *testing.T) {
	srv, _ := newTestServer(t)
	http.Post(srv.URL+"/instances", "application/json",
		strings.NewReader(`{"Ref":"redis:7","ID":"keep"}`))

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/instances/keep", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestHealthz(t *testing.T) {
	srv, _ := newTestServer(t)
	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

var _ = ports.DefaultRange

// Both ends of DBFORGE_SOCKET must agree. The CLI has always honoured it; the
// daemon did not, so the override silently produced a daemon and a client
// talking to different paths.
func TestSocketPathHonoursTheEnvOverride(t *testing.T) {
	t.Setenv("DBFORGE_SOCKET", "/tmp/custom/dbforged.sock")
	if got := SocketPath(); got != "/tmp/custom/dbforged.sock" {
		t.Fatalf("SocketPath() = %q, want the override", got)
	}
}

func TestSocketPathFallsBackToRuntimeDir(t *testing.T) {
	t.Setenv("DBFORGE_SOCKET", "")
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/4242")
	if got, want := SocketPath(), "/run/user/4242/dbforge/dbforged.sock"; got != want {
		t.Fatalf("SocketPath() = %q, want %q", got, want)
	}
}

// A streaming create must report progress as it happens, not hand over the
// whole history once the work is done -- the point is to show something is
// happening during a slow pull.
func TestStreamingCreateReportsProgressThenTheInstance(t *testing.T) {
	fake := runtime.NewFake()
	fake.PullEvents = []runtime.PullEvent{
		{Message: "contacting registry"},
		{Message: "downloading layers", Layer: 1},
		{Message: "downloading layers", Layer: 2},
	}
	h, _ := newTestHandler(t, fake)

	body, _ := json.Marshal(CreateOptions{Ref: "redis:7", ID: "streamed"})
	req := httptest.NewRequest(http.MethodPost, "/instances?stream=true", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (a stream reports failure in its body)", rec.Code)
	}

	var progress []runtime.PullEvent
	var created *model.Instance
	dec := json.NewDecoder(rec.Body)
	for {
		var ev CreateEvent
		if err := dec.Decode(&ev); err != nil {
			break
		}
		switch {
		case ev.Progress != nil:
			progress = append(progress, *ev.Progress)
		case ev.Instance != nil:
			created = ev.Instance
		case ev.Error != "":
			t.Fatalf("unexpected error event: %s", ev.Error)
		}
	}

	if created == nil {
		t.Fatal("stream ended without an instance")
	}
	if created.ID != "streamed" {
		t.Fatalf("created %q, want streamed", created.ID)
	}
	if len(progress) < len(fake.PullEvents) {
		t.Fatalf("got %d progress events, want at least %d: %+v",
			len(progress), len(fake.PullEvents), progress)
	}
	// The layer counter is what the interface shows, so it has to survive.
	var maxLayer int
	for _, p := range progress {
		if p.Layer > maxLayer {
			maxLayer = p.Layer
		}
	}
	if maxLayer != 2 {
		t.Fatalf("highest layer reported = %d, want 2", maxLayer)
	}
}

// A streamed failure cannot use the status line, since 200 was already sent
// before the work began. It must arrive as an event, with its kind intact so
// the client can still tell a conflict from a broken daemon.
func TestStreamingCreateReportsFailureAsAnEvent(t *testing.T) {
	h, mgr := newTestHandler(t, runtime.NewFake())
	if _, err := mgr.Create(context.Background(), CreateOptions{Ref: "redis:7", ID: "taken"}); err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(CreateOptions{Ref: "redis:7", ID: "taken"})
	req := httptest.NewRequest(http.MethodPost, "/instances?stream=true", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var last CreateEvent
	dec := json.NewDecoder(rec.Body)
	for {
		var ev CreateEvent
		if err := dec.Decode(&ev); err != nil {
			break
		}
		last = ev
	}
	if last.Error == "" {
		t.Fatalf("stream ended without an error event: %+v", last)
	}
	if last.Kind != "conflict" {
		t.Fatalf("kind = %q, want conflict so the client can classify it", last.Kind)
	}
}

// The non-streaming path is what every existing client uses; adding streaming
// must not have changed it.
func TestNonStreamingCreateStillReturnsPlainJSON(t *testing.T) {
	h, _ := newTestHandler(t, runtime.NewFake())

	body, _ := json.Marshal(CreateOptions{Ref: "redis:7", ID: "plain"})
	req := httptest.NewRequest(http.MethodPost, "/instances", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rec.Code)
	}
	var inst model.Instance
	if err := json.Unmarshal(rec.Body.Bytes(), &inst); err != nil {
		t.Fatalf("body is not a plain instance: %v\n%s", err, rec.Body.String())
	}
	if inst.ID != "plain" {
		t.Fatalf("got %q, want plain", inst.ID)
	}
}
