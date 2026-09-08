// Package update replaces the installed DBForge binaries with the newest
// published release.
//
// The design constraint is that this runs unattended from a keystroke in the
// TUI, so every step that could go wrong quietly is made to fail loudly
// instead: the download is checksummed against the release's own SHA256SUMS
// before anything is replaced, the replacement is a rename within the install
// directory (atomic, and legal even while the old binary is running), and an
// install this process does not own -- a packaged one under /usr -- is refused
// with the command that would do it properly rather than half-updated.
package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// apiBase is GitHub's API root. A variable only so tests can point it at a
// local server; nothing reads it from the environment, because "update from
// wherever this variable says" is a way to install arbitrary binaries.
var apiBase = "https://api.github.com"

// Repo is where releases come from. A constant rather than configuration:
// pointing the updater at an arbitrary host is a way to install arbitrary
// binaries, and nothing about DBForge needs it.
const Repo = "fiifiofosu/dbforge"

// binaries are the files a release ships that an install actually contains.
// The dbforged and dbctl names are symlinks to dbforge, so they update with it.
var binaries = []string{"dbforge", "dbforge-tui"}

// checksumAsset is the release asset listing the SHA-256 of every other one.
const checksumAsset = "SHA256SUMS"

// maxAssetBytes caps a download. The binaries are ~10 MB; anything an order of
// magnitude past that is not a release, and streaming it to disk unbounded is
// how a bad day becomes a full filesystem.
const maxAssetBytes = 256 << 20

// ErrNoRelease means the repository has no published release this build can
// see -- including the case where it is private and the API denies it.
var ErrNoRelease = errors.New("no published release found")

// ErrNotOwned means the installed binaries belong to a package manager.
type ErrNotOwned struct {
	Dir string
}

func (e *ErrNotOwned) Error() string {
	return fmt.Sprintf("DBForge is installed in %s, which belongs to your package manager.\n"+
		"Update it the way it was installed, e.g.\n"+
		"  paru -S dbforge-git      (or your AUR helper)\n"+
		"  sudo pacman -U dbforge-git-*.pkg.tar.zst   (from the release page)", e.Dir)
}

// Release is one published version.
type Release struct {
	Tag     string
	Version string
	URL     string
	// assets maps an asset's file name to its download URL.
	assets map[string]string
}

// Progress reports a step to the caller. Never nil inside this package.
type Progress func(string)

func (p Progress) say(format string, args ...any) {
	if p != nil {
		p(fmt.Sprintf(format, args...))
	}
}

// Latest asks GitHub for the newest release.
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
		Assets  []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&body); err != nil {
		return Release{}, fmt.Errorf("reading GitHub's answer: %w", err)
	}
	if body.TagName == "" || body.Draft {
		return Release{}, ErrNoRelease
	}

	rel := Release{
		Tag:     body.TagName,
		Version: strings.TrimPrefix(body.TagName, "v"),
		URL:     body.HTMLURL,
		assets:  map[string]string{},
	}
	for _, a := range body.Assets {
		rel.assets[a.Name] = a.URL
	}
	return rel, nil
}

// Newer reports whether release is a later version than current.
//
// A development build ("0.7.0-dev+abc123") is never treated as newer than the
// release it is working towards, so a developer running their own build is not
// told they are up to date when they are actually ahead -- they are offered
// the release, and can decline.
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

// InstallDir is the directory holding the running binaries.
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

// CheckWritable reports whether this process may replace the installed
// binaries. A packaged install is refused rather than fought with: replacing
// pacman's files leaves the package database lying about what is on disk.
func CheckWritable(dir string) error {
	for _, prefix := range []string{"/usr/", "/opt/", "/nix/"} {
		if strings.HasPrefix(dir+"/", prefix) {
			return &ErrNotOwned{Dir: dir}
		}
	}
	// The real test, whatever the path looks like: can we write here?
	probe, err := os.CreateTemp(dir, ".dbforge-update-probe-*")
	if err != nil {
		return &ErrNotOwned{Dir: dir}
	}
	probe.Close()
	os.Remove(probe.Name())
	return nil
}

// Apply downloads the release and replaces the binaries in dir.
//
// It returns the names it replaced. Nothing is moved into place until every
// file has been downloaded and checksummed, so a failure partway leaves the
// installation exactly as it was.
func Apply(ctx context.Context, rel Release, dir string, onProgress Progress) ([]string, error) {
	if err := CheckWritable(dir); err != nil {
		return nil, err
	}

	sums, err := fetchChecksums(ctx, rel)
	if err != nil {
		return nil, err
	}

	// Staged in the install directory so the final move is a rename within one
	// filesystem: /tmp is very often a different one, and a cross-device
	// rename is a copy, which is not atomic.
	staging, err := os.MkdirTemp(dir, ".dbforge-update-*")
	if err != nil {
		return nil, fmt.Errorf("preparing a staging directory in %s: %w", dir, err)
	}
	defer os.RemoveAll(staging)

	staged := map[string]string{}
	for _, name := range binaries {
		if _, ok := rel.assets[name]; !ok {
			// A release that ships no TUI is a release this updater cannot
			// half-apply. Say which file is missing.
			return nil, fmt.Errorf("release %s has no %q asset", rel.Tag, name)
		}
		want, ok := sums[name]
		if !ok {
			return nil, fmt.Errorf("release %s does not checksum %q; refusing to install it",
				rel.Tag, name)
		}
		onProgress.say("downloading %s", name)
		path := filepath.Join(staging, name)
		got, err := download(ctx, rel.assets[name], path)
		if err != nil {
			return nil, err
		}
		if got != want {
			return nil, fmt.Errorf("%s does not match the release checksum "+
				"(got %s, expected %s); nothing was installed", name, got[:12], want[:12])
		}
		onProgress.say("verified %s", name)
		staged[name] = path
	}

	var replaced []string
	for _, name := range binaries {
		target := filepath.Join(dir, name)
		if err := os.Chmod(staged[name], 0o755); err != nil {
			return replaced, err
		}
		// Rename over a running binary is fine on Linux: the old inode stays
		// alive for the processes that have it open, which is why the TUI can
		// replace itself and why the daemon must still be restarted after.
		if err := os.Rename(staged[name], target); err != nil {
			return replaced, fmt.Errorf("installing %s: %w", target, err)
		}
		replaced = append(replaced, name)
		onProgress.say("installed %s", name)
	}
	return replaced, nil
}

// RestartDaemon restarts dbforged so it runs the new binary. A daemon that
// keeps running is still executing the code that was just replaced.
func RestartDaemon(ctx context.Context) error {
	if _, err := exec.LookPath("systemctl"); err == nil {
		out, err := exec.CommandContext(ctx, "systemctl", "--user", "restart", "dbforged").
			CombinedOutput()
		if err == nil {
			return nil
		}
		if !strings.Contains(string(out), "not found") &&
			!strings.Contains(string(out), "not-found") {
			return fmt.Errorf("restarting dbforged: %w: %s", err, strings.TrimSpace(string(out)))
		}
	}
	// No unit: whoever started the daemon by hand restarts it by hand. Saying
	// so is better than silently leaving the old one running.
	return errors.New("update installed; restart the daemon yourself (it is not a systemd unit here)")
}

// fetchChecksums reads the release's SHA256SUMS into name -> hex digest.
func fetchChecksums(ctx context.Context, rel Release) (map[string]string, error) {
	url, ok := rel.assets[checksumAsset]
	if !ok {
		return nil, fmt.Errorf("release %s publishes no %s; refusing to install unverified binaries",
			rel.Tag, checksumAsset)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("downloading %s: %w", checksumAsset, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("downloading %s: %s", checksumAsset, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	return parseChecksums(string(body)), nil
}

// parseChecksums reads sha256sum(1) output: "<hex>  <name>" per line.
func parseChecksums(s string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(s, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || len(fields[0]) != 64 {
			continue
		}
		// A leading "*" marks binary mode; the name is the base either way,
		// since a release lists what it publishes, not where it was built.
		out[filepath.Base(strings.TrimPrefix(fields[1], "*"))] = strings.ToLower(fields[0])
	}
	return out
}

// download streams url to path and returns its SHA-256.
func download(ctx context.Context, url, path string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := httpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("downloading %s: %w", filepath.Base(path), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("downloading %s: %s", filepath.Base(path), resp.Status)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
	if err != nil {
		return "", err
	}
	defer f.Close()

	sum := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, sum), io.LimitReader(resp.Body, maxAssetBytes)); err != nil {
		return "", fmt.Errorf("downloading %s: %w", filepath.Base(path), err)
	}
	if err := f.Sync(); err != nil {
		return "", err
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

func httpClient() *http.Client {
	// No redirect policy override: GitHub serves assets from a redirect to
	// its CDN, and the checksum is what makes trusting the bytes safe.
	return &http.Client{Timeout: 10 * time.Minute}
}
