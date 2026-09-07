# Phase 2 — persistence & restart safety

Verified on Omarchy 3.8.4 with Podman 6.0.1, rootless.

## What "survives a reboot" actually requires

Three separate things, and missing any one of them means the databases do not
come back:

1. **The daemon must start.** `dbforged.service` is a systemd user unit,
   `WantedBy=default.target`.
2. **The user manager must be running when nobody is logged in.** That is
   `loginctl enable-linger`. Without it the unit is enabled but the whole user
   session is torn down at logout, so nothing runs until someone logs back in.
   This is the step that is easy to miss, because everything looks correct
   until you actually log out.
3. **Something must start the containers.** Rootless Podman does not do this on
   boot by itself. The daemon does it, from recorded intent.

## Policy is not the same as intent

An instance has a *restart policy* (`always`, `on-failure`, `no`) and a
*desired state* (`running`, `stopped`). Both are needed:

- Policy alone cannot distinguish "the container died" from "the user stopped
  it". Restoring on policy alone would resurrect databases you deliberately
  turned off every time you rebooted.
- Desired state alone cannot express "bring this back if it crashes, but leave
  it alone if it exits cleanly".

Only an explicit `dbctl start` / `dbctl stop` changes desired state. A
container dying on its own does not, which is what makes crash recovery
distinguishable from a deliberate stop.

## Podman's own restart policy is deliberately unused

**Observed bug.** An instance created with `restart=always` and then stopped
with `dbctl stop` came back on its own. The daemon was correct -- it recorded
`desired = "stopped"` and skipped the instance during restore. Podman restarted
it independently, because the *container* carried `RestartPolicy=always` and
podman re-evaluates that when its service restarts:

```
$ podman inspect dbforge-p2-stopped --format '{{.HostConfig.RestartPolicy.Name}} {{.State.StartedAt}}'
always 2026-09-07 00:27:45 +0000 UTC     # exactly when podman.socket was bounced
```

The fix is that DBForge always creates containers with `RestartPolicy=no` and
enforces restarts itself. Podman has no access to desired state, so it cannot
make this decision correctly; having two controllers act on different
information guarantees they eventually disagree.

The cost is that a crashed container is not restarted instantly by podman. The
daemon covers that with a supervision loop (30s by default), which is slower
but correct.

## Scopes

Containers are discovered by label, so a second daemon -- a test run, a
throwaway config -- adopted the real installation's instances. Every container
now carries `io.dbforge.scope`, and a daemon ignores foreign scopes. Containers
with no scope label predate this and are still adopted, so upgrading does not
orphan anything.

This surfaced as an integration test that passed alone and failed in a suite:
the test daemon was adopting instances created by hand minutes earlier.

## Verified behaviours

| Behaviour | How |
|---|---|
| `restart=always` instance returns after a daemon restart | Real containers, `systemctl --user restart dbforged` |
| A `dbctl stop` survives a reboot | Same, plus a `podman.socket` bounce |
| `restart=no` is never auto-started | Real containers |
| `on-failure` restarts only after an unclean exit | Real `SIGKILL`, exit 137 |
| Unclean exit is surfaced, not hidden | `dbctl list` shows `exited(137)` and warns about crash recovery |
| A crash is repaired while the host is up | Supervision loop, verified at a 3s interval |
| The daemon survives the podman socket disappearing | `systemctl --user restart podman.socket` |
| Hand-edited config is not clobbered | Unit test, digest-based detection |
| Concurrent `dbctl` calls on one instance serialise | Unit test under `-race` |

## Not verified here

**Suspend/resume.** It needs a real lid close, so it is a manual check:
`packaging/verify-suspend.sh before`, suspend, resume,
`packaging/verify-suspend.sh after`. The script's logic is exercised (both
halves run and pass without a suspend); what is unverified is the behaviour of
podman and netavark across an actual suspend.

**Reboot.** The daemon restart path is verified, and it is the same code path a
boot takes, but a genuine power cycle with lingering enabled has not been run.
