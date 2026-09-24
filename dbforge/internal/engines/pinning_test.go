package engines

import (
	"errors"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// digestRef matches a reference pinned by content: repository@sha256:<64 hex>.
var digestRef = regexp.MustCompile(`^[a-z0-9./-]+@sha256:[0-9a-f]{64}$`)

// Every image DBForge can run must be named by a digest. A pin that is not a
// well-formed digest is a pin that pins nothing, and the generator is not the
// only way this file could ever be edited.
func TestEveryPinIsAWellFormedDigest(t *testing.T) {
	for engine, byTag := range pins {
		e, err := Get(engine)
		if err != nil {
			t.Errorf("pins.go pins %q, which is not an engine in the catalogue: %v", engine, err)
			continue
		}
		if len(byTag) == 0 {
			t.Errorf("engine %q has an empty pin set, so nothing can be created", engine)
		}
		for tag := range byTag {
			ref, err := e.PinnedRef(tag)
			if err != nil {
				t.Errorf("%s:%s: %v", engine, tag, err)
				continue
			}
			if !digestRef.MatchString(ref) {
				t.Errorf("%s:%s resolves to %q, which is not repository@sha256:<64 hex>",
					engine, tag, ref)
			}
			if !strings.HasPrefix(ref, e.Image+"@") {
				t.Errorf("%s:%s resolves to %q, which is not under the engine's repository %q",
					engine, tag, ref, e.Image)
			}
		}
	}
}

// An engine nobody can create is worse than an engine that is not offered: the
// user picks it in the form and then finds there are no versions.
func TestEveryCataloguedEngineHasPins(t *testing.T) {
	for _, name := range Names() {
		if got := ApprovedVersions(name); len(got) == 0 {
			t.Errorf("engine %q is in the catalogue but has no pinned versions", name)
		}
	}
}

// The tag is what the version-dependent behaviour keys off -- the Postgres 18
// layout change, the host directory mode -- so a pin whose tag does not parse
// as a version silently picks the wrong data path.
func TestPinnedPostgresTagsDriveTheLayoutChoice(t *testing.T) {
	pg, err := Get("postgres")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range pg.Versions() {
		major, ok := majorVersion(v)
		if !ok {
			t.Errorf("pinned postgres tag %q has no major version, so the data "+
				"layout for it is guessed rather than known", v)
			continue
		}
		want := "/var/lib/postgresql"
		if major < 18 {
			want = "/var/lib/postgresql/data"
		}
		if got := pg.DataPathFor(v); got != want {
			t.Errorf("postgres:%s mounts at %q, want %q", v, got, want)
		}
	}
}

func TestParseRefRejectsAnUnpinnedVersion(t *testing.T) {
	cases := []string{
		"postgres:16.3",   // a real tag upstream, but not one that is pinned
		"postgres:latest", // mutable by definition
		"redis:7-alpine",  // a variant nobody reviewed
		"postgres:99",     // does not exist at all
	}
	for _, ref := range cases {
		_, _, err := ParseRef(ref)
		if err == nil {
			t.Errorf("ParseRef(%q) was accepted", ref)
			continue
		}
		var unapproved *UnapprovedVersionError
		if !errors.As(err, &unapproved) {
			t.Errorf("ParseRef(%q) = %v, want an UnapprovedVersionError", ref, err)
		}
	}
}

func TestParseRefAcceptsEveryPinnedVersion(t *testing.T) {
	for _, name := range Names() {
		for _, v := range ApprovedVersions(name) {
			eng, version, err := ParseRef(name + ":" + v)
			if err != nil {
				t.Errorf("ParseRef(%s:%s): %v", name, v, err)
				continue
			}
			if eng.Name != name || version != v {
				t.Errorf("ParseRef(%s:%s) = %s:%s", name, v, eng.Name, version)
			}
		}
	}
}

// The form shows this list in order, so the newest version is the one under
// the cursor. A string sort would put 9 above 10 and 8.4 below 8.0.
func TestApprovedVersionsAreOrderedNewestFirst(t *testing.T) {
	if got := ApprovedVersions("mysql"); !slices.Equal(got, []string{"9", "8.4", "8.0"}) {
		t.Errorf("mysql versions = %v, want [9 8.4 8.0]", got)
	}
	if got := ApprovedVersions("mariadb"); !slices.Equal(got, []string{"11", "10.11", "10.6"}) {
		t.Errorf("mariadb versions = %v, want [11 10.11 10.6]", got)
	}
	if got := ApprovedVersions("postgres"); !slices.IsSortedFunc(got, func(a, b string) int {
		return compareTags(b, a)
	}) {
		t.Errorf("postgres versions are not newest-first: %v", got)
	}
}

func TestApprovedVersionsOfAnUnknownEngineIsEmpty(t *testing.T) {
	if got := ApprovedVersions("cassandra"); got != nil {
		t.Errorf("got %v, want nil for an engine that is not in the catalogue", got)
	}
}

// The error is the only place a user learns what they may have instead.
func TestUnapprovedVersionErrorNamesTheApprovedOnes(t *testing.T) {
	_, _, err := ParseRef("redis:5")
	if err == nil {
		t.Fatal("redis:5 was accepted")
	}
	for _, v := range ApprovedVersions("redis") {
		if !strings.Contains(err.Error(), v) {
			t.Errorf("the error does not mention approved version %q: %v", v, err)
		}
	}
}
