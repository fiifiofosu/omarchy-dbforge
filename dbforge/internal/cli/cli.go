package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/fiifiofosu/dbforge/internal/daemon"
	"github.com/fiifiofosu/dbforge/internal/engines"
	"github.com/fiifiofosu/dbforge/internal/model"
	"github.com/fiifiofosu/dbforge/internal/runtime"
)

const usage = `dbctl - manage local database instances

Usage:
  dbctl create <engine:version> [--name ID] [--port N] [--no-start]
                                [--restart no|on-failure|always]
  dbctl list [--json]
  dbctl start <id>
  dbctl stop <id>
  dbctl restart <id>
  dbctl rm <id> [--wipe-data] [--force] [--yes]
  dbctl logs <id> [--follow] [--tail N]
  dbctl tui
  dbctl status [--waybar]
  dbctl conn <id>
  dbctl restart-policy <id> <no|on-failure|always>
  dbctl restore
  dbctl engines
  dbctl doctor [--json]

Engines: %s

Data lives under ~/.local/share/dbforge and is NOT removed by 'rm' unless
--wipe-data is given.
`

// Run dispatches a dbctl invocation. It returns the process exit code.
func Run(ctx context.Context, args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Printf(usage, strings.Join(engines.Names(), ", "))
		return 0
	}

	c := NewClient(os.Getenv("DBFORGE_SOCKET"))
	cmd, rest := args[0], args[1:]

	var err error
	switch cmd {
	case "create":
		err = cmdCreate(ctx, c, rest)
	case "list", "ls":
		err = cmdList(ctx, c, rest)
	case "start":
		err = cmdSimple(ctx, rest, "start", "started", c.Start)
	case "stop":
		err = cmdSimple(ctx, rest, "stop", "stopped", c.Stop)
	case "restart":
		err = cmdSimple(ctx, rest, "restart", "restarted", c.RestartInstance)
	case "rm", "remove", "destroy":
		err = cmdRemove(ctx, c, rest)
	case "logs":
		err = cmdLogs(ctx, c, rest)
	case "conn", "connstring":
		err = cmdConn(ctx, c, rest)
	case "restart-policy":
		err = cmdRestartPolicy(ctx, c, rest)
	case "restore":
		err = cmdRestore(ctx, c)
	case "status":
		err = cmdStatus(ctx, c, rest)
	case "tui", "ui":
		err = runTUI()
	case "engines":
		fmt.Println(strings.Join(engines.Names(), "\n"))
	case "doctor":
		err = cmdDoctor(ctx, c, rest)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		fmt.Printf(usage, strings.Join(engines.Names(), ", "))
		return 2
	}

	if err != nil {
		// doctor has already printed a full report; adding "error: 3 check(s)
		// failed" underneath it says nothing new.
		var checks errCheckFailed
		if errors.As(err, &checks) {
			return 1
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	return 0
}

// splitLeadingPositional pulls a leading non-flag argument off the front of
// args.
//
// Go's flag package stops parsing at the first positional argument, so
// `dbctl rm app-db --wipe-data` would parse zero flags and silently ignore
// --wipe-data. Every subcommand that takes both a positional and flags must
// go through this, or flags become order-dependent in a way nobody expects.
func splitLeadingPositional(args []string) (positional string, rest []string) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		return args[0], args[1:]
	}
	return "", args
}

func cmdCreate(ctx context.Context, c *Client, args []string) error {
	fs := flag.NewFlagSet("create", flag.ContinueOnError)
	name := fs.String("name", "", "instance id (default: engine+version)")
	port := fs.Int("port", 0, "pin a host port (default: auto-allocate)")
	noStart := fs.Bool("no-start", false, "create without starting")
	restart := fs.String("restart", "", "restart policy: no, on-failure, always (default always)")

	ref, args := splitLeadingPositional(args)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if ref == "" {
		ref = fs.Arg(0)
	}
	if ref == "" {
		return fmt.Errorf("usage: dbctl create <engine:version> [--name ID]")
	}

	// Progress goes to stderr so `dbctl create ... > file` still captures only
	// the result, and so a pipeline is not fed a spinner.
	lastPhase := ""
	onProgress := func(ev runtime.PullEvent) {
		line := ev.Message
		if ev.Layer > 0 {
			line = fmt.Sprintf("%s (layer %d)", line, ev.Layer)
		}
		if line == lastPhase {
			return // podman repeats a phase per layer; say it once
		}
		lastPhase = line
		fmt.Fprintf(os.Stderr, "  %s\n", line)
	}

	inst, err := c.CreateStream(ctx, daemon.CreateOptions{
		Ref: ref, ID: *name, Port: *port, Start: !*noStart,
		Restart: model.RestartPolicy(*restart),
	}, onProgress)
	if err != nil {
		return err
	}

	fmt.Printf("Created %s (%s:%s) on port %d\n", inst.ID, inst.Engine, inst.Version, inst.Port)
	fmt.Printf("  data: %s\n", inst.DataDir)
	fmt.Printf("  restart: %s\n", inst.Restart)
	if cs, err := c.ConnString(ctx, inst.ID); err == nil {
		fmt.Printf("  conn: %s\n", cs)
	}
	return nil
}

func cmdList(ctx context.Context, c *Client, args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit JSON (for scripts and the waybar module)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	list, err := c.List(ctx)
	if err != nil {
		return err
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(list)
	}

	if len(list) == 0 {
		fmt.Println("No instances. Create one with: dbctl create postgres:16")
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tENGINE\tVERSION\tPORT\tSTATUS\tRESTART\tCREATED")
	for _, i := range list {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\t%s\t%s\n",
			i.ID, i.Engine, i.Version, i.Port, statusLabel(i), i.Restart, age(i.CreatedAt))
	}
	tw.Flush()

	for _, i := range list {
		if i.Status == model.StatusMissing {
			fmt.Fprintf(os.Stderr,
				"\nwarning: %q has no container -- it was removed outside dbforge.\n"+
					"Its data is still at %s. Use `dbctl rm %s` to forget it.\n", i.ID, i.DataDir, i.ID)
		}
		if i.Status != model.StatusRunning && i.LastExitCode != 0 {
			fmt.Fprintf(os.Stderr,
				"\nwarning: %q exited uncleanly (code %d). The engine may run crash\n"+
					"recovery on next start. Check `dbctl logs %s`.\n", i.ID, i.LastExitCode, i.ID)
		}
	}
	return nil
}

func statusLabel(i model.Instance) string {
	switch {
	case i.Status == model.StatusMissing:
		return "missing(!)"
	case i.Status != model.StatusRunning && i.LastExitCode != 0:
		return fmt.Sprintf("exited(%d)", i.LastExitCode)
	default:
		return string(i.Status)
	}
}

func cmdRestartPolicy(ctx context.Context, c *Client, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: dbctl restart-policy <id> <no|on-failure|always>")
	}
	if err := c.SetRestartPolicy(ctx, args[0], args[1]); err != nil {
		return err
	}
	fmt.Printf("%s: restart policy set to %s\n", args[0], args[1])
	return nil
}

// cmdRestore is mostly a diagnostic: the daemon restores on startup by itself.
// Being able to trigger it by hand makes the reboot path testable.
func cmdRestore(ctx context.Context, c *Client) error {
	rep, err := c.Restore(ctx)
	if err != nil {
		return err
	}
	if len(rep.Started) == 0 && len(rep.Failed) == 0 {
		fmt.Println("Nothing to restore.")
	}
	for _, id := range rep.Started {
		fmt.Printf("started %s\n", id)
	}
	for id, reason := range rep.Failed {
		fmt.Fprintf(os.Stderr, "failed to start %s: %s\n", id, reason)
	}
	for id, reason := range rep.Skipped {
		fmt.Printf("skipped %s (%s)\n", id, reason)
	}
	if len(rep.Failed) > 0 {
		return fmt.Errorf("%d instance(s) failed to restore", len(rep.Failed))
	}
	return nil
}

func age(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func cmdSimple(ctx context.Context, args []string, verb, pastTense string, fn func(context.Context, string) error) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: dbctl %s <id>", verb)
	}
	if err := fn(ctx, args[0]); err != nil {
		return err
	}
	fmt.Printf("%s: %s\n", args[0], pastTense)
	return nil
}

// cmdRemove guards the single most consequential action in the tool. A bare
// "y/N" is not enough for data deletion (spec 7, phase 3), so --wipe-data
// additionally requires typing the instance name.
func cmdRemove(ctx context.Context, c *Client, args []string) error {
	fs := flag.NewFlagSet("rm", flag.ContinueOnError)
	wipe := fs.Bool("wipe-data", false, "ALSO delete the data directory (irreversible)")
	force := fs.Bool("force", false, "remove even if running")
	yes := fs.Bool("yes", false, "skip confirmation (scripts only)")

	id, args := splitLeadingPositional(args)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if id == "" {
		id = fs.Arg(0)
	}
	if id == "" {
		return fmt.Errorf("usage: dbctl rm <id> [--wipe-data]")
	}

	if *wipe && !*yes {
		fmt.Printf("This will PERMANENTLY DELETE all data for %q.\n", id)
		fmt.Printf("Type the instance name to confirm: ")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if strings.TrimSpace(line) != id {
			return fmt.Errorf("confirmation did not match; nothing was removed")
		}
	}

	if err := c.Remove(ctx, id, *wipe, *force); err != nil {
		return err
	}
	if *wipe {
		fmt.Printf("%s: container and data removed\n", id)
	} else {
		fmt.Printf("%s: container removed (data kept)\n", id)
	}
	return nil
}

func cmdLogs(ctx context.Context, c *Client, args []string) error {
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	follow := fs.Bool("follow", false, "stream new output")
	tail := fs.Int("tail", 200, "lines of history")

	id, args := splitLeadingPositional(args)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if id == "" {
		id = fs.Arg(0)
	}
	if id == "" {
		return fmt.Errorf("usage: dbctl logs <id> [--follow]")
	}
	return c.Logs(ctx, id, *follow, *tail, os.Stdout)
}

func cmdConn(ctx context.Context, c *Client, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: dbctl conn <id>")
	}
	cs, err := c.ConnString(ctx, args[0])
	if err != nil {
		return err
	}
	fmt.Println(cs)
	return nil
}
