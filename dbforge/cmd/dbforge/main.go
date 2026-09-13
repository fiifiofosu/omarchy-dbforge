// Command dbforge is both the daemon and the CLI, per spec 5: one binary,
// dispatched on how it was invoked. Installed as two names:
//
//	dbforged -> the daemon
//	dbctl    -> the CLI
//
// Invoking `dbforge daemon` is equivalent to running it as dbforged.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/fiifiofosu/dbforge/internal/cli"
	"github.com/fiifiofosu/dbforge/internal/daemon"
	"github.com/fiifiofosu/dbforge/internal/notify"
	"github.com/fiifiofosu/dbforge/internal/ports"
	"github.com/fiifiofosu/dbforge/internal/runtime"
	"github.com/fiifiofosu/dbforge/internal/store"
)

// version is stamped at build time: -ldflags "-X main.version=..."
var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// So `dbctl update` can tell this build apart from the newest release.
	cli.Version = version

	args := os.Args[1:]
	switch {
	case len(args) > 0 && (args[0] == "--version" || args[0] == "version"):
		fmt.Printf("dbforge %s\n", version)
	case filepath.Base(os.Args[0]) == "dbforged":
		os.Exit(runDaemon(ctx, args))
	case len(args) > 0 && args[0] == "daemon":
		os.Exit(runDaemon(ctx, args[1:]))
	default:
		os.Exit(cli.Run(ctx, args))
	}
}

func runDaemon(ctx context.Context, _ []string) int {
	log := slog.New(logHandler())

	// So a client can tell whether it is older than the daemon it is talking
	// to; an upgrade replaces binaries without restarting running processes.
	daemon.Version = version

	rt, err := runtime.NewPodman(ctx, os.Getenv("DBFORGE_PODMAN_SOCKET"))
	if err != nil {
		log.Error("podman unavailable", "error", err)
		return 1
	}

	cfgPath, err := store.DefaultPath()
	if err != nil {
		log.Error("resolving config path", "error", err)
		return 1
	}

	dataRoot, err := store.DefaultDataRoot()
	if err != nil {
		log.Error("resolving data root", "error", err)
		return 1
	}

	notifier := notify.FromEnv(log)
	defer notifier.Stop()

	// Read the config once before anything writes, so an upgrade from an older
	// schema can be reported accurately -- the first save rewrites the file in
	// the current schema and the evidence is gone. A file from a *newer*
	// dbforge is a hard stop here rather than a confusing failure later.
	st := store.New(cfgPath)
	if _, err := st.Load(); err != nil {
		log.Error("reading config", "path", cfgPath, "error", err)
		return 1
	}
	migratedFrom := st.LoadedVersion()

	mgr := daemon.NewManager(rt, st, daemon.Config{
		DataRoot:  dataRoot,
		PortRange: ports.DefaultRange,
		Log:       log,
		Scope:     os.Getenv("DBFORGE_SCOPE"),
		Notifier:  notifier,
	})

	// Reconcile before serving so the first request sees accurate state and
	// any crash debris from the previous run is cleaned up (spec 7, phase 1).
	rep, err := mgr.Reconcile(ctx)
	if err != nil {
		log.Error("initial reconciliation failed", "error", err)
		return 1
	}
	log.Info("reconciled",
		"adopted", rep.Adopted, "missing", rep.Missing, "pruned", rep.Pruned,
		"unclean", rep.Unclean, "config", cfgPath, "data_root", dataRoot)

	if migratedFrom < store.SchemaVersion {
		log.Info("migrated config schema", "from", migratedFrom,
			"to", store.SchemaVersion, "backup", st.BackupPath(migratedFrom))
	}

	// Bring back instances the user left running. Rootless Podman does not
	// start containers at boot by itself, so after a reboot this is what makes
	// an instance come back (spec 7, phase 2). Instances the user explicitly
	// stopped are left alone.
	if os.Getenv("DBFORGE_NO_RESTORE") == "" {
		restored, err := mgr.Restore(ctx)
		if err != nil {
			log.Error("restore failed", "error", err)
		} else if len(restored.Started) > 0 || len(restored.Failed) > 0 {
			log.Info("restored instances",
				"started", restored.Started, "failed", restored.Failed)
		}
	}

	// Keep restoring while we run, not just at startup, so a crashed instance
	// with restart=always actually comes back.
	go mgr.Supervise(ctx, superviseInterval())

	srv := daemon.NewServer(mgr, log)
	if err := srv.Serve(ctx, daemon.SocketPath()); err != nil {
		log.Error("server stopped", "error", err)
		return 1
	}
	return 0
}

// superviseInterval is how often the daemon re-checks that instances which
// should be running actually are. Zero disables supervision.
func superviseInterval() time.Duration {
	if v := os.Getenv("DBFORGE_SUPERVISE_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err == nil {
			return d
		}
	}
	return 30 * time.Second
}

// logHandler builds the daemon's logger.
//
// Text by default, because the usual reader is a person running
// `journalctl --user -u dbforged`. DBFORGE_LOG_FORMAT=json switches to JSON
// for the other case: attaching daemon output to a bug report, where being
// able to filter by field beats being able to skim (spec 7, phase 6).
func logHandler() slog.Handler {
	opts := &slog.HandlerOptions{Level: logLevel()}
	if os.Getenv("DBFORGE_LOG_FORMAT") == "json" {
		return slog.NewJSONHandler(os.Stderr, opts)
	}
	return slog.NewTextHandler(os.Stderr, opts)
}

func logLevel() slog.Level {
	switch os.Getenv("DBFORGE_LOG_LEVEL") {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
