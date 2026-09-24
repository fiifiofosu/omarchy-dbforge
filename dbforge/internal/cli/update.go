package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"

	"github.com/fiifiofosu/dbforge/internal/update"
)

// Version is this binary's build version, set by main. It is compared against
// the newest release; "dev" means a local build, which is always shown the
// release.
var Version = "dev"

// cmdUpdate reports whether a newer release exists, and how to install it.
//
// It does not install one. DBForge used to replace its own binaries from the
// newest GitHub release; that is gone, because choosing the executable and the
// checksum to verify it against from the same mutable "latest" pointer proves
// only that the two agree, not that either is the reviewed artifact. See the
// package comment in internal/update.
func cmdUpdate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	// Kept as an accepted no-op: checking is now all this command does, and a
	// script or a muscle-memory `dbctl update --check` should not start
	// failing over a flag that describes the only remaining behaviour.
	fs.Bool("check", false, "deprecated; this command only ever checks")
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

	fmt.Printf("installed %s, available %s\n%s\n\n", Version, rel.Version, rel.URL)
	fmt.Println(update.InstallHint())
	return nil
}
