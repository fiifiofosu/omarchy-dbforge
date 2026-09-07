package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/fiifiofosu/dbforge/internal/daemon"
	"github.com/fiifiofosu/dbforge/internal/doctor"
	"github.com/fiifiofosu/dbforge/internal/ports"
	"github.com/fiifiofosu/dbforge/internal/runtime"
	"github.com/fiifiofosu/dbforge/internal/store"
)

// cmdDoctor diagnoses the installation. It deliberately does not go through
// the daemon: the machine where this is most needed is one where the daemon
// will not start.
func cmdDoctor(ctx context.Context, c *Client, args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit the report as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}

	env, err := defaultDoctorEnv(c)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, 4*doctor.Timeout)
	defer cancel()
	rep := doctor.Run(ctx, env)

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			return err
		}
	} else {
		printReport(os.Stdout, rep)
	}

	// A failure means DBForge is broken, so the exit code has to say so --
	// this command is meant to be usable in a script or a bug report template.
	if rep.Failed() > 0 {
		return errCheckFailed{n: rep.Failed()}
	}
	return nil
}

// errCheckFailed carries a nonzero exit without printing a second error line;
// the report above already said everything.
type errCheckFailed struct{ n int }

func (e errCheckFailed) Error() string {
	return fmt.Sprintf("%d check(s) failed", e.n)
}

func defaultDoctorEnv(c *Client) (doctor.Env, error) {
	cfgPath, err := store.DefaultPath()
	if err != nil {
		return doctor.Env{}, err
	}
	dataRoot, err := store.DefaultDataRoot()
	if err != nil {
		return doctor.Env{}, err
	}

	env := doctor.DefaultEnv(cfgPath, dataRoot, daemon.SocketPath(), ports.DefaultRange)
	env.PingPodman = func(ctx context.Context) error {
		rt, err := runtime.NewPodman(ctx, os.Getenv("DBFORGE_PODMAN_SOCKET"))
		if err != nil {
			return err
		}
		return rt.Ping(ctx)
	}
	env.PingDaemon = func(ctx context.Context) error {
		_, err := c.List(ctx)
		return err
	}
	return env, nil
}

// symbols are plain ASCII on purpose: doctor output ends up pasted into bug
// reports and terminals that mangle anything cleverer.
var symbols = map[doctor.Status]string{
	doctor.StatusOK:   "  ok  ",
	doctor.StatusWarn: " warn ",
	doctor.StatusFail: " FAIL ",
	doctor.StatusSkip: " skip ",
}

func printReport(w *os.File, rep doctor.Report) {
	width := 0
	for _, c := range rep.Checks {
		if len(c.Name) > width {
			width = len(c.Name)
		}
	}

	for _, c := range rep.Checks {
		fmt.Fprintf(w, "[%s] %-*s  %s\n", symbols[c.Status], width, c.Name, c.Detail)
		if c.Fix != "" {
			for _, line := range strings.Split(c.Fix, "\n") {
				fmt.Fprintf(w, "         %s%s\n", strings.Repeat(" ", 0), line)
			}
		}
	}

	fmt.Fprintln(w)
	switch {
	case rep.Failed() > 0:
		fmt.Fprintf(w, "%d failed, %d warnings. Fix the failures top-down: "+
			"the later ones are often symptoms of the first.\n", rep.Failed(), rep.Warned())
	case rep.Warned() > 0:
		fmt.Fprintf(w, "No failures, %d warning(s). DBForge will work; "+
			"the warnings are about what happens when you log out or run out of ports.\n", rep.Warned())
	default:
		fmt.Fprintln(w, "All checks passed.")
	}
}
