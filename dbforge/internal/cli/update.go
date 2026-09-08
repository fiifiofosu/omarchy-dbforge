package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/fiifiofosu/dbforge/internal/update"
)

// Version is this binary's build version, set by main. The updater compares it
// against the newest release; "dev" means a local build, which is always
// offered the release.
var Version = "dev"

// cmdUpdate installs the newest published release over this installation.
func cmdUpdate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	check := fs.Bool("check", false, "report whether an update exists, install nothing")
	if err := fs.Parse(args); err != nil {
		return err
	}

	rel, err := update.Latest(ctx)
	if errors.Is(err, update.ErrNoRelease) {
		return fmt.Errorf("no published release found for %s "+
			"(a private repository publishes none this machine can see)", update.Repo)
	}
	if err != nil {
		return err
	}

	if !update.Newer(Version, rel.Version) {
		fmt.Printf("dbforge %s is the latest release.\n", rel.Version)
		return nil
	}
	fmt.Printf("installed %s, available %s\n%s\n", Version, rel.Version, rel.URL)
	if *check {
		return nil
	}

	dir, err := update.InstallDir()
	if err != nil {
		return err
	}
	replaced, err := update.Apply(ctx, rel, dir, func(s string) {
		fmt.Printf("  %s\n", s)
	})
	if err != nil {
		return err
	}
	fmt.Printf("installed %s (%d binaries in %s)\n", rel.Version, len(replaced), dir)

	if err := update.RestartDaemon(ctx); err != nil {
		// The binaries are in place; only the restart needs a hand. That is a
		// note, not a failed update.
		fmt.Fprintf(os.Stderr, "note: %v\n", err)
		return nil
	}
	fmt.Println("restarted dbforged")
	return nil
}
