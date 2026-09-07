//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/fiifiofosu/dbforge/internal/doctor"
	"github.com/fiifiofosu/dbforge/internal/ports"
	"github.com/fiifiofosu/dbforge/internal/runtime"
	"github.com/fiifiofosu/dbforge/internal/store"
)

// TestDoctorOnThisMachine runs the real checks against the real host.
//
// The unit tests cover what each check says about a machine broken in a
// specific way; only this one covers whether the probes work at all. A check
// that reads the wrong cgroup path or shells out to a flag `loginctl` does not
// have would pass every unit test and be useless in the field.
func TestDoctorOnThisMachine(t *testing.T) {
	ctx := context.Background()

	cfg, err := store.DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	dataRoot, err := store.DefaultDataRoot()
	if err != nil {
		t.Fatal(err)
	}

	env := doctor.DefaultEnv(cfg, dataRoot, "/run/user/1000/dbforge/dbforged.sock", ports.DefaultRange)
	env.PingPodman = func(ctx context.Context) error {
		rt, err := runtime.NewPodman(ctx, os.Getenv("DBFORGE_PODMAN_SOCKET"))
		if err != nil {
			return err
		}
		return rt.Ping(ctx)
	}
	// The daemon may legitimately not be running here, and doctor already
	// treats that as a warning rather than a failure.
	env.PingDaemon = func(context.Context) error { return nil }

	rep := doctor.Run(ctx, env)
	for _, c := range rep.Checks {
		t.Logf("%-6s %-22s %s", c.Status, c.Name, c.Detail)
	}

	// Every check must produce a status and a non-empty detail. A blank detail
	// is the shape of a probe that silently returned nothing.
	for _, c := range rep.Checks {
		if c.Status == "" {
			t.Errorf("check %q has no status", c.Name)
		}
		if strings.TrimSpace(c.Detail) == "" {
			t.Errorf("check %q reported no detail", c.Name)
		}
	}

	// This suite only runs where podman works, so these two must be true here
	// -- if they are not, the probes are broken rather than the machine.
	for _, name := range []string{"podman installed", "podman socket"} {
		var found bool
		for _, c := range rep.Checks {
			if c.Name != name {
				continue
			}
			found = true
			if c.Status != doctor.StatusOK {
				t.Errorf("%s = %s on a machine running the integration suite: %s",
					name, c.Status, c.Detail)
			}
		}
		if !found {
			t.Errorf("no %q check ran", name)
		}
	}

	// The report has to survive the trip into a bug report.
	if _, err := json.Marshal(rep); err != nil {
		t.Fatalf("report does not marshal: %v", err)
	}
}

// The subuid and cgroup probes read real files whose formats differ between
// distributions. Reading them wrongly shows up as a check that is confidently
// wrong, which is worse than one that admits it could not tell.
func TestDoctorProbesReadRealSystemFiles(t *testing.T) {
	env := doctor.DefaultEnv("/nonexistent", "/nonexistent", "/nonexistent", ports.DefaultRange)

	if env.Username == "" {
		t.Error("could not determine the username; subuid checks would be meaningless")
	}
	if env.HomeDir == "" {
		t.Error("could not determine the home directory; %h in a unit would not resolve")
	}

	if ctrls, err := env.CgroupControllers(env.UID); err != nil {
		t.Logf("cgroup controllers unavailable here: %v", err)
	} else if len(ctrls) == 0 {
		t.Error("cgroup probe succeeded but returned no controllers")
	} else {
		t.Logf("delegated controllers: %v", ctrls)
	}

	if on, err := env.Linger(env.Username); err != nil {
		t.Logf("lingering unavailable here: %v", err)
	} else {
		t.Logf("lingering: %v", on)
	}
}
