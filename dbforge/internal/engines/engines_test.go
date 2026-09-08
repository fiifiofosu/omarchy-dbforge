package engines

import (
	"os"
	"testing"

	"github.com/fiifiofosu/dbforge/internal/ports"
)

// A port base outside the allocator's range is silently ignored, so the
// carefully chosen value would have no effect. Pin the invariant.
func TestDefaultPortBasesAreInsideDefaultRange(t *testing.T) {
	r := ports.DefaultRange
	for _, name := range Names() {
		e, err := Get(name)
		if err != nil {
			t.Fatal(err)
		}
		if e.DefaultPortBase < r.Low || e.DefaultPortBase > r.High {
			t.Errorf("%s: DefaultPortBase %d is outside the default range %d-%d",
				name, e.DefaultPortBase, r.Low, r.High)
		}
	}
}

func TestPortBasesAreUnique(t *testing.T) {
	seen := map[int]string{}
	for _, name := range Names() {
		e, _ := Get(name)
		if prev, dup := seen[e.DefaultPortBase]; dup {
			t.Errorf("%s and %s share port base %d", prev, name, e.DefaultPortBase)
		}
		seen[e.DefaultPortBase] = name
	}
}

// Postgres must not set a custom PGDATA: pointing it at a subdirectory of the
// bind mount leaves the mount root-owned and mode 0700, which the postgres
// user cannot traverse. Verified against postgres:16.
func TestPostgresDoesNotOverridePGDATA(t *testing.T) {
	e, err := Get("postgres")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := e.FirstRunEnv("pw")["PGDATA"]; ok {
		t.Fatal("postgres sets PGDATA; this breaks initdb on a bind mount")
	}
}

// A stop timeout that is too short means SIGKILL mid-write, and crash recovery
// on the next start. Observed with postgres: podman's 10s default killed it
// during initdb and an ordinary stop produced exit 137.
func TestEveryEngineDeclaresAStopTimeout(t *testing.T) {
	for _, name := range Names() {
		e, err := Get(name)
		if err != nil {
			t.Fatal(err)
		}
		if e.StopTimeoutSecs == 0 {
			t.Errorf("%s declares no stop timeout; it would inherit podman's 10s default", name)
		}
	}
}

func TestDatabaseEnginesGetGenerousStopTimeouts(t *testing.T) {
	// The SQL engines checkpoint on shutdown and need real time.
	for _, name := range []string{"postgres", "mysql", "mariadb"} {
		e, err := Get(name)
		if err != nil {
			t.Fatal(err)
		}
		if e.StopTimeoutSecs < 30 {
			t.Errorf("%s stop timeout is %ds; too short for a checkpoint on shutdown",
				name, e.StopTimeoutSecs)
		}
	}
}

func TestPostgresDataPathFollowsMajorVersion(t *testing.T) {
	pg, err := Get("postgres")
	if err != nil {
		t.Fatal(err)
	}
	// Postgres 18 moved the data directory into a per-major subdirectory and
	// its entrypoint refuses to start with a mount at the old path.
	cases := map[string]string{
		"16":          "/var/lib/postgresql/data",
		"16.3":        "/var/lib/postgresql/data",
		"17-alpine":   "/var/lib/postgresql/data",
		"18":          "/var/lib/postgresql",
		"18.6":        "/var/lib/postgresql",
		"19-bookworm": "/var/lib/postgresql",
		"latest":      "/var/lib/postgresql",
	}
	for version, want := range cases {
		if got := pg.DataPathFor(version); got != want {
			t.Errorf("DataPathFor(%q) = %q, want %q", version, got, want)
		}
	}

	// From 18 the mount is the parent of $PGDATA and the entrypoint walks
	// into it as the unprivileged postgres user, so it needs traversal.
	modes := map[string]os.FileMode{
		"16":     0o700,
		"17.5":   0o700,
		"18":     0o711,
		"18.6":   0o711,
		"latest": 0o711,
	}
	for version, want := range modes {
		if got := pg.HostDirModeFor(version); got != want {
			t.Errorf("HostDirModeFor(%q) = %#o, want %#o", version, got, want)
		}
	}
	if got := pg.HostDirModeFor("18"); got&0o077 != 0o011 {
		t.Errorf("18+ host dir mode %#o must grant traversal and nothing else", got)
	}

	// Engines without a version-specific layout fall back to DataPath.
	redis, err := Get("redis")
	if err != nil {
		t.Fatal(err)
	}
	if got := redis.DataPathFor("7"); got != "/data" {
		t.Errorf("redis DataPathFor(7) = %q, want /data", got)
	}
	if got := redis.HostDirModeFor("7"); got != 0o700 {
		t.Errorf("redis HostDirModeFor(7) = %#o, want 0700", got)
	}
}
