// Package registry lists the versions available for an engine.
//
// The TUI's create form needs a version list, and that list comes from the
// network. Since a laptop is often offline, this is built around a cache with
// an explicit "this is stale" signal rather than around the network call
// succeeding (spec 7, phase 3).
package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Source says where a version list came from, so callers can tell the user.
type Source string

const (
	// SourceLive means the list was just fetched.
	SourceLive Source = "live"
	// SourceCache means the network was unavailable and this is what we had.
	SourceCache Source = "cache"
	// SourceBuiltin means there was no cache either, so these are the versions
	// compiled into DBForge. Least trustworthy, but never empty.
	SourceBuiltin Source = "builtin"
)

// Result is a version list plus its provenance.
type Result struct {
	Versions []string
	Source   Source
	// FetchedAt is when the list was retrieved, for cache staleness display.
	FetchedAt time.Time
	// Err is the network error when falling back, so the UI can say why.
	Err error
}

// Stale reports whether a cached result is old enough to mention.
func (r Result) Stale() bool {
	return r.Source != SourceLive && !r.FetchedAt.IsZero() && time.Since(r.FetchedAt) > 7*24*time.Hour
}

// builtin is the floor: versions we know exist, so the form is never empty
// even on a fresh install with no network.
var builtin = map[string][]string{
	"postgres": {"17", "16", "15", "14", "13"},
	"mysql":    {"9", "8.4", "8.0"},
	"mariadb":  {"11", "10.11", "10.6"},
	"redis":    {"7", "6.2"},
}

// Client fetches and caches version lists.
type Client struct {
	HTTP     *http.Client
	CacheDir string
}

// New returns a Client caching under ~/.cache/dbforge.
func New() *Client {
	dir := os.Getenv("XDG_CACHE_HOME")
	if dir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			dir = filepath.Join(home, ".cache")
		}
	}
	return &Client{
		HTTP:     &http.Client{Timeout: 8 * time.Second},
		CacheDir: filepath.Join(dir, "dbforge"),
	}
}

type cacheFile struct {
	Versions  []string  `json:"versions"`
	FetchedAt time.Time `json:"fetched_at"`
}

func (c *Client) cachePath(engine string) string {
	return filepath.Join(c.CacheDir, "versions-"+engine+".json")
}

// Versions returns the versions available for an engine, preferring a live
// fetch and falling back to cache and then to the builtin list. It never
// returns an empty list without an error.
func (c *Client) Versions(ctx context.Context, engine string) Result {
	live, err := c.fetch(ctx, engine)
	if err == nil && len(live) > 0 {
		now := time.Now()
		c.writeCache(engine, cacheFile{Versions: live, FetchedAt: now})
		return Result{Versions: live, Source: SourceLive, FetchedAt: now}
	}

	if cached, cerr := c.readCache(engine); cerr == nil && len(cached.Versions) > 0 {
		return Result{
			Versions:  cached.Versions,
			Source:    SourceCache,
			FetchedAt: cached.FetchedAt,
			Err:       err,
		}
	}

	if b, ok := builtin[engine]; ok {
		return Result{Versions: append([]string(nil), b...), Source: SourceBuiltin, Err: err}
	}
	return Result{Source: SourceBuiltin, Err: fmt.Errorf("no versions known for engine %q: %w", engine, err)}
}

// CachedOnly returns the cache without touching the network. The TUI uses this
// to render instantly, then refreshes in the background.
func (c *Client) CachedOnly(engine string) Result {
	if cached, err := c.readCache(engine); err == nil && len(cached.Versions) > 0 {
		return Result{Versions: cached.Versions, Source: SourceCache, FetchedAt: cached.FetchedAt}
	}
	if b, ok := builtin[engine]; ok {
		return Result{Versions: append([]string(nil), b...), Source: SourceBuiltin}
	}
	return Result{Source: SourceBuiltin}
}

type hubResponse struct {
	Next    string `json:"next"`
	Results []struct {
		Name string `json:"name"`
	} `json:"results"`
}

// fetch asks Docker Hub for an image's tags.
func (c *Client) fetch(ctx context.Context, engine string) ([]string, error) {
	url := fmt.Sprintf(
		"https://hub.docker.com/v2/repositories/library/%s/tags?page_size=100&ordering=last_updated",
		engine)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("docker hub returned %s", resp.Status)
	}

	var hub hubResponse
	if err := json.NewDecoder(resp.Body).Decode(&hub); err != nil {
		return nil, err
	}

	tags := make([]string, 0, len(hub.Results))
	for _, r := range hub.Results {
		tags = append(tags, r.Name)
	}
	return FilterVersionTags(tags), nil
}

// versionTag matches plain version tags: 16, 8.4, 10.11.2. Deliberately
// excludes -alpine, -bookworm, rc, beta and "latest": for choosing a database
// version those are noise, and a user who wants one can type it by hand.
var versionTag = regexp.MustCompile(`^\d+(\.\d+){0,2}$`)

// FilterVersionTags keeps plain version tags and sorts them newest first.
func FilterVersionTags(tags []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		if !versionTag.MatchString(t) || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return compareVersions(out[i], out[j]) > 0 })
	return out
}

// compareVersions orders dotted numeric versions numerically, so 10 sorts
// above 9 rather than below it as a string compare would.
func compareVersions(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		var ai, bi int
		if i < len(as) {
			ai, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			bi, _ = strconv.Atoi(bs[i])
		}
		if ai != bi {
			if ai > bi {
				return 1
			}
			return -1
		}
	}
	return 0
}

func (c *Client) readCache(engine string) (cacheFile, error) {
	var out cacheFile
	raw, err := os.ReadFile(c.cachePath(engine))
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, err
	}
	if len(out.Versions) == 0 {
		return out, errors.New("cache is empty")
	}
	return out, nil
}

func (c *Client) writeCache(engine string, f cacheFile) {
	if err := os.MkdirAll(c.CacheDir, 0o700); err != nil {
		return
	}
	raw, err := json.Marshal(f)
	if err != nil {
		return
	}
	// Best effort: a failed cache write must never fail the fetch.
	tmp := c.cachePath(engine) + ".tmp"
	if os.WriteFile(tmp, raw, 0o600) == nil {
		os.Rename(tmp, c.cachePath(engine))
	}
}
