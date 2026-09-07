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

## Uninstall

Data lives in `~/.local/share/dbforge` and config in `~/.config/dbforge`,
neither of which pacman owns, so removal cannot touch them. `post_remove` says
so explicitly and prints where to find the containers and files, plus the fact
that `dbctl rm --wipe-data` has to be run *before* uninstalling, while the tool
that understands the layout is still present. Deleting the directory by hand
does not work anyway: the files are owned by a subuid the user cannot unlink.

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
