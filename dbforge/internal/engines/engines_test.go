package engines

import (
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
