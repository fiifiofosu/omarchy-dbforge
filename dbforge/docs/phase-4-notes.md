# Phase 4 — the status bar widget

waybar, verified against the running bar on Omarchy 3.8.4.

## The bug that would have made this ship broken

waybar is started by systemd. The systemd user manager's `PATH` does not
include `~/.local/bin`, because user services never source `~/.bashrc`:

```
$ tr '\0' '\n' < /proc/$(pgrep -x waybar)/environ | grep ^PATH
PATH=/home/dv/.local/share/mise/shims:/home/dv/.local/share/omarchy/bin:...
```

A module whose `exec` is `dbctl status --waybar` therefore runs nothing, and
waybar renders an **empty widget with no error anywhere** — not in the bar, not
in the journal. It simply looks like the feature does not work.

The installer now resolves `dbctl`, `dbforge-tui` and `dbforge-waybar-menu` at
install time and writes absolute paths. The menu script additionally prepends
its own directory to `PATH`, since it shells out to `dbctl` itself.

Verified by running the module with waybar's exact environment:

```
$ env -i PATH="$WAYBAR_PATH" HOME=$HOME sh -c '/home/dv/.local/bin/dbctl status --waybar'
{"text":"󰆼","alt":"empty","tooltip":"No database instances...","class":"empty"}
```

## Signal-driven updates

The plan asks for signals over polling, to avoid waking the CPU for a value
that changes a few times a day. waybar refreshes a custom module on
`SIGRTMIN+N`; the daemon sends that whenever it persists a state change.

Two details worth recording:

**`SIGRTMIN` is 34, not 32.** The kernel's first real-time signal is 32, but
glibc reserves 32 and 33 for its threading implementation and exposes
`SIGRTMIN` as 34. waybar is glibc-linked, so it computes 34+N. Go's `syscall`
package does not export the constant at all, so it is defined explicitly with
a test pinning the value — getting it wrong sends a signal that either does
nothing or kills the bar.

**The hook goes in `save()`, not at each call site.** Every state change ends
in a persist, so hooking there covers create, start, stop, restart, remove and
reconciliation at once, and cannot be forgotten when a new action is added.
Signals are debounced by 250ms because a create touches state several times in
a second and the bar only needs telling once.

Verified end to end with a stand-in process: `comm` is taken from the
executable name, so a copy of `python3` named `waybar` is indistinguishable
from the real bar to the `/proc` scan. Three state changes produced four
signals, all `42` = `SIGRTMIN+8`, confirming both discovery and debouncing.

## Offline is not the same as empty

The plan calls this out, and it is the widget's main design constraint. "The
daemon is not running" and "you have no instances" are both an absence of
instances; rendered naively they are the same widget. They get different
icons, different CSS classes and different tooltips, and `dbctl status
--waybar` still exits 0 and emits valid JSON when the daemon is unreachable —
a non-zero exit would make waybar render nothing, which is exactly the
confusion being avoided.

## Many instances

The tooltip caps at 12 rows and appends a count of the remainder. Rows are
sorted by severity first, so a missing or crashed instance is never the one
truncated away, and the widget text switches to the problem count rather than
the running count when anything is wrong — a degraded instance should pull the
eye rather than be averaged into a healthy-looking number.

## Menu formatting lives in Go

The right-click menu was first written as `dbctl list --json` piped into
`python3 -c '...'` inside a bash script. Threading quotes through bash into
python into JSON produced a syntax error on the first run (`\"` is not valid
Python inside a single-quoted shell string). It is now `dbctl status --menu`,
formatted in Go, with tests asserting the columns stay where the script's
`awk` expects them.

## Quickshell (4b) deferred

Omarchy 3.8.4 is waybar-based, which the plan's own open question recommends
targeting first. A Quickshell module is additive and needs a Quickshell install
to test against; shipping untested QML would be worse than shipping nothing.
The daemon side is already done — the signal and `dbctl status --waybar` are
shell-agnostic, so 4b is a QML file and nothing more.

## An open port is not a ready database

Verifying Phase 4 turned the integration suite red in a way that had nothing to
do with Phase 4: Postgres reported "data directory is empty after initdb",
Redis reset the connection right after accepting it, and each test passed on
its own. The suspicion was port reuse across tests, but giving every manager a
disjoint port block changed nothing.

The cause was `waitForPort`, which only dialled the port. Under rootless
Podman the `rootlessport` forwarder binds the host port as soon as the
container is created, long before the database inside it is serving. So the
wait returned immediately, and the test then read a data directory that
`initdb` had not filled yet, or spoke to a Redis that was not listening. The
per-test timing differences (page cache, image already pulled) decided whether
it passed.

Waiting now means speaking the protocol: Redis must answer `PING` with `PONG`,
Postgres must answer an `SSLRequest` packet. The suite went from ~89s failing
to ~20s green, and stays green under `-shuffle=on`.

The disjoint port blocks and the `TestMain` purge of `test-` scoped containers
were kept anyway. Neither was the bug, but both remove real ways for one test
to break the next.

## Verified

| Behaviour | How |
|---|---|
| Widget renders under waybar's own PATH | `env -i` with waybar's environment |
| Signal reaches the bar | stand-in process named `waybar`, caught signal 42 |
| Debouncing | 3 state changes produced 4 signals, not dozens |
| Offline vs empty differ | unit tests plus live daemon stop |
| Right-click start/stop | stub picker under waybar's PATH; stopped then started a real instance |
| Right-click with daemon down | offered and performed `systemctl --user start dbforged` |
| Installer is idempotent | second run declines, config still valid JSONC |
| Widget latency | integration test fails above 2s, since waybar runs it synchronously |
| Full suite is order-independent | `go test -tags integration -shuffle=on`, run repeatedly |
