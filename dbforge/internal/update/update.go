// Package update tells the user when a newer DBForge has been published.
//
// It used to install one as well: fetch the latest GitHub release, download
// its binaries and its SHA256SUMS, and replace the installed executables. That
// is gone, and deliberately.
//
// The problem was not the checksum. The checksum was real, and it caught a
// download that did not match what the release listed. But the binary and the
// list of hashes it was checked against came from the same place, chosen the
// same way -- whatever "releases/latest" pointed at that minute. Anyone able to
// publish or alter a release published both halves, so the check could only
// prove the two agreed with each other, never that either was the artifact
// anybody had reviewed. A program that replaces its own executable with code
// selected by a mutable pointer is a supply-chain hole, whatever it hashes on
// the way in.
//
// Binding it properly would mean an expected digest or signature fixed
// independently of the release being downloaded -- which, for a version that
// does not exist yet when the running binary is built, there is no honest way
// to do without a signing key and verification this program does not have. So
// the download is not fixed, it is removed. What is left reads a version
// number and prints it. Nothing here writes to disk, and nothing here can put
// new code on the machine: installing is the package manager's job, and it
// already verifies packages against keys the user has decided to trust.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// apiBase is GitHub's API root. A variable only so tests can point it at a
// local server; nothing reads it from the environment.
var apiBase = "https://api.github.com"

// Repo is where releases are published.
const Repo = "fiifiofosu/dbforge"

// ErrNoRelease means the repository has no published release this build can
// see -- including the case where it is private and the API denies it.
var ErrNoRelease = errors.New("no published release found")

// Release is one published version. It carries what is needed to tell the user
// a newer version exists and where to read about it -- a number and a link,
// and deliberately no download URLs.
type Release struct {
	Tag     string
	Version string
	URL     string
}

// Latest asks GitHub for the newest release.
//
// This is a read. It returns a version string and a URL for the user to open;
// nothing it returns is fetched, executed or written anywhere.
func Latest(ctx context.Context) (Release, error) {
	url := fmt.Sprintf("%s/repos/%s/releases/latest", apiBase, Repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	// Unauthenticated on purpose: this reads a public release list and
	// nothing else, so it has no business handling anyone's token.
	resp, err := httpClient().Do(req)
	if err != nil {
		return Release{}, fmt.Errorf("asking GitHub for the latest release: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return Release{}, ErrNoRelease
	case http.StatusForbidden, http.StatusTooManyRequests:
		return Release{}, errors.New("GitHub is rate-limiting this machine; try again later")
	default:
		return Release{}, fmt.Errorf("GitHub returned %s", resp.Status)
	}

	var body struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
		Draft   bool   `json:"draft"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&body); err != nil {
		return Release{}, fmt.Errorf("reading GitHub's answer: %w", err)
	}
	if body.TagName == "" || body.Draft {
		return Release{}, ErrNoRelease
	}

	return Release{
		Tag:     body.TagName,
		Version: strings.TrimPrefix(body.TagName, "v"),
		URL:     body.HTMLURL,
	}, nil
}

// Newer reports whether release is a later version than current.
//
// A development build ("0.7.0-dev+abc123") is never treated as newer than the
// release it is working towards, so a developer running their own build is not
// told they are up to date when they are actually ahead -- they are shown the
// release, and can ignore it.
func Newer(current, release string) bool {
	cur, curDev := parseVersion(current)
	rel, _ := parseVersion(release)
	switch cmp := compare(rel, cur); {
	case cmp > 0:
		return true
	case cmp < 0:
		return false
	default:
		// Same numbers: a dev build of that version predates the release.
		return curDev
	}
}

// parseVersion splits "v0.7.0-dev+abc" into [0 7 0] and whether it is a
// development build.
func parseVersion(v string) ([3]int, bool) {
	v = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(v), "v"))
	dev := strings.Contains(v, "dev") || strings.Contains(v, "dirty")
	// Cut anything after the numbers: -dev, +hash, .r12.gabc from a git build.
	numeric := strings.FieldsFunc(v, func(r rune) bool {
		return r != '.' && (r < '0' || r > '9')
	})
	var out [3]int
	if len(numeric) == 0 {
		return out, true
	}
	for i, part := range strings.Split(numeric[0], ".") {
		if i > 2 {
			break
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			break
		}
		out[i] = n
	}
	return out, dev
}

func compare(a, b [3]int) int {
	for i := range a {
		switch {
		case a[i] > b[i]:
			return 1
		case a[i] < b[i]:
			return -1
		}
	}
	return 0
}

// InstallDir is the directory holding the running binaries. It is used only to
// guess how DBForge was installed, so the advice names the right command.
func InstallDir() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	self, err = filepath.EvalSymlinks(self)
	if err != nil {
		return "", err
	}
	return filepath.Dir(self), nil
}

// InstallHint is how to install a new version on this machine.
//
// "Update it the way you installed it" is true but useless on its own, so this
// guesses from where the binaries live and names an actual command. A wrong
// guess costs the user a glance; there is nothing here to get wrong more
// expensively than that, because nothing here runs the command.
func InstallHint() string {
	dir, err := InstallDir()
	if err != nil {
		dir = ""
	}
	packaged := false
	for _, prefix := range []string{"/usr/", "/opt/", "/nix/"} {
		if strings.HasPrefix(dir+"/", prefix) {
			packaged = true
		}
	}
	if packaged {
		return "DBForge was installed by your package manager (" + dir + "). Update it there:\n" +
			"  paru -S dbforge-git      (or your AUR helper)\n" +
			"  sudo pacman -Syu"
	}
	return "DBForge does not update itself. Install the new version the way you\n" +
		"installed this one, e.g.\n" +
		"  paru -S dbforge-git      (or your AUR helper)\n" +
		"  git pull && make install  (from a source checkout)"
}

func httpClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second}
}
