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

	"github.com/fiifiofosu/dbforge/internal/cli"
	"github.com/fiifiofosu/dbforge/internal/daemon"
	"github.com/fiifiofosu/dbforge/internal/ports"
	"github.com/fiifiofosu/dbforge/internal/runtime"
	"github.com/fiifiofosu/dbforge/internal/store"
)

// version is stamped at build time: -ldflags "-X main.version=..."
var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

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
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel()}))

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

	dataRoot, err := defaultDataRoot()
	if err != nil {
		log.Error("resolving data root", "error", err)
		return 1
	}

	mgr := daemon.NewManager(rt, store.New(cfgPath), daemon.Config{
		DataRoot:  dataRoot,
		PortRange: ports.DefaultRange,
		Log:       log,
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
		"config", cfgPath, "data_root", dataRoot)

	srv := daemon.NewServer(mgr, log)
	if err := srv.Serve(ctx, daemon.SocketPath()); err != nil {
		log.Error("server stopped", "error", err)
		return 1
	}
	return 0
}

// defaultDataRoot is ~/.local/share/dbforge, honouring XDG_DATA_HOME. Data
// lives outside the package's purview so uninstalling never deletes it
// (spec 7, phase 5).
func defaultDataRoot() (string, error) {
	if d := os.Getenv("DBFORGE_DATA_ROOT"); d != "" {
		return d, nil
	}
	dir := os.Getenv("XDG_DATA_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dir, "dbforge"), nil
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
