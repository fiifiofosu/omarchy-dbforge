package engines

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// This file is the boundary between the tag a user types and the image
// DBForge actually runs.
//
// A registry tag is a mutable pointer. "postgres:16" names whatever the
// library maintainers last pushed under that name, so an instance created
// today and an instance created next month can be running different code, and
// neither of them need be the code anyone reviewed. Pulling a tag therefore
// means executing whatever the registry decides to serve at pull time.
//
// So a tag never reaches the runtime. Every image DBForge can run is listed in
// pins.go against the digest of its manifest -- a content address, which names
// one artifact and cannot be repointed -- and the tag survives only as the
// name users type and as the version the catalogue reasons about (data layout,
// directory mode). Changing what DBForge runs is a reviewed diff to pins.go,
// produced by `make pins`. See scripts/update-pins.sh.

// UnapprovedVersionError means the version is not in the pinned set.
//
// It carries the approved list because that is the only useful thing to say:
// "postgres:17.2 is not approved" invites the user to guess again, whereas
// naming what they may have is one step, not a search.
type UnapprovedVersionError struct {
	Engine   string
	Version  string
	Approved []string
}

func (e *UnapprovedVersionError) Error() string {
	if len(e.Approved) == 0 {
		return fmt.Sprintf("no approved images for engine %q", e.Engine)
	}
	return fmt.Sprintf("%s:%s is not an approved image (approved: %s)\n"+
		"DBForge only runs images pinned to a reviewed digest; adding a version "+
		"means a change to internal/engines/pins.go",
		e.Engine, e.Version, strings.Join(e.Approved, ", "))
}

// ApprovedVersions lists the versions pinned for an engine, newest first. An
// unknown engine has none, which is not an error here -- Get reports that.
func ApprovedVersions(engine string) []string {
	byTag, ok := pins[strings.ToLower(engine)]
	if !ok {
		return nil
	}
	out := make([]string, 0, len(byTag))
	for tag := range byTag {
		out = append(out, tag)
	}
	sort.Slice(out, func(i, j int) bool { return compareTags(out[i], out[j]) > 0 })
	return out
}

// IsApproved reports whether this engine and version are pinned.
func IsApproved(engine, version string) bool {
	_, ok := pins[strings.ToLower(engine)][version]
	return ok
}

// Versions lists the versions approved for this engine, newest first.
func (e Engine) Versions() []string { return ApprovedVersions(e.Name) }

// Digest is the pinned manifest digest for a version, if it has one.
func (e Engine) Digest(version string) (string, bool) {
	d, ok := pins[e.Name][version]
	return d, ok
}

// PinnedRef is the image reference to pull and run: image@sha256:...
//
// This is the only reference the runtime is ever given. An unapproved version
// has no reference at all rather than falling back to the tag, because a
// fallback is exactly the hole this closes.
func (e Engine) PinnedRef(version string) (string, error) {
	digest, ok := e.Digest(version)
	if !ok {
		return "", &UnapprovedVersionError{
			Engine:   e.Name,
			Version:  version,
			Approved: ApprovedVersions(e.Name),
		}
	}
	return e.Image + "@" + digest, nil
}

// compareTags orders version tags numerically: 10 sorts above 9, and "8.4"
// above "8.0". A component that is not a number sorts below one that is, so a
// hypothetical "8-alpine" never outranks "9".
func compareTags(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		an, aok := tagPart(as, i)
		bn, bok := tagPart(bs, i)
		switch {
		case aok && !bok:
			return 1
		case !aok && bok:
			return -1
		case an > bn:
			return 1
		case an < bn:
			return -1
		}
	}
	return strings.Compare(a, b)
}

// tagPart reads the i'th dotted component as a number. A missing component is
// zero and present, so "8" and "8.0" compare equal on the second component.
func tagPart(parts []string, i int) (int, bool) {
	if i >= len(parts) {
		return 0, true
	}
	n, err := strconv.Atoi(parts[i])
	if err != nil {
		return 0, false
	}
	return n, true
}
