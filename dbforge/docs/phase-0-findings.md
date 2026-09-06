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

## Not yet verified

Podman was not installable at spike time: the Omarchy stable mirror 404s on the
versions in the local pacman DB, which was ~7 weeks stale. These remain open:

- Postgres and Redis running side by side, both reachable from the host.
- Data surviving `podman stop` / `podman start`.
- Bind-mount ownership under rootless UID mapping.
- Suspend/resume behaviour (laptop lid close) — plan §7 phase 2.
