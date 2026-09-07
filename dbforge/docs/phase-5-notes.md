# Phase 5 — packaging and distribution

What shipped: an AUR package (`dbforge-git`), pacman install hooks with an
opt-out, an uninstall path that leaves databases alone, and the two edge cases
the plan names — upgrading while instances run, and migrating a config written
by an older build.

## One unit, two install layouts

`make install` puts binaries in `~/.local/bin` and the unit in
`~/.config/systemd/user/`. The package puts them in `/usr/bin` and
`/usr/lib/systemd/user/`. The unit's `ExecStart` is an absolute path and its
`Environment=PATH` has to contain wherever `podman` and `dbforged` actually
are, so the same file cannot serve both.

Shipping two units would let them drift. Instead there is one template,
`packaging/dbforged.service.in`, with `@BINDIR@` and `@PATH@` substituted at
install time by whichever installer is running. `make pkgbuild-check` asserts
the built package's unit contains neither placeholder and points at
`/usr/bin/dbforged` — the failure mode being a package whose service silently
refuses to start because it points at a `~/.local/bin` that only the packager
had.

`@PATH@` is separate from `@BINDIR@` only because a packaged install would
otherwise render `PATH=/usr/bin:/usr/local/bin:/usr/bin`.

## Enabling a user service from a root package

pacman runs as root with no user session, so `post_install` cannot start
`dbforged` — it is a user service that needs the invoking user's Podman socket.
What it can do is `systemctl --global enable`, which links the unit into every
user's `default.target.wants` and takes effect at their next login. That is the
standard Arch mechanism and it is what the hook uses.

Opt-out is a marker file, `/etc/dbforge/no-autoenable`, checked on both
`post_install` and `post_upgrade`. An environment variable would not work: the
hook runs inside pacman's transaction, not in the shell that started it, and an
opt-out that silently stops applying on the next upgrade is worse than none.

`podman.socket` and `loginctl enable-linger` are printed, not performed.
Enabling lingering for a user is a system policy decision, and turning on a
socket the user may have deliberately masked is not a package's call.

## Upgrading with databases running

The plan's edge case: "daemon restart shouldn't stop/restart containers
unnecessarily; reconnect to existing containers on startup."

This already held — `KillMode=process` stops only the daemon, and startup
reconciliation adopts containers by label rather than recreating them — but
nothing pinned it. `TestUpgradeLeavesRunningInstancesAlone` now does: it writes
a key into a running Redis, replaces the daemon, and asserts the container's ID
and start time are unchanged, `Restore` started nothing, and the key is still
there. Anything that reintroduced a stop/start would fail on the start time
before it failed on the data.

## Config migration

`schema_version` was already written; nothing read it for anything but
rejection. Now:

- `Load` records the version that was on disk, before `migrate()` rewrites it
  in memory. Getting this order wrong makes the field useless — the first thing
  migration does is overwrite the evidence that migration was needed.
- The first `Save` after loading an older file copies the original to
  `instances.toml.v<N>.bak`, once, and never overwrites an existing backup. A
  daemon saves constantly; a backup taken on every save would within seconds be
  a copy of the migrated file, which is not a backup of anything.
- `Reconcile` forces a save when the loaded schema is older, even with no
  drift. Without this a config that reconciles cleanly is never rewritten: it
  stays on the old schema indefinitely while the daemon logs that it migrated,
  and the *next* release's migration starts from a version it no longer
  expects. This was caught by the integration test, not by reasoning — the unit
  tests exercise the store directly and never saw the manager's save condition.
- Reading a *newer* schema stays a hard error, now with the recovery path in
  the message. A downgrade must refuse rather than guess.

The v0→v1 migration itself is deliberately a no-op on instance fields. v0 files
lack `restart`, `desired` and `scope`, but the daemon's `withDefaults` already
infers those from the live container — better evidence than the store could
invent. The migration's only job is to stop a v0 file being mistaken for a v1
one.

## The build needed C headers and nobody noticed

CI failed on the first push of this phase, and not on anything Phase 5
touched:

```
Package gpgme was not found in the pkg-config search path.
fatal error: btrfs/version.h: No such file or directory
```

Podman's Go bindings pull in two cgo packages: `github.com/proglottis/gpgme`,
for verifying image signatures, and `go.podman.io/storage/drivers/btrfs`.
Neither is something we execute — we are an HTTP client of podman, not a
second copy of it — but both are in the import graph, and each needs C headers
that a stock CI runner does not have.

This had been true since Phase 0. It was invisible because every machine the
build had ever run on had podman installed, and so had the headers. The
standard tags fix it: `containers_image_openpgp` swaps gpgme for Go's own
OpenPGP, `exclude_graphdriver_btrfs` drops the driver. With both, the module
builds with cgo disabled entirely.

Two things follow from "invisible locally":

- `TestBuildsWithoutCgo` compiles the module with `CGO_ENABLED=0`. Dropping
  the tags fails it on any machine, rather than on someone else's.
- CI now runs `make` targets rather than its own `go` commands. Tags have to
  be identical everywhere or the import graph differs between what CI checks
  and what ships, and a workflow with its own copy of the commands is how they
  drift apart. `GOTAGS` in the Makefile is the single definition; the PKGBUILD
  gets them by calling `make`.

A third CI job now stages `make DESTDIR=... PREFIX=/usr install` and asserts on
the resulting tree. That catches a broken install layout on every push, without
needing an Arch runner — only building the real package does.

## Readiness budgets sized for a busy machine

One integration run failed during this phase and passed on every rerun. The
run took 107s, the shape of a wait timing out rather than an assertion
failing, and it happened while `make test -race` was recompiling the whole
module under the new tags -- load average near 4.

Postgres had a 90s budget to finish `initdb` and start serving. That is
comfortable on an idle laptop and marginal on a loaded one, which is the worst
kind of timeout to pick: it passes for whoever chose it and fails for everyone
else, intermittently. The budgets are now named constants sized for a cold
image pull plus first-run initialisation on a machine doing something else --
three minutes for Postgres, a minute for Redis. A CI runner is slower than
this laptop on both counts.

## Uninstall

Data lives in `~/.local/share/dbforge` and config in `~/.config/dbforge`,
neither of which pacman owns, so removal cannot touch them. `post_remove` says
so explicitly and prints where to find the containers and files, plus the fact
that `dbctl rm --wipe-data` has to be run *before* uninstalling, while the tool
that understands the layout is still present. Deleting the directory by hand
does not work anyway: the files are owned by a subuid the user cannot unlink.

## Nothing is published yet

The package is built and checked on every push; it is not on the AUR. It
clones this repository, which is private, so an upload would hand every user a
failure at `Retrieving sources...`. There are also no tags, so `pkgver()` falls
back to a commit count (`0.0.0.r6.g69fd029`) and `--version` reports the same.

Both resolve together: making the repository public and cutting the first
tagged release. That was deliberately held for Phase 6 rather than done here --
going public is not reversible, and a first release is worth making once, with
`dbctl doctor` in it, rather than twice.

The README says all of this where someone would otherwise copy a
`paru -S dbforge-git` line that cannot work.

## `dbforge-bin` deferred

The plan says "`dbforge-bin` or `dbforge-git`". `-git` is the one that can be
published today; `-bin` needs release tarballs with checksums that do not exist
yet. `make dist-tarball` produces one from a tag, and since packaging already
goes through `make DESTDIR=... PREFIX=/usr install`, the `-bin` PKGBUILD is a
short file whenever there is a release to point it at.

`.SRCINFO` is likewise not committed. It is generated from the PKGBUILD, so a
copy in this repo could only ever be a stale one; `make srcinfo` writes it at
publish time.

## Verified

| Behaviour | How |
|---|---|
| The build needs no C headers | `TestBuildsWithoutCgo`: `CGO_ENABLED=0` compile of the whole module |
| Package builds from a clean checkout | `makepkg` against a `file://` clone, unit tests run in `check()` |
| Installs every load-bearing path | `check.sh` asserts on binaries, symlinks, unit, waybar files, licence |
| `dbctl`/`dbforged` are symlinks to one binary | `bsdtar -tvf` on the built package |
| Version stamping survives packaging | `dbforge --version` from the extracted package reports the `pkgver` |
| Unit points at the packaged binary | `ExecStart=/usr/bin/dbforged`, no placeholders left |
| Upgrade does not disturb running instances | integration test on container ID, start time and live data |
| Install hooks enable, opt out, and disable correctly | `make hook-test`: stub `systemctl`, assert on the calls |
| The opt-out survives an upgrade | hook test asserts `post_upgrade` honours the marker |
| Removal message keeps naming the data | hook test greps `post_remove` for the paths |
| Old configs migrate and are backed up | unit tests on the store, integration test through the daemon |
| Backup survives repeated saves | unit test writing three times over a v0 file |
| Newer schema refuses rather than corrupts | unit test |
