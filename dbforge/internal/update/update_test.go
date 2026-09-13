package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewerComparesVersionsNotStrings(t *testing.T) {
	cases := []struct {
		current, release string
		want             bool
	}{
		{"0.6.0", "0.7.0", true},
		{"0.7.0", "0.7.0", false},
		{"0.7.0", "0.6.9", false},
		// String comparison gets these wrong; version comparison does not.
		{"0.9.0", "0.10.0", true},
		{"0.10.0", "0.9.0", false},
		{"1.0.0", "0.99.9", false},
		// Tags may or may not carry the v.
		{"v0.6.0", "0.7.0", true},
		{"0.6.0", "v0.7.0", true},
		// A local build of the version being worked towards predates its
		// release, so the release is an update.
		{"0.7.0-dev+abc123", "0.7.0", true},
		{"dev", "0.1.0", true},
		// An AUR-style pkgver counts commits *past* the tag, so 0.7.0.r4 is
		// ahead of the 0.7.0 release, not behind it.
		{"0.7.0.r4.gabcdef", "0.7.0", false},
		// ...but a dev build ahead of the newest release is not behind it.
		{"0.8.0-dev+abc123", "0.7.0", false},
	}
	for _, c := range cases {
		if got := Newer(c.current, c.release); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.current, c.release, got, c.want)
		}
	}
}

func TestParseChecksumsReadsSha256sumOutput(t *testing.T) {
	sums := parseChecksums(strings.Join([]string{
		"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855  dbforge",
		"da39a3ee5e6b4b0d3255bfef95601890afd80709 *dbforge-tui",
		"5e884898da28047151d0e56f8dc6292773603d0d6aabbdd62a11ef721d1542d8  dist/dbforge-0.7.0.tar.gz",
		"",
		"not a checksum line",
	}, "\n"))
	if got := sums["dbforge"]; got != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Errorf("dbforge = %q", got)
	}
	// A short digest is not SHA-256 and must not be accepted as one.
	if _, ok := sums["dbforge-tui"]; ok {
		t.Error("a 40-character digest was accepted as SHA-256")
	}
	// A path in the sums file still names the asset it covers.
	if _, ok := sums["dbforge-0.7.0.tar.gz"]; !ok {
		t.Errorf("path-qualified entry not matched: %v", sums)
	}
}

func TestCheckWritableRefusesAPackagedInstall(t *testing.T) {
	var notOwned *ErrNotOwned
	if err := CheckWritable("/usr/bin"); !errors.As(err, &notOwned) {
		t.Fatalf("/usr/bin: got %v, want ErrNotOwned", err)
	}
	// The message has to say what to do instead, since the update cannot.
	if !strings.Contains(notOwned.Error(), "pacman") {
		t.Errorf("refusal does not name the package manager: %v", notOwned)
	}
	if err := CheckWritable(t.TempDir()); err != nil {
		t.Errorf("a writable directory was refused: %v", err)
	}
}

// fakeRelease serves a release's assets, and returns a Release pointing at it.
func fakeRelease(t *testing.T, tag string, files map[string]string, corrupt string) Release {
	t.Helper()
	mux := http.NewServeMux()
	var sums strings.Builder
	for name, content := range files {
		sum := sha256.Sum256([]byte(content))
		fmt.Fprintf(&sums, "%s  %s\n", hex.EncodeToString(sum[:]), name)
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	rel := Release{Tag: tag, Version: strings.TrimPrefix(tag, "v"), assets: map[string]string{}}
	for name, content := range files {
		body := content
		if name == corrupt {
			body = content + " tampered"
		}
		mux.HandleFunc("/"+name, func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, body)
		})
		rel.assets[name] = srv.URL + "/" + name
	}
	checksums := sums.String()
	mux.HandleFunc("/"+checksumAsset, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, checksums)
	})
	rel.assets[checksumAsset] = srv.URL + "/" + checksumAsset
	return rel
}

func TestApplyReplacesBinariesOnlyAfterVerifyingThem(t *testing.T) {
	dir := t.TempDir()
	for _, name := range binaries {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("old "+name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	rel := fakeRelease(t, "v9.9.9", map[string]string{
		"dbforge":     "new dbforge",
		"dbforge-tui": "new dbforge-tui",
	}, "")

	replaced, err := Apply(context.Background(), rel, dir, nil)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(replaced) != len(binaries) {
		t.Fatalf("replaced %v, want %v", replaced, binaries)
	}
	for _, name := range binaries {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "new "+name {
			t.Errorf("%s = %q, want the downloaded content", name, got)
		}
		st, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0o755 {
			t.Errorf("%s mode = %v, want 0755", name, st.Mode().Perm())
		}
	}
	// Staging must not survive a successful update.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".dbforge-update-") {
			t.Errorf("staging left behind: %s", e.Name())
		}
	}
}

func TestApplyInstallsNothingWhenAChecksumDoesNotMatch(t *testing.T) {
	dir := t.TempDir()
	for _, name := range binaries {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("old "+name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// The second binary is served tampered with. The first one verifies fine,
	// so this is exactly the case where a naive updater half-installs.
	rel := fakeRelease(t, "v9.9.9", map[string]string{
		"dbforge":     "new dbforge",
		"dbforge-tui": "new dbforge-tui",
	}, "dbforge-tui")

	_, err := Apply(context.Background(), rel, dir, nil)
	if err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("got %v, want a checksum failure", err)
	}
	for _, name := range binaries {
		got, _ := os.ReadFile(filepath.Join(dir, name))
		if string(got) != "old "+name {
			t.Errorf("%s was replaced despite the failure: %q", name, got)
		}
	}
}

func TestApplyRefusesAReleaseWithNoChecksums(t *testing.T) {
	dir := t.TempDir()
	rel := Release{Tag: "v9.9.9", assets: map[string]string{
		"dbforge":     "http://example.invalid/dbforge",
		"dbforge-tui": "http://example.invalid/dbforge-tui",
	}}
	_, err := Apply(context.Background(), rel, dir, nil)
	if err == nil || !strings.Contains(err.Error(), checksumAsset) {
		t.Fatalf("got %v, want a refusal naming %s", err, checksumAsset)
	}
}

func TestLatestReportsAMissingReleaseDistinctly(t *testing.T) {
	// A private repository answers 404 to an unauthenticated caller, which has
	// the same shape as a repository with no releases at all. Both must reach
	// the caller as ErrNoRelease so it can explain them together.
	for _, status := range []int{http.StatusNotFound} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
		}))
		apiBase = srv.URL
		_, err := Latest(context.Background())
		srv.Close()
		if !errors.Is(err, ErrNoRelease) {
			t.Errorf("status %d: got %v, want ErrNoRelease", status, err)
		}
	}
}

func TestLatestReadsTheReleaseAndItsAssets(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"tag_name":"v0.8.0","html_url":"https://example.test/r/v0.8.0",
			"assets":[{"name":"dbforge","browser_download_url":"https://example.test/dbforge"},
			          {"name":"SHA256SUMS","browser_download_url":"https://example.test/SHA256SUMS"}]}`)
	}))
	defer srv.Close()
	apiBase = srv.URL

	rel, err := Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rel.Version != "0.8.0" || rel.Tag != "v0.8.0" {
		t.Errorf("version %q tag %q", rel.Version, rel.Tag)
	}
	if rel.assets["dbforge"] != "https://example.test/dbforge" {
		t.Errorf("assets = %v", rel.assets)
	}
}

func TestLatestIgnoresADraftRelease(t *testing.T) {
	// A draft is not published; installing one would ship unreleased code.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"tag_name":"v0.9.0","draft":true}`)
	}))
	defer srv.Close()
	apiBase = srv.URL

	if _, err := Latest(context.Background()); !errors.Is(err, ErrNoRelease) {
		t.Fatalf("got %v, want ErrNoRelease", err)
	}
}

func TestMain(m *testing.M) {
	// Every test that reaches the network points apiBase at its own server;
	// restoring it here keeps a failure from leaking into the next package.
	defer func() { apiBase = "https://api.github.com" }()
	os.Exit(m.Run())
}
