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
