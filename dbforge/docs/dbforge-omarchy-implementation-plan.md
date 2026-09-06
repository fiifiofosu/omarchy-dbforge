# DBForge — A Local Database Manager for Omarchy

*Working title. Rename freely — referred to as "DBForge" throughout this doc for consistency.*

## 1. Problem Statement

Omarchy developers need a fast way to spin up, switch between, and tear down local database engines (Postgres, MySQL/MariaDB, Redis, etc.) at arbitrary versions, without polluting the system package manager or fighting Arch's "one version per package" model. DBngin solves this on macOS with a menu-bar app that manages native per-version binaries. That mechanism doesn't map to Arch: side-by-side native versions aren't how pacman/AUR work, and Omarchy's UX is keyboard-first and terminal-native, not menu-bar-first.

## 2. Goals

- Create, start, stop, and destroy local database instances at a chosen engine + version in seconds.
- Support running multiple instances of the same engine (different versions or same version, different databases) concurrently without port or data collisions.
- Persist data across restarts; make destroying an instance's *container* safe without losing data unless the user explicitly wipes it.
- Surface instance status at a glance from the Omarchy shell (waybar or Quickshell), with deeper control via a TUI.
- Install and update like any other Omarchy tool: a single AUR package, no separate runtime the user has to babysit.

## 3. Non-Goals (v1)

- Cloud/remote database management — local only.
- A full GUI database client (table browsing, query editor) — DBForge manages *instances*, not data. Users still connect with DBeaver, TablePlus, psql, etc.
- Automatic backup/replication tooling — out of scope until core lifecycle management is solid.
- Windows/macOS support.

## 4. Language & Stack Decision

**Go**, for the core daemon, CLI, and TUI.

Rationale: Podman's official client bindings (`github.com/containers/podman/v5/pkg/bindings`) are Go-native and the most mature way to control Podman programmatically — Rust options here are thinner community crates or raw REST calls against the socket. Since the daemon's core job is reliably talking to Podman, this removes a major source of early friction. Go's concurrency model (goroutines/channels) also maps well onto the daemon's actual workload (concurrent requests from the TUI, CLI, and widget while managing multiple containers), and for a first systems-language project, Go's learning curve is considerably gentler than Rust's ownership model — closer to what's already familiar from TypeScript/Node. Bubble Tea (Go) is a mature, well-documented TUI framework, directly covering Phase 3.

## 5. Architecture

Three layers, one source of truth:

```
┌─────────────────┐   ┌──────────────────┐   ┌───────────────────────┐
│ waybar/Quickshell│   │       TUI         │   │        dbctl (CLI)    │
│  status widget   │   │ (Bubble Tea, Go)  │   │  scriptable commands  │
│  (read + quick   │   │                   │   │                        │
│   actions)       │   │                   │   │                        │
└────────┬─────────┘   └────────┬──────────┘   └───────────┬────────────┘
         │                      │                            │
         └──────────────┬───────┴────────────────────────────┘
                         │  Unix socket (JSON-RPC or gRPC)
                 ┌───────▼────────┐
                 │   dbforged      │  daemon — owns all state
                 │  (systemd user  │
                 │     service)    │
                 └───────┬────────┘
                         │  Podman API (rootless)
                 ┌───────▼────────┐
                 │ Container per   │
                 │ engine+version  │
                 │ +instance       │
                 └─────────────────┘
```

**Why a daemon instead of each frontend talking to Podman directly:** avoids race conditions when the TUI and widget both act at once, gives one place to enforce port allocation, and lets the widget stay lightweight (it only needs to read state, not manage container lifecycles itself).

**Why Podman over Docker:** rootless by default, no long-running privileged daemon fighting Omarchy's minimal-background-services philosophy, and drop-in CLI/API compatibility if a user already has Docker habits.

### Component choices

| Component | Language/Tool | Rationale |
|---|---|---|
| `dbforged` | Go | Official Podman client bindings are Go-native; single static binary; easy AUR packaging |
| `dbctl` | Same binary, subcommand mode | Scriptability; also what the TUI shells out to or RPCs into |
| TUI | Bubble Tea (Go) | Mature, well-documented; idiomatic for the k9s/lazydocker category |
| Status widget | waybar custom module (shell script + JSON) now; Quickshell/QML module later | waybar is universal across current Omarchy installs; Quickshell is the newer 4.0 shell — support both if targeting the current install base |

## 6. Data Model

```toml
# ~/.config/dbforge/instances.toml
[[instance]]
id = "pg16-app"
engine = "postgres"
version = "16"
port = 5433
data_dir = "~/.local/share/dbforge/postgres/16/pg16-app"
env = { POSTGRES_PASSWORD = "..." }   # generated, stored, never echoed by default
status = "running"                     # derived at read time, not persisted as truth
created_at = "2026-09-06T10:00:00Z"
```

State the daemon must always be able to reconstruct from Podman directly (container labels), not just from this file — the file is a cache/index, Podman is ground truth. This matters for edge cases below.

## 7. Implementation Phases

### Phase 0 — Spike / feasibility (2–4 days)
- Confirm rootless Podman works cleanly under a default Omarchy install (SELinux/AppArmor profile, cgroups v2 delegation for user services, storage driver defaults).
- Manually run one Postgres and one Redis container side by side, hit both from the host, confirm data survives `podman stop`/`start`.
- **Edge case to test now, not later:** does Omarchy's default firewall (if any) or systemd-resolved config interfere with binding to localhost ports? Confirm before designing around it.

### Phase 1 — Core daemon + CLI (engine lifecycle)
- `dbforged`: create/start/stop/remove instance, backed by Podman.
- `dbctl create postgres:16 --name pg16-app`, `dbctl start/stop/rm <id>`, `dbctl list`, `dbctl logs <id>`.
- Port allocation: pick a free port in a configurable range, record it, refuse to double-assign.
- Data directory management: bind-mount per instance, created with correct ownership for rootless Podman (subuid/subgid mapping).
- Config persistence in `~/.config/dbforge/instances.toml`, state reconciliation against live Podman containers on every daemon start.

**Edge cases for this phase:**
- Daemon crashes mid-operation (e.g., mid-create): must not leave an orphaned container untracked, or a tracked instance with no container. Reconciliation pass on startup should adopt or prune.
- Port already in use by something outside DBForge's knowledge (another dev tool, a stray container). Detect via actual bind attempt, not just "not in our config."
- User manually runs `podman rm` on a DBForge-managed container outside the tool. Next `dbctl list` should detect drift and mark the instance `missing`, not crash or silently recreate it.
- Disk full during data directory creation or container start — surface a clear error, don't leave a half-initialized instance marked `running`.
- Two instances requested with the same `id`/name — reject with a clear conflict error before touching Podman.
- Rootless Podman storage quota / subuid range exhaustion on machines with many instances — detect and surface, don't fail silently.
- Version tag doesn't exist upstream (typo'd `postgres:99`) — validate against the registry before creating, with a fast, clear error.

### Phase 2 — Persistence & restart safety
- systemd user service for `dbforged` (`~/.config/systemd/user/dbforged.service`), enabled by default so it survives login/logout cycles correctly.
- On host reboot: instances marked `running` before shutdown should offer (or auto-) restart, matching the container's `restart` policy the user chose at creation.
- Data integrity: for engines like Postgres, an unclean container stop (host crash, OOM kill) can leave the data directory in a state needing recovery — surface the engine's own crash-recovery behavior in logs rather than masking it.

**Edge cases:**
- User deletes an instance's config entry by hand-editing the TOML while the daemon is running — daemon should detect the file changed externally and reconcile rather than silently overwriting on next write.
- Simultaneous `dbctl` invocations (two terminals) trying to mutate the same instance — needs locking at the daemon (single-writer queue per instance id).
- Host suspends/resumes (laptop lid close) — containers should survive; verify Podman's behavior here specifically, it's a common laptop-dev gap.

### Phase 3 — TUI
- List view: engine, version, port, status, uptime, at-a-glance.
- Actions: start/stop/restart/destroy, view logs (tailing), create new instance via a guided form (engine → version list fetched/cached → name → port override).
- Connection info panel: ready-to-copy connection string per instance.

**Edge cases:**
- Long-running log tail shouldn't block the rest of the TUI or leak file descriptors if the user backs out mid-tail.
- Creating an instance while offline (no registry access to check available versions) — fall back to a cached version list, warn clearly that it's cached.
- Destroy action needs a distinct confirmation for "remove container, keep data" vs "remove container and data" — this is the single most consequential foot-gun DBngin-style tools have; make the destructive path require typing the instance name or an explicit `--wipe-data` flag, never a bare "y/N".

### Phase 4 — Status widget (waybar first, Quickshell second)
- waybar custom module polling `dbctl list --json` on an interval or via a signal from the daemon (prefer signal-driven over polling to avoid battery/CPU churn).
- Click actions: left-click opens TUI in a terminal, right-click gives a quick start/stop menu (via walker or a small native menu).
- Quickshell module as a follow-up once the core is stable, for installs on the 4.0+ shell.

**Edge cases:**
- Daemon not running when the widget queries it — show a distinct "daemon offline" state, not zero instances (those look identical otherwise and are misleading).
- Very many instances — widget should summarize (count + any in error state) rather than trying to render a full list inline.

### Phase 5 — Packaging & distribution
- AUR package (`dbforge-bin` or `dbforge-git`) bundling the daemon/CLI/TUI binary plus the waybar module script and a systemd user unit.
- Post-install hook enabling the systemd user service (with clear opt-out).
- Uninstall path: package removal should not silently delete instance data — data lives under `~/.local/share/dbforge`, outside the package's purview, and this should be documented explicitly.

**Edge cases:**
- Upgrading DBForge itself while instances are running — daemon restart shouldn't stop/restart containers unnecessarily; reconnect to existing containers on startup.
- Config schema changes between versions — need a migration path (versioned config file) so an upgrade doesn't corrupt `instances.toml`.

### Phase 6 — Hardening / polish (ongoing)
- Structured logging for the daemon (for debugging "why won't my instance start" reports).
- `dbctl doctor` command: checks Podman is installed/rootless-configured correctly, subuid/subgid ranges are sane, port range isn't fully exhausted — this single command will absorb most support burden.
- Telemetry-free by default; if any usage stats are ever added, opt-in only (matches Omarchy's ethos and avoids a trust hit).

## 8. Cross-Cutting Edge Cases

- **Engine-specific first-run behavior:** Postgres needs an initdb step and a password set via env on first container start only; MySQL similarly initializes on first run. The daemon must know each engine's "first boot vs subsequent boot" distinction so restarts don't accidentally re-trigger initialization.
- **Version upgrades of an existing instance:** if a user wants to move an instance from Postgres 15 to 16, that's not a version bump on the same container — it needs an explicit dump/restore flow (or an explicit refusal with guidance) rather than silently swapping images against incompatible on-disk data.
- **Resource limits:** no default CPU/memory caps means a runaway instance can starve the host. Sensible defaults per engine, overridable per instance.
- **Clock/timezone consistency:** containers should inherit host timezone for log timestamp sanity, easy to overlook.
- **Uninstall vs "remove all instances":** two very different user intents that must have two very different, clearly labeled commands.

## 9. Testing Strategy

- Unit tests around port allocation, config reconciliation, and the drift-detection logic (Phase 1) — these are where silent data loss bugs hide.
- Integration tests spinning up real rootless Podman containers in CI (GitHub Actions supports this with some setup) for at least Postgres, MySQL, Redis.
- Manual test matrix: fresh Omarchy install, install with existing Docker (not Podman) present, laptop suspend/resume, low-disk-space scenario.

## 10. Open Questions to Resolve Before Phase 1

- Target the current stable Omarchy install base (waybar) or design Quickshell-first given where the project is heading — recommend waybar first since it covers today's installs, Quickshell as an additive Phase 4b.
- Default port ranges per engine, and whether to allow the user to pin a specific port outside that range at creation time.

## 11. Learning Path Note

Since Go is new, Phase 0/1 doubles as the learning vehicle: start with the Podman bindings and basic CLI plumbing (closest to familiar imperative code), then goroutines/channels once the daemon needs to handle concurrent requests (Phase 1–2), then Bubble Tea's model-update-view pattern for the TUI (Phase 3) — each phase introduces roughly one new Go concept rather than all at once.
