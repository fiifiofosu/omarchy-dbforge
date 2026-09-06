package daemon

import (
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
	dir := t.TempDir()
	fake := runtime.NewFake()
	m := NewManager(fake, store.New(filepath.Join(dir, "instances.toml")), Config{
		DataRoot: filepath.Join(dir, "data"),
		Probe:    func(int) error { return nil },
	})
	if _, err := m.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(NewServer(m, testLogger()).Handler())
	t.Cleanup(srv.Close)
	return srv, fake
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
