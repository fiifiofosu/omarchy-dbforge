# DBForge

Spin up local database instances on Omarchy in seconds — any engine, any
version, side by side, without touching pacman.

DBForge is what DBngin is on macOS, rebuilt for how Arch actually works. Rather
than juggling per-version native binaries (which pacman's one-version-per-
package model fights), every instance is a rootless Podman container with its
own port and its own data directory. Creating a Postgres 16 alongside a
Postgres 15 alongside a Redis 7 is three commands and no conflicts.

> **Status: Phase 4 complete (waybar).** Lifecycle, port allocation,
> persistence, drift reconciliation, restart policies, reboot recovery, the TUI
> and the status bar widget are implemented, unit tested, and verified end to
> end against real rootless Podman containers. The Quickshell module (4b) is a
> deliberate follow-up. See [Roadmap](#roadmap).

---

## Table of contents

- [Why this exists](#why-this-exists)
- [Install](#install)
- [Upgrading and uninstalling](#upgrading-and-uninstalling)
- [Quick start](#quick-start)
- [Commands](#commands)
- [Troubleshooting](#troubleshooting)
- [How it works](#how-it-works)
- [Where things live](#where-things-live)
- [Design decisions](#design-decisions)
- [Safety model](#safety-model)
- [Supported engines](#supported-engines)
- [Development](#development)
- [Troubleshooting](#troubleshooting)
- [Roadmap](#roadmap)

---

## Why this exists

Arch gives you exactly one version of Postgres at a time. That is fine until a
client project needs 15, a side project needs 16, and something old needs 13.
The usual workarounds are all bad: hand-rolled `docker run` incantations you
retype from shell history, `docker-compose.yml` files copied between projects
that drift apart, or version managers that shell out to a container runtime
anyway but hide what they did.

DBForge's premise is that managing *instances* is a small, well-defined problem
worth solving properly, and that browsing data is somebody else's job. Point
DBeaver, TablePlus or `psql` at the connection string it gives you.

**Non-goals:** cloud/remote databases, a GUI query editor, backup and
replication tooling.

---

## Install

### Requirements

| Requirement | Why |
|---|---|
| Rootless Podman | The container runtime. Docker is not used, see [Design decisions](#design-decisions) |
| Go 1.26+ | Build only |
| `subuid`/`subgid` entries for your user | Rootless containers. Arch sets these up by default |
| cgroups v2 with `cpu`, `memory`, `pids` delegated | Per-instance resource limits |

### From source

This is the only install path today. The repository is private and nothing is
published yet, so the AUR package below is built but not uploaded.

```bash
git clone https://github.com/fiifiofosu/dbforge
cd dbforge
make install
./packaging/install.sh
```

`make install` puts a single binary under two names (`dbforged`, `dbctl`) into
`~/.local/bin` and renders the systemd user unit for that location.
`install.sh` then enables `podman.socket` and `dbforged`, and tells you if user
lingering is off.

A per-user unit in `~/.config/systemd/user/` overrides a packaged one of the
same name. If you install from source and later install the package, remove the
per-user unit or it will keep pointing at `~/.local/bin`:

```bash
rm ~/.config/systemd/user/dbforged.service && systemctl --user daemon-reload
```

### As an Arch package — built, not yet published

`packaging/aur/dbforge-git/` is a complete, tested PKGBUILD, and
`make pkgbuild-check` builds it and verifies what it installs. It is **not on
the AUR**: it clones this repository, which is private, so it would fail at
`Retrieving sources...` for anyone else. Publishing waits on the repository
going public, which is a Phase 6 decision alongside the first tagged release.

You can still build and install it locally:

```bash
packaging/aur/check.sh --install
```

That installs to `/usr/bin`, puts the systemd user unit in
`/usr/lib/systemd/user/`, and enables it for all users with `systemctl
--global enable`. Two things it deliberately does not do for you, because
neither is a package's decision to make:

```bash
systemctl --user enable --now podman.socket   # the daemon talks to this
sudo loginctl enable-linger $USER             # so databases survive logout
systemctl --user start dbforged
```

To keep the service from being enabled — on install and on every future
upgrade:

```bash
sudo mkdir -p /etc/dbforge && sudo touch /etc/dbforge/no-autoenable
sudo systemctl --global disable dbforged.service
```

### A note on PATH

The systemd user manager does **not** have `~/.local/bin` on its `PATH` by
default, even though your shell does — `~/.bashrc` is never sourced for user
services. The shipped unit sets `PATH` explicitly for this reason. If you
invoke `dbctl` from a Hyprland keybind or a `.desktop` file, use the absolute
path for the same reason.

---

## Upgrading and uninstalling

### Upgrading

An upgrade replaces the binaries; picking them up means restarting the daemon:

```bash
systemctl --user restart dbforged
```

**This does not stop your databases.** The containers are owned by Podman, not
by the unit — `KillMode=process` means stopping the daemon kills only the
daemon. On startup it reconciles against Podman and reattaches to whatever is
already running, so an upgrade mid-afternoon does not drop your connections.
There is an integration test that asserts exactly this: same container ID, same
start time, same open state, across a daemon replacement.

If the config schema changed between versions, the daemon migrates
`instances.toml` on first start and keeps the original as
`instances.toml.v<N>.bak`. It logs `migrated config schema` when it does. A
config written by a *newer* DBForge than the one you are running is a hard
error rather than a guess — downgrade-then-upgrade never corrupts the file.

### Uninstalling

```bash
sudo pacman -Rns dbforge-git    # if installed as a package
make uninstall                  # from source
```

Either way **your databases are not removed.** Containers stay in Podman and
data stays in `~/.local/share/dbforge`, because uninstalling a manager should
never delete the thing it was managing. To find them afterwards:

```bash
podman ps -a --filter label=io.dbforge.managed
ls ~/.local/share/dbforge
```

To remove the data too, do it deliberately *before* uninstalling, when the tool
that understands the layout is still installed:

```bash
dbctl ls                              # list what you have
dbctl rm <id> --wipe-data             # per instance
```

`--wipe-data` is the only path that removes database files, and it goes through
`podman unshare` — the files are owned by a subuid your user cannot unlink
directly, so `rm -rf` on that directory fails with EPERM.

---

## Quick start

```bash
# Create and start a Postgres 16 instance
dbctl create postgres:16 --name app-db

# Created app-db (postgres:16) on port 15433
#   data: /home/you/.local/share/dbforge/postgres/16/app-db
#   conn: postgresql://postgres:xK3...@127.0.0.1:15433/postgres

# A second Postgres at a different version, no conflict
dbctl create postgres:15 --name legacy-db

# And a Redis
dbctl create redis:7

dbctl list
# ID          ENGINE    VERSION  PORT   STATUS   CREATED
# app-db      postgres  16       15433  running  just now
# legacy-db   postgres  15       15434  running  just now
# redis7      redis     7        15435  running  just now

psql "$(dbctl conn app-db)"
```

---

## Commands

```
dbctl create <engine:version> [--name ID] [--port N] [--no-start]
dbctl list [--json]
dbctl start <id>
dbctl stop <id>
dbctl rm <id> [--wipe-data] [--force] [--yes]
dbctl logs <id> [--follow] [--tail N]
dbctl conn <id>
dbctl restart-policy <id> <no|on-failure|always>
dbctl restore
dbctl engines
```

**`create`** allocates a free port, creates the data directory, pulls the image
if needed and starts the container. `--port` pins a specific port (useful for
claiming the conventional 5432); it fails loudly rather than silently picking
another if that port is taken.

**`list --json`** is the scripting surface, and what the Phase 4 waybar module
will consume. It always emits a JSON array, never `null`, so consumers can tell
"no instances" from "daemon unreachable" — those must look different.

**`rm`** removes the *container* and keeps your data. See
[Safety model](#safety-model).

**`logs --follow`** streams until you interrupt it.

**`restart-policy`** changes whether an instance comes back automatically. It
takes effect immediately -- no need to recreate the container.

**`restore`** starts everything that should be running. The daemon does this
itself on startup and on an interval; the command exists so the reboot path is
testable by hand.

---

### `dbctl doctor`

The first thing to run when something does not work. It checks the rootless
setup end to end -- podman, the socket, subuid/subgid ranges, the id-map
helpers, cgroup delegation, the data directory, config permissions, port
availability, the systemd unit, lingering, and the daemon -- and tells you what
to do about anything it finds.

```
$ dbctl doctor
[  ok  ] podman installed     /usr/bin/podman
[  ok  ] podman socket        responding
[  ok  ] running rootless     uid 1000
[  ok  ] subuid range         65536 ids from 100000
[  ok  ] subgid range         65536 ids from 100000
[  ok  ] newuidmap/newgidmap  present
[  ok  ] cgroup delegation    cpu memory pids
[  ok  ] data directory       /home/you/.local/share/dbforge
[  ok  ] config file          /home/you/.config/dbforge/instances.toml (952 bytes)
[  ok  ] port range           64 of 64 probed ports free in 15000-15999 (sampled 64)
[  ok  ] systemd unit         /home/you/.config/systemd/user/dbforged.service -> ...
[  ok  ] user lingering       enabled
[  ok  ] dbforged             responding on /run/user/1000/dbforge/dbforged.sock

All checks passed.
```

Warnings never fail the command — lingering being off or cgroup controllers
not being delegated are things DBForge works around, and failing on them would
train you to ignore the output. A failure exits non-zero, so this is usable in
a script or a bug report:

```bash
dbctl doctor --json > doctor.json
```

It changes nothing, so it is safe to run on a machine where nothing works —
which is the only machine you will ever run it on.

---

## The status bar widget

```bash
./packaging/waybar/install.sh
```

Adds a `custom/dbforge` module to your waybar config, backing the file up first
and refusing to double-add.

| State | Shows |
|---|---|
| Instances running | database icon + running count |
| All stopped | dimmed database icon |
| No instances | dimmed database icon, tooltip explaining |
| Something wrong | alert icon + count, in red |
| **Daemon not running** | alert icon, in amber, tooltip with the fix |

The last two rows are the point: "the daemon is down" and "you have no
instances" are both an absence, and rendered naively they look identical. They
need different responses, so they get different icons, classes and tooltips.

**Left-click** opens the TUI in a terminal. **Right-click** gives a one-gesture
start/stop menu through walker (or fuzzel/wofi). The menu never offers to
delete data — that belongs behind the TUI's typed confirmation.

With many instances the tooltip summarises rather than listing them all, and
sorts anything broken to the top so it is not the row that gets truncated away.

### Updates are signal-driven, not polled

`dbforged` sends `SIGRTMIN+8` whenever instance state changes, and the module
listens on that signal. The widget updates the moment something happens rather
than waking the CPU on a timer — the plan asks for this specifically, to avoid
battery churn. Signals are debounced, so a create that touches state several
times produces one refresh.

The module also carries `"interval": 300` as a slow fallback in case a signal
is missed. Set `DBFORGE_WAYBAR_SIGNAL=0` to disable signalling and rely purely
on polling.

### Absolute paths matter here

The installer writes absolute paths into the config, and this is not
cosmetic: waybar is started by systemd, whose user manager has no
`~/.local/bin` on `PATH`, because user services never source `~/.bashrc`. A
module referring to a bare `dbctl` finds nothing and renders an **empty
widget, silently** — no error anywhere. The same applies to the click handlers
and inside the menu script.

## The TUI

```bash
dbctl tui
```

A full-screen view of every instance, with actions on the selected row.

```
DBForge  local database instances

  ID               ENGINE    VERSION  PORT   STATUS      RESTART    UPTIME
> app-db           postgres  16       15432  running     always     2h14m
  cache            redis     7        15379  stopped     no         -

s start/stop   R restart   l logs   c connection   n new   d destroy   r refresh   q quit
```

| Key | Action |
|---|---|
| `j`/`k` or arrows | Move |
| `s` | Start or stop the selected instance |
| `R` | Restart it |
| `l` or enter | Follow its logs |
| `c` | Show the connection string |
| `n` | Create a new instance (guided) |
| `d` | Destroy it |
| `r` | Refresh now |
| `q` | Quit |

**Creating** walks engine → version → name → port → restart policy. The version
list comes from Docker Hub, filtered to plain version tags and sorted newest
first. Offline it falls back to a cache and then to a builtin list, and says
which it is showing rather than pretending the list is current.

**Destroying** asks what should happen to the data. Removing the container and
keeping the data is preselected, so a reflexive enter never deletes a database.
Choosing to delete the data additionally requires typing the instance name.

**Logs** stream live. Backing out cancels the stream rather than orphaning it.

### Why the TUI is a separate binary

`dbforge-tui` is its own executable, and `dbctl tui` runs it. Bubble Tea's
package init queries the terminal (background colour, then a cursor-position
report) and blocks for termenv's 5-second timeout if the terminal does not
answer. Because that runs at *import* time, merely linking the TUI into the CLI
made `dbctl list` take 5.03s instead of 0.05s in an environment that does not
reply -- CI, or a bare pty. Interactive terminals answer instantly and were
never affected, but a CLI should not depend on that.

## Restart behaviour

Instances come back after a reboot. Two separate things decide whether:

| Restart policy | Meaning |
|---|---|
| `always` (default) | Comes back whenever it should be running -- crash, daemon restart, or reboot |
| `on-failure` | Comes back only after an unclean exit, not after a clean one |
| `no` | Only ever started explicitly |

The policy is one half. The other is **desired state**: what you last asked
for. An instance you stopped with `dbctl stop` stays stopped across a reboot
*whatever its policy says*, because otherwise `dbctl stop` would not mean
anything the next morning. Only an explicit `start` or `stop` changes it.

The daemon re-checks on an interval (30s by default,
`DBFORGE_SUPERVISE_INTERVAL`), so a crashed instance with `always` comes back
while the host stays up, not just at the next login.

### Why the container's own restart policy is always "no"

Podman has its own restart policy, and DBForge deliberately does not use it.
Podman re-evaluates that policy when the podman service restarts, and it has no
idea whether you stopped an instance on purpose -- so a container marked
`always` comes back after `dbctl stop` and silently undoes your decision. This
was observed, not theorised: a stopped instance restarted itself when
`podman.socket` was bounced.

Restart policy is the daemon's to enforce, because only the daemon knows
desired state. Two controllers with different information is one too many.

### Lingering

For instances to survive **logout** and come back at **boot**, the systemd user
manager has to keep running when you are not logged in:

```bash
sudo loginctl enable-linger $USER
```

Without it, your databases stop at logout and stay down until you next log in.
`packaging/install.sh` checks this and tells you if it is missing.

## How it works

```
  dbctl            (future) TUI          (future) waybar module
    │                   │                          │
    └───────────────────┴──────────────┬───────────┘
                                       │  HTTP/JSON over a Unix socket
                              ┌────────▼─────────┐
                              │     dbforged     │   single writer, owns state
                              │ systemd --user   │
                              └────────┬─────────┘
                                       │  Podman REST API (rootless)
                              ┌────────▼─────────┐
                              │ one container    │
                              │ per instance     │
                              └──────────────────┘
```

Every frontend goes through the daemon rather than talking to Podman directly.
That is what makes port allocation race-free and lets the future status widget
stay a dumb reader instead of a second thing that can mutate containers.

The transport is plain HTTP over a Unix socket at
`$XDG_RUNTIME_DIR/dbforge/dbforged.sock`, mode `0600`. HTTP rather than a
bespoke protocol so the waybar module can be a shell script with `curl`.

### Podman is the source of truth

`instances.toml` is an **index, not the truth**. Everything needed to
reconstruct an instance is stamped onto the container as labels
(`io.dbforge.id`, `io.dbforge.engine`, `io.dbforge.port`, …). On every startup
and every `list`, the daemon reconciles the file against live Podman state and
handles three kinds of drift:

| Situation | What DBForge does |
|---|---|
| Container has our label but no config entry (daemon died after create, before save) | **Adopts** it, rebuilding the entry from labels |
| Config entry whose container has vanished (you ran `podman rm` yourself) | Marks it **`missing`** and warns. Never silently recreates it — your data is still there |
| Config entry stuck in `creating` with no container (create died partway) | **Prunes** it, freeing the id and port |

Reconciliation is idempotent; a steady state reports no drift.

---

## Where things live

| Path | Contents |
|---|---|
| `~/.config/dbforge/instances.toml` | Instance index. Mode `0600` — it holds generated passwords |
| `~/.local/share/dbforge/<engine>/<version>/<id>/` | The actual database files |
| `$XDG_RUNTIME_DIR/dbforge/dbforged.sock` | Daemon socket, mode `0600` |
| `~/.config/systemd/user/dbforged.service` | The user service (source install) |
| `/usr/lib/systemd/user/dbforged.service` | The user service (packaged install) |
| `~/.config/dbforge/instances.toml.v<N>.bak` | Pre-migration config, kept when an upgrade changes the schema |

Data deliberately lives outside anything a package manager owns, so
uninstalling DBForge never deletes a database.

### Environment variables

| Variable | Purpose |
|---|---|
| `DBFORGE_SOCKET` | Override the daemon socket path (both the daemon and the CLI) |
| `DBFORGE_PODMAN_SOCKET` | Override the Podman socket |
| `DBFORGE_DATA_ROOT` | Override where instance data is stored |
| `DBFORGE_LOG_LEVEL` | `debug`, `info`, `warn`, `error` |
| `DBFORGE_LOG_FORMAT` | `json` for machine-readable daemon logs; text by default |
| `DBFORGE_SUPERVISE_INTERVAL` | How often to re-check instances (default `30s`) |
| `DBFORGE_NO_RESTORE` | Set to skip restoring instances on daemon startup |
| `DBFORGE_SCOPE` | Isolate this installation's containers from another on the same host |
| `DBFORGE_TUI_BIN` | Path to the TUI binary, if not beside `dbctl` |
| `DBFORGE_WAYBAR_SIGNAL` | SIGRTMIN offset for bar refreshes (default `8`, `0` disables) |

---

## Design decisions

### Podman, not Docker

Rootless by default, and no privileged always-on daemon — which suits Omarchy's
minimal-background-services philosophy. DBForge never needs root.

### Containers, not native per-version binaries

DBngin ships native binaries per version because that is tractable on macOS.
On Arch it means fighting pacman forever. Containers give genuine side-by-side
versions with no packaging work, at the cost of a runtime dependency.

### The Podman Go bindings moved

As of Podman 6 the module path is **`go.podman.io/podman/v6`**, not
`github.com/containers/podman/v5`. The old path still resolves on the proxy but
fails to build, with a confusing "module declares its path as" error. Everything
sits behind a `runtime.Runtime` interface, so swapping to raw REST calls or to
Docker means writing one file.

### Ports start at 15432, not 5432

Rootless containers *can* bind 5432 (`ip_unprivileged_port_start` is 1024 on
Arch), but defaulting there would collide with a natively installed engine. So
the default range is 15000-15999, and each engine starts its search at a port
whose digits echo the conventional one (15432 for Postgres, 15306 for MySQL) so
an instance's port stays guessable without ever occupying the real one.
`--port 5432` is there when you do want the conventional one.

Crucially, a port is only considered free once it has actually been **bound**.
"Not in our config" is not the same as "available" — another dev tool or a
stray container can hold a port DBForge knows nothing about.

---

## Safety model

Destroying a database is the one thing a tool like this can get catastrophically
wrong, so the destructive path is deliberately awkward:

- **`dbctl rm <id>` keeps your data.** It removes the container only. Recreating
  an instance against the same data directory brings the database back.
- **`dbctl rm <id> --wipe-data` deletes the data**, and requires you to type the
  instance name to confirm. Not a `y/N` — those get muscle-memoried.
- `--yes` skips confirmation for scripts, and is the only way to wipe
  non-interactively.
- The daemon refuses to wipe a data directory that is not an absolute path, as a
  guard against a corrupted config turning into an `rm -rf` on the wrong thing.
- A failed `create` rolls back: no orphaned container, no phantom entry marked
  `running`, and the port is released for reuse.
- Passwords are generated per instance (192 bits, base64url), stored in a
  `0600` file, and never printed except by `dbctl conn`.
- Instances bind `127.0.0.1` only, never `0.0.0.0`.

---

## Supported engines

| Engine | Image | Default port base | Data path in container |
|---|---|---|---|
| `postgres` | `docker.io/library/postgres` | 5433 | `/var/lib/postgresql/data` |
| `mysql` | `docker.io/library/mysql` | 3307 | `/var/lib/mysql` |
| `mariadb` | `docker.io/library/mariadb` | 3317 | `/var/lib/mysql` |
| `redis` | `docker.io/library/redis` | 6380 | `/data` |

Engines that initialise on first boot (Postgres's `initdb`, MySQL's setup) get
their initialisation environment only when the data directory is new. Passing
it again against a populated directory is at best ignored and at worst an
error, so first-boot versus subsequent-boot is modelled explicitly.

Adding an engine means one entry in `internal/engines/engines.go`.

---

## Development

```bash
make test              # unit tests, with -race
make vet               # both build tags
make build             # -> dist/dbforge, dist/dbforge-tui
make test-integration  # needs a live rootless Podman; pulls real images
make pkgbuild-check    # builds the AUR package and checks what it installs
```

### Layout

```
cmd/dbforge/        entry point; dispatches daemon vs CLI on argv[0]
cmd/dbforge-tui/    the TUI, kept out of the CLI binary on purpose
internal/model/     shared types; also the on-disk schema
internal/engines/   the engine catalogue
internal/ports/     port allocation, with real bind probing
internal/store/     instances.toml: atomic writes, external-edit detection
internal/runtime/   container engine interface + Podman impl + in-memory fake
internal/daemon/    Manager (all state) and the HTTP server
internal/cli/       dbctl
internal/registry/  engine version lists, cached for offline use
internal/tui/       the Bubble Tea interface
internal/notify/    signals the status bar when state changes
packaging/waybar/   module definition, styling, menu script, installer
packaging/aur/      PKGBUILD, pacman install hooks, and a checker that builds it
packaging/          systemd user unit template, per-user installer
```

The systemd unit is a template (`packaging/dbforged.service.in`): `ExecStart`
and `PATH` differ between a per-user install and a packaged one, so it is
rendered at install time rather than shipped twice and allowed to diverge.

### Build tags

The build always carries `containers_image_openpgp,exclude_graphdriver_btrfs`
(`GOTAGS` in the Makefile). Podman's bindings import gpgme and the btrfs graph
driver — neither of which this program executes — and both need C headers that
only a machine with podman's build dependencies has. The tags leave the module
buildable with `CGO_ENABLED=0`, which `TestBuildsWithoutCgo` checks.

Use `make` rather than bare `go` commands, or you will be testing a different
import graph from the one that ships.

### Data directories are owned by a subordinate UID

Under rootless Podman a container process running as a non-root user leaves
files owned by a *subuid* on the host: Postgres runs as container uid 999,
which appears as host uid 100998, not your 1000. You cannot `rm -rf` those
directories yourself, and neither can DBForge directly -- wiping goes through
`podman unshare`. This is why `Runtime.RemovePath` exists rather than a plain
`os.RemoveAll`.

The practical consequence: `ls ~/.local/share/dbforge/postgres/16/my-db` will
give you "Permission denied". That is expected. Use `dbctl rm <id> --wipe-data`
rather than deleting directories by hand.

### Scopes

Containers are found by label, so two DBForge daemons on one host would adopt
each other's instances. Each installation has a **scope** (`default` unless
`DBFORGE_SCOPE` says otherwise) stamped onto every container, and a daemon
ignores containers belonging to another scope. This is what lets the
integration tests run against real Podman without disturbing -- or being
disturbed by -- the instances you actually use.

### Testing approach

Everything above `runtime.Runtime` is tested against an in-memory fake, so the
whole lifecycle — including crash recovery and drift — runs without Podman.
Tests cover the cases where silent data loss hides: port double-assignment,
reconciliation, rollback on partial create, concurrent mutation of one
instance, and the keep-data-by-default guarantee.

Integration tests run against real rootless Podman, behind a build tag so the
default `go test` stays fast and dependency-free:

```bash
go test -tags integration -timeout 20m ./test/integration/
```

They cover the Postgres and Redis lifecycles, two instances of one engine side
by side, data surviving stop/start, wiping a subuid-owned data directory, drift
after an external `podman rm`, a nonexistent tag failing fast, restoration
after a daemon restart, restart policies, and the daemon surviving a
`podman.socket` restart. Roughly 60 seconds once images are cached; they skip
rather than fail when Podman is absent.

Suspend/resume cannot be automated. `packaging/verify-suspend.sh` does it in
two halves, around a real lid close:

```bash
./packaging/verify-suspend.sh before
# suspend, resume
./packaging/verify-suspend.sh after
```

---

## Troubleshooting

**`cannot reach dbforged`** — the daemon is not running:
```bash
systemctl --user status dbforged
systemctl --user start dbforged
```

**`connecting to podman ... connection refused`** — the Podman socket is not
enabled:
```bash
systemctl --user enable --now podman.socket
```

**An instance shows `exited(137)`** — it was killed rather than stopped
cleanly (usually OOM). With `restart=always` the daemon brings it back within
the supervision interval. For Postgres, expect crash recovery in `dbctl logs`.

**Databases stop when I log out** — user lingering is off:
`sudo loginctl enable-linger $USER`.

**An instance shows `missing(!)`** — its container was removed outside DBForge.
Your data is untouched. `dbctl rm <id>` forgets the entry; recreating with the
same name reattaches to the existing data directory.

**Postgres won't initialise** — it refuses to `initdb` into a non-empty
directory, so DBForge puts the cluster in a `pgdata` subdirectory of the mount.
If you bind an old directory, check `dbctl logs <id>` for Postgres's own error.

**Port conflicts** — DBForge proves a port free by binding it, so a conflict
means something really is listening. `ss -tlnp | grep <port>` will say what.

**The waybar widget shows nothing at all** — almost always PATH. waybar cannot
see `~/.local/bin`; check the config has absolute paths, and re-run
`./packaging/waybar/install.sh`.

**The widget does not update until I click it** — the daemon signals
`SIGRTMIN+8` only if it can find a process named `waybar`. Confirm with
`pgrep -x waybar`.

**`dbctl tui` says dbforge-tui is not found** — the TUI binary was not
installed alongside `dbctl`. Run `make install` again, or point
`DBFORGE_TUI_BIN` at it.

**Stopping Postgres leaves it `exited(137)`** — it was killed before it
finished shutting down. Each engine declares its own shutdown budget (60s for
the SQL engines), so this should not happen; if it does, the machine was
probably under heavy load.

---

## Roadmap

| Phase | Scope | Status |
|---|---|---|
| 0 | Feasibility spike on Omarchy | ✅ done |
| 1 | Daemon + CLI, lifecycle, ports, persistence, reconciliation | ✅ done, incl. integration tests |
| 2 | systemd unit, restart safety, restart policies, reboot recovery | ✅ done; suspend/resume needs a manual run |
| 3 | Bubble Tea TUI | ✅ done |
| 4 | waybar module | ✅ done |
| 4b | Quickshell module | ⬜ deferred (Omarchy 3.x is waybar-based) |
| 5 | AUR packaging, upgrade and migration safety | ✅ done; package built and checked, not yet published |
| 6 | `dbctl doctor`, structured logging, telemetry guarantee | ✅ done |

Full plan: [`docs/dbforge-omarchy-implementation-plan.md`](docs/dbforge-omarchy-implementation-plan.md).
Phase notes: [`docs/phase-0-findings.md`](docs/phase-0-findings.md),
[`docs/phase-2-notes.md`](docs/phase-2-notes.md),
[`docs/phase-3-notes.md`](docs/phase-3-notes.md),
[`docs/phase-4-notes.md`](docs/phase-4-notes.md),
[`docs/phase-5-notes.md`](docs/phase-5-notes.md),
[`docs/phase-6-notes.md`](docs/phase-6-notes.md).

---

## License

MIT
