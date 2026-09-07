# Phase 0 — feasibility findings

Environment audited: Omarchy 3.8.4, kernel 7.1.4-arch1-1.

## Rootless Podman prerequisites

| Check | Result |
|---|---|
| `/etc/subuid`, `/etc/subgid` | `dv:100000:65536` — present |
| `newuidmap` / `newgidmap` | `cap_setuid=ep` / `cap_setgid=ep`. Arch uses **file capabilities**, not the setuid bit; a `ls -l` check for `s` gives a false negative |
| cgroups | v2 (`cgroup2fs`), with `cpu memory pids` delegated to the user slice |
| `max_user_namespaces` | 62045 |
| LSM | `capability,landlock,lockdown,yama,bpf` — **no SELinux, no AppArmor** |

No SELinux means bind mounts need no `:Z` relabelling, which removes a class of
error the plan anticipated.

`cpu` being delegated matters: the per-instance CPU caps in §8 of the plan work
rootless. On systems where only `memory pids` are delegated they would not.

## Firewall / localhost binding

The plan flagged this as "confirm before designing around it". Confirmed
empirically rather than by reading rules:

- ufw is **active**.
- Binding and connecting `127.0.0.1:5433` — works.
- Binding `0.0.0.0:5434`, connecting via loopback — works.

**Conclusion: no design change needed.** ufw's default `allow in on lo` leaves
loopback alone, and DBForge only ever binds loopback. Worth re-checking in
`dbctl doctor` rather than assuming, since a custom ufw policy could break it.

## Port floor

`net.ipv4.ip_unprivileged_port_start = 1024`, so rootless containers *can* bind
5432 / 3306 / 6379 directly. Ports are still allocated from a high range by
default to avoid displacing a natively installed engine, with `--port` for
pinning a conventional one.

## Docker coexistence

Docker 29.6.2 is installed and running on this machine. `pacman -Si podman`
declares no `Conflicts` or `Provides` against it, so the two coexist. This is
the harder row of the plan's §9 test matrix and it is the default environment
here.

## Corrections to the plan

1. **The Podman Go bindings module path changed.** §4 names
   `github.com/containers/podman/v5/pkg/bindings`. Arch ships Podman 6.0.1, and
   as of v6 the module is **`go.podman.io/podman/v6`**. The old path still
   resolves on the module proxy but fails with:

   ```
   module declares its path as: go.podman.io/podman/v6
           but was required as: github.com/containers/podman/v6
   ```

2. **The bindings build cleanly** — no cgo trouble, ~267 dependencies, 11 MB
   binary. The plan's fallback (raw REST over the socket) is not needed, though
   `runtime.Runtime` keeps it one file away.

3. **waybar-first is confirmed correct.** Omarchy 3.8.4 is waybar-era; the
   Quickshell 4.0 path is additive, as §10 recommended.

## Spike results (verified against Podman 6.0.1)

Rootless Podman confirmed working: `runc`, native `overlay` storage (not
fuse-overlayfs), `netavark`, cgroups v2 with the systemd manager. UID mapping
is `container 0 -> host 1000`, then `container 1..65536 -> host 100000+`.

| Question | Result |
|---|---|
| Postgres 16 and Redis 7 side by side | Both run concurrently, distinct ports and data directories |
| Reachable from the host with ufw active | Yes, both, over loopback |
| Data survives stop/start | Yes, verified by writing through the published port and reading back after a restart |
| Bind-mount ownership under rootless | **Trap found** -- see below |
| Nonexistent tag fails fast | Yes, ~1.8s, before any container or data directory is created |

### The bind-mount ownership trap

A container process running as a non-root user leaves files on the host owned
by a *subordinate* UID. Postgres runs as container uid 999, which lands as host
uid 100998 -- not 1000. The host user cannot read, traverse or unlink those
files:

```
$ stat -c %u ~/.local/share/dbforge/postgres/16/app-db
100998
$ rm -rf ~/.local/share/dbforge/postgres/16/app-db
rm: cannot remove '...': Permission denied
```

This broke `--wipe-data`, which used `os.RemoveAll`. Deletion has to happen
inside the user namespace instead, via `podman unshare rm -rf`. That is now
`Runtime.RemovePath`, and it is the one place DBForge shells out rather than
using the bindings -- `unshare` re-executes a process in a namespace, which is
not something the API can express.

### The Postgres PGDATA trap

The original design put `PGDATA` in a `pgdata/` subdirectory of the bind mount,
on the theory that Postgres refuses to `initdb` into a non-empty directory.
That fails under rootless Podman:

```
mkdir: cannot create directory '/var/lib/postgresql/data': Permission denied
```

The image's `docker_create_db_directories` runs as root, creates `$PGDATA` and
chowns **`$PGDATA` and its contents** to the `postgres` user -- but not the
parent. It then re-execs as `postgres` and runs the same function again. With
`PGDATA` in a subdirectory, the mount itself stays `root:root 0700`, so the
`postgres` user cannot traverse into it; `mkdir -p` fails to stat the parent,
assumes it is missing, and reports that path.

Mounting directly at the image's default `PGDATA` lets the chown land on the
mount itself. The non-empty-directory concern does not apply, because DBForge
always creates a fresh dedicated directory rather than mounting a filesystem
root.

### Still open

- Suspend/resume across a laptop lid close (plan 7, phase 2). Needs a real
  suspend cycle; not exercised here.
- Behaviour on a genuinely full disk. The rollback path is unit-tested with a
  simulated failure, but not against real ENOSPC.
