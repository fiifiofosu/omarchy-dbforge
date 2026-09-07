# DBForge

Spin up local database instances on Omarchy in seconds — any engine, any
version, side by side, without touching pacman.

DBForge is what DBngin is on macOS, rebuilt for how Arch actually works. Rather
than juggling per-version native binaries (which pacman's one-version-per-
package model fights), every instance is a rootless Podman container with its
own port and its own data directory. Creating a Postgres 16 alongside a
Postgres 15 alongside a Redis 7 is three commands and no conflicts.

> **Status: Phase 1 complete.** The daemon, CLI, port allocation, data-directory
> management, config persistence and drift reconciliation are implemented, unit
> tested, and verified end to end against real rootless Podman containers. The
> TUI (Phase 3) and waybar widget (Phase 4) are not built yet.
> See [Roadmap](#roadmap).

---

## Table of contents

- [Why this exists](#why-this-exists)
- [Install](#install)
- [Quick start](#quick-start)
- [Commands](#commands)
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

```bash
git clone https://github.com/fiifiofosu/dbforge
cd dbforge
make install
```

That installs a single binary under two names (`dbforged`, `dbctl`) into
`~/.local/bin`, plus a systemd user unit. Then:

```bash
systemctl --user enable --now podman.socket
systemctl --user enable --now dbforged
```

### A note on PATH

The systemd user manager does **not** have `~/.local/bin` on its `PATH` by
default, even though your shell does — `~/.bashrc` is never sourced for user
services. The shipped unit sets `PATH` explicitly for this reason. If you
invoke `dbctl` from a Hyprland keybind or a `.desktop` file, use the absolute
path for the same reason.

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

---

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
| `~/.config/systemd/user/dbforged.service` | The user service |

Data deliberately lives outside anything a package manager owns, so
uninstalling DBForge never deletes a database.

### Environment variables

| Variable | Purpose |
|---|---|
| `DBFORGE_SOCKET` | Override the daemon socket path |
| `DBFORGE_PODMAN_SOCKET` | Override the Podman socket |
| `DBFORGE_DATA_ROOT` | Override where instance data is stored |
| `DBFORGE_LOG_LEVEL` | `debug`, `info`, `warn`, `error` |

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
make test     # unit tests
make vet
make build    # -> dist/dbforge
go test -race ./...
```

### Layout

```
cmd/dbforge/        entry point; dispatches daemon vs CLI on argv[0]
internal/model/     shared types; also the on-disk schema
internal/engines/   the engine catalogue
internal/ports/     port allocation, with real bind probing
internal/store/     instances.toml: atomic writes, external-edit detection
internal/runtime/   container engine interface + Podman impl + in-memory fake
internal/daemon/    Manager (all state) and the HTTP server
internal/cli/       dbctl
packaging/          systemd user unit
```

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
after an external `podman rm`, and a nonexistent tag failing fast. Roughly 50
seconds once images are cached; they skip rather than fail when Podman is
absent.

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

**An instance shows `missing(!)`** — its container was removed outside DBForge.
Your data is untouched. `dbctl rm <id>` forgets the entry; recreating with the
same name reattaches to the existing data directory.

**Postgres won't initialise** — it refuses to `initdb` into a non-empty
directory, so DBForge puts the cluster in a `pgdata` subdirectory of the mount.
If you bind an old directory, check `dbctl logs <id>` for Postgres's own error.

**Port conflicts** — DBForge proves a port free by binding it, so a conflict
means something really is listening. `ss -tlnp | grep <port>` will say what.

---

## Roadmap

| Phase | Scope | Status |
|---|---|---|
| 0 | Feasibility spike on Omarchy | ✅ done |
| 1 | Daemon + CLI, lifecycle, ports, persistence, reconciliation | ✅ done, incl. integration tests |
| 2 | systemd unit, restart safety, suspend/resume | 🚧 unit shipped; restart policy and suspend/resume pending |
| 3 | Bubble Tea TUI | ⬜ not started |
| 4 | waybar module, then Quickshell | ⬜ not started |
| 5 | AUR packaging | ⬜ not started |
| 6 | `dbctl doctor`, structured logging | ⬜ partial (logging done) |

Full plan: [`docs/dbforge-omarchy-implementation-plan.md`](docs/dbforge-omarchy-implementation-plan.md).

---

## License

MIT
