package update

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
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

func TestLatestReadsTheRelease(t *testing.T) {
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
	if rel.URL != "https://example.test/r/v0.8.0" {
		t.Errorf("url %q", rel.URL)
	}
}

// The release above advertises downloadable binaries and a checksum file. This
// package must come away with none of them: a Release carries a version number
// and a link for the user to read, and nothing this program could fetch and
// run. If asset URLs ever creep back into Release, the self-updater is being
// rebuilt and this is the test that should stop it.
func TestReleaseCarriesNothingDownloadable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"tag_name":"v0.8.0","html_url":"https://example.test/r/v0.8.0",
			"assets":[{"name":"dbforge","browser_download_url":"https://evil.test/dbforge"}]}`)
	}))
	defer srv.Close()
	apiBase = srv.URL

	rel, err := Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < reflect.TypeOf(rel).NumField(); i++ {
		f := reflect.TypeOf(rel).Field(i)
		if f.Type.Kind() != reflect.String {
			t.Errorf("Release.%s is a %s; a release should be plain strings, "+
				"not somewhere to keep assets", f.Name, f.Type.Kind())
			continue
		}
		if v := reflect.ValueOf(rel).Field(i).String(); strings.Contains(v, "evil.test") {
			t.Errorf("Release.%s = %q: an asset download URL reached the caller", f.Name, v)
		}
	}
}

// The advice has to name a command. "Update it the way you installed it" is
// true and useless, and a user who is told only that will go looking for the
// updater that used to be here.
func TestInstallHintNamesAnActualCommand(t *testing.T) {
	hint := InstallHint()
	if !strings.Contains(hint, "paru") && !strings.Contains(hint, "pacman") &&
		!strings.Contains(hint, "make install") {
		t.Errorf("install hint names no command to run:\n%s", hint)
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
