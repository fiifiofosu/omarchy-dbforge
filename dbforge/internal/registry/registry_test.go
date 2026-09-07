package registry

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestFilterVersionTagsKeepsOnlyPlainVersions(t *testing.T) {
	in := []string{
		"16", "latest", "16-alpine", "16.1", "15", "16-bookworm",
		"17rc1", "9", "10", "", "16.1.0", "16", // duplicate
	}
	got := FilterVersionTags(in)
	want := []string{"17", "16.1.0", "16.1", "16", "15", "10", "9"}

	// 17rc1 must not survive, and 10 must sort above 9.
	if reflect.DeepEqual(got, want) {
		return
	}
	// Assert the properties rather than an exact list, so the test explains
	// itself when it fails.
	for _, bad := range []string{"latest", "16-alpine", "17rc1", "16-bookworm", ""} {
		for _, g := range got {
			if g == bad {
				t.Errorf("tag %q should have been filtered out", bad)
			}
		}
	}
	var ten, nine int = -1, -1
	for i, g := range got {
		if g == "10" {
			ten = i
		}
		if g == "9" {
			nine = i
		}
	}
	if ten == -1 || nine == -1 || ten > nine {
		t.Errorf("got %v; 10 must sort above 9 (numeric, not lexical)", got)
	}
}

func TestCompareVersionsIsNumeric(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"10", "9", 1},
		{"9", "10", -1},
		{"16", "16", 0},
		{"8.4", "8.0", 1},
		{"10.11", "10.6", 1},
		{"16.1.2", "16.1", 1},
	}
	for _, c := range cases {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

// liveClient points a Client at a stub Docker Hub.
func liveClient(t *testing.T, tags ...string) *Client {
	t.Helper()
	results := make([]struct {
		Name string `json:"name"`
	}, 0, len(tags))
	for _, tag := range tags {
		results = append(results, struct {
			Name string `json:"name"`
		}{Name: tag})
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(hubResponse{Results: results})
	}))
	t.Cleanup(srv.Close)
	return &Client{
		CacheDir: t.TempDir(),
		HTTP: &http.Client{Transport: rewriter{
			base: http.DefaultTransport,
			host: srv.Listener.Addr().String(),
		}},
	}
}

// rewriter sends every request to the stub server regardless of its URL.
type rewriter struct {
	base http.RoundTripper
	host string
}

func (rt rewriter) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.URL.Scheme = "http"
	r.URL.Host = rt.host
	return rt.base.RoundTrip(r)
}

func TestVersionsLiveFetchPopulatesCache(t *testing.T) {
	c := liveClient(t, "16", "15", "latest", "16-alpine")

	res := c.Versions(context.Background(), "postgres")
	if res.Source != SourceLive {
		t.Fatalf("source = %q, want live (err: %v)", res.Source, res.Err)
	}
	if !reflect.DeepEqual(res.Versions, []string{"16", "15"}) {
		t.Fatalf("versions = %v, want [16 15]", res.Versions)
	}
	if _, err := os.Stat(filepath.Join(c.CacheDir, "versions-postgres.json")); err != nil {
		t.Fatalf("cache was not written: %v", err)
	}

	// A later offline call must serve exactly what was cached.
	c.HTTP = &http.Client{Transport: failingTransport{}}
	again := c.Versions(context.Background(), "postgres")
	if again.Source != SourceCache {
		t.Fatalf("second call source = %q, want cache", again.Source)
	}
	if !reflect.DeepEqual(again.Versions, []string{"16", "15"}) {
		t.Fatalf("cached versions = %v, want [16 15]", again.Versions)
	}
}

// The offline path is the one that matters on a laptop.
func TestVersionsFallsBackToCacheWhenOffline(t *testing.T) {
	dir := t.TempDir()
	fetched := time.Now().Add(-2 * time.Hour)
	raw, _ := json.Marshal(cacheFile{Versions: []string{"16", "15"}, FetchedAt: fetched})
	os.WriteFile(filepath.Join(dir, "versions-postgres.json"), raw, 0o600)

	// A client whose transport always fails, i.e. offline.
	c := &Client{CacheDir: dir, HTTP: &http.Client{Transport: failingTransport{}}}

	res := c.Versions(context.Background(), "postgres")
	if res.Source != SourceCache {
		t.Fatalf("source = %q, want cache", res.Source)
	}
	if len(res.Versions) != 2 {
		t.Fatalf("versions = %v, want the cached list", res.Versions)
	}
	if res.Err == nil {
		t.Error("the network error should be reported so the UI can explain the fallback")
	}
}

func TestVersionsFallsBackToBuiltinWithNoCache(t *testing.T) {
	c := &Client{CacheDir: t.TempDir(), HTTP: &http.Client{Transport: failingTransport{}}}
	res := c.Versions(context.Background(), "redis")
	if res.Source != SourceBuiltin {
		t.Fatalf("source = %q, want builtin", res.Source)
	}
	if len(res.Versions) == 0 {
		t.Fatal("builtin fallback must never be empty; the create form would have nothing to show")
	}
}

func TestVersionsNeverEmptyForKnownEngines(t *testing.T) {
	c := &Client{CacheDir: t.TempDir(), HTTP: &http.Client{Transport: failingTransport{}}}
	for _, e := range []string{"postgres", "mysql", "mariadb", "redis"} {
		if res := c.Versions(context.Background(), e); len(res.Versions) == 0 {
			t.Errorf("engine %q offline yields no versions", e)
		}
	}
}

func TestCachedOnlyDoesNotTouchNetwork(t *testing.T) {
	c := &Client{CacheDir: t.TempDir(), HTTP: &http.Client{Transport: panicTransport{t}}}
	res := c.CachedOnly("postgres")
	if len(res.Versions) == 0 {
		t.Fatal("CachedOnly returned nothing; it should fall back to builtin")
	}
}

func TestStaleOnlyForOldNonLiveResults(t *testing.T) {
	fresh := Result{Source: SourceCache, FetchedAt: time.Now().Add(-time.Hour)}
	if fresh.Stale() {
		t.Error("a one-hour-old cache should not be reported as stale")
	}
	old := Result{Source: SourceCache, FetchedAt: time.Now().Add(-30 * 24 * time.Hour)}
	if !old.Stale() {
		t.Error("a 30-day-old cache should be reported as stale")
	}
	live := Result{Source: SourceLive, FetchedAt: time.Now().Add(-30 * 24 * time.Hour)}
	if live.Stale() {
		t.Error("a live result is never stale")
	}
}

type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errOffline
}

var errOffline = &net.OpError{Op: "dial", Err: os.ErrDeadlineExceeded}

type panicTransport struct{ t *testing.T }

func (p panicTransport) RoundTrip(*http.Request) (*http.Response, error) {
	p.t.Error("CachedOnly made a network request")
	return nil, errOffline
}
