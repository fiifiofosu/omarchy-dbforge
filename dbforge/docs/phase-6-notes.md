# Phase 6 — hardening and polish

What shipped: `dbctl doctor`, a log format switch, a telemetry guarantee that
is tested rather than asserted, and a branch and release workflow.

## `dbctl doctor`

Thirteen checks, in dependency order, over the things that actually break a
rootless setup. The order matters: the first failure in the list is the one
worth fixing, and checks that depend on a failed one report `skip` rather than
piling up derived failures. Someone whose podman is not installed should see
one problem, not four.

Three rules the checks follow:

- **A warning never fails the command.** Lingering being off, cgroup
  controllers not delegated, a port range filling up -- DBForge works in all
  of those. Failing on them would train people to ignore the output, and then
  the real failures go unread too.
- **Every non-ok check carries a fix.** A diagnosis without a fix is a
  complaint. There is a test that walks a deliberately broken machine and
  fails if any warning or failure has an empty `Fix`.
- **Nothing is mutated.** `doctor` is safe to run on a machine where nothing
  works, which is the only machine it will ever be run on.

`--json` for bug reports and scripts; a failure exits non-zero, and the CLI
suppresses its usual `error:` line for that case because the report above has
already said everything.

### Testing a broken machine

The whole `Env` is injected -- `LookPath`, `ReadFile`, `Stat`, the probes -- so
each test breaks exactly one thing on an otherwise healthy machine. That is the
only way to be sure a check reports on what it claims to and not on a side
effect. Reproducing "subuid range too short" or "cgroup controllers not
delegated" for real is otherwise impossible without wrecking the host.

The unit tests cover what each check *says*; an integration test covers whether
the probes work at all. A check that read the wrong cgroup path, or passed
`loginctl` a flag it does not have, would pass every unit test and be useless
in the field.

### Two bugs doctor found on its first run

Running it on this machine failed the systemd unit check:

```
[ FAIL ] systemd unit  ...dbforged.service runs %h/.local/bin/dbforged, which does not exist
```

`%h` is a systemd specifier, not a directory. The check was stat'ing the
ExecStart line literally, so it would have reported a false failure for anyone
with a specifier-based unit -- which is how DBForge's own unit was written
before Phase 5. Fixed by expanding `%h`, `%u`, `%U` and `%%` before the stat,
and leaving anything unrecognised alone so the failure direction stays safe.

Second, probing the log format with `DBFORGE_SOCKET` set showed the daemon
ignoring it while the CLI honoured it. The documented override half-worked:
`DBFORGE_SOCKET=x dbctl ls` talked to `x` while `DBFORGE_SOCKET=x dbforged`
listened on the default, and the only symptom was a connection refused.
`daemon.SocketPath()` now reads it, with tests on both branches.

## Structured logging

`slog` was already in place. What was missing was a way to get it out in a form
worth attaching to a bug report, so `DBFORGE_LOG_FORMAT=json` switches the
handler. Text stays the default because the usual reader is a person running
`journalctl --user -u dbforged`; JSON is for the other case, where filtering
by field beats skimming.

## Telemetry

The claim is "DBForge sends nothing anywhere", and it needed to be more than a
sentence in a README, because a dependency bump could quietly reverse it.

It cannot be tested as "OpenTelemetry is absent" -- podman's bindings
instrument their HTTP client with it, so 25 otel packages are in the graph
whatever we do. What makes that harmless is what is *not* there: no exporter,
and no SDK tracer provider. Without those, every span is recorded into a no-op
and nothing can leave the machine. `TestNoTelemetryIsLinked` pins exactly that,
along with the usual analytics SDKs.

Stating it precisely is the point. "No telemetry" would have been a claim that
a grep could embarrass.

## Branching and releases

`main` is now reached only through a pull request; work lands on `develop`.

- **CI** runs on pull requests into `main` and on pushes to `develop`. Gating
  only the PR would mean discovering at merge time that a week of commits is
  broken.
- **Release** runs on a `v*` tag. Tagging is the deliberate act; everything
  else is a consequence of it, so a release can be reproduced by re-running
  against the same tag.

The release workflow re-runs the full verification before building anything: a
tag that would not pass a pull request has no business becoming a release. It
then builds binaries and a source tarball, builds the Arch package in an
`archlinux:base-devel` container -- the only place `makepkg` means anything --
and checksums everything at the end, in the job that publishes, so the sums
cover exactly what is uploaded.

Two gates on the version string. A binary reporting `dev` was never stamped
with the tag; one reporting `-dirty` was built from a tree with uncommitted
changes, so the tag does not describe it. Either makes every bug report from
that release unattributable, which is worse than having no release. Both were
found by dry-running the build steps locally against a throwaway tag -- the
`-dirty` case actually occurred.

### The Arch job caught two things on its first run

Extracting the package build into a reusable workflow -- so pull requests
exercise it, not just releases -- paid for itself immediately.

The PKGBUILD's `check()` called bare `go test ./...`: the one go invocation in
the repository not routed through the Makefile, and so the one without
`GOTAGS`. It pulled in the btrfs graph driver, needing C headers a clean chroot
does not have. Exactly the Phase 5 CI failure, in the last place still doing it
by hand, and invisible locally because a developer's Arch box has those
headers.

`make test` could not be used directly either: Arch's Go packaging flags
include `-buildmode=pie`, which the race detector refuses to combine with.
Hence `make test-package` -- same tags, no `-race`, since CI has already run
the race build on the same commit.

`check.sh` then went green while uploading nothing. Its copy of the built
package into `dist/` was `cp ... 2>/dev/null || true`, and `dist/` does not
necessarily exist, since makepkg builds in its own temp directory. The script
could print "Package left in dist/" having left nothing there.

Both are now guarded closer to home: `check.sh` greps the PKGBUILD for direct
`go build`/`go test` calls, so the next one fails in a second rather than
seven minutes into a container build.

## Verified

| Behaviour | How |
|---|---|
| Each check reports correctly on a machine broken in one specific way | unit tests over an injected `Env`, one fault at a time |
| Every warning and failure offers a fix | a test that breaks five things and asserts no empty `Fix` |
| A failed dependency skips rather than cascades | missing podman produces one failure, not two |
| A dead daemon points at podman first when podman is down | unit test on both orderings |
| The probes work against a real host | integration test, all thirteen checks against this machine |
| systemd specifiers resolve | unit test plus the live run that found the bug |
| `DBFORGE_SOCKET` means the same thing to both ends | unit tests on override and fallback |
| Nothing ships telemetry | dependency assertion on exporters and SDK providers |
| Release build stamps a real version | dry-run against a throwaway tag; both gates exercised |
| The Arch package builds on clean Arch | reusable workflow, green on both PR and develop-push triggers |
| The PKGBUILD cannot drift off the Makefile | `check.sh` greps it for direct go invocations |
