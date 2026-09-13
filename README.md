# DBForge

Local database instances on Omarchy — any engine, any version, side by side —
and a bar widget to drive them. See what is running, start and stop it, and
copy a connection string, without leaving the bar.

The widget is a front end. The thing it drives is **DBForge**, a daemon that
runs local Postgres, MySQL and Redis instances as rootless Podman containers,
each with its own port and its own data directory — so Postgres 16 next to
Postgres 15 next to Redis 7 is three commands and no conflicts.

**Both are in this repository.** The widget is the QML at the root; DBForge is
the Go under [`dbforge/`](dbforge). Installing the widget does not install
DBForge — see [Install](#install) — but you never have to go anywhere else to
get it.

If you run waybar rather than the Omarchy 4.0 shell, DBForge ships a waybar
module of its own ([`dbforge/packaging/waybar`](dbforge/packaging/waybar)) —
use that instead. The two are alternatives, not companions.

## Layout

```
manifest.json  Panel.qml  Service.qml  Model.js  DbForgeIcon.qml
                    the Omarchy bar widget — must be at the repository root,
                    because Omarchy reads a plugin's manifest from the root of
                    the repo it clones
dbforge/            the daemon, CLI and TUI the widget drives (Go)
```

## Requirements

- Omarchy 4.0 or newer (`omarchy plugin` and the Quickshell-based bar)
- DBForge built and installed from [`dbforge/`](dbforge) — Go 1.26+ and
  rootless Podman
- `wl-copy` (`wl-clipboard`), for copying connection strings

### External commands

Plugins run unsandboxed inside the long-lived shell process, so here is
everything this one executes, and why. It runs no network requests of its own
and writes nothing outside the commands below.

| Command | When | Why |
|---|---|---|
| `sh` | on load, and to copy | Resolves the binaries below; pipes `dbctl conn` into `wl-copy` without letting an instance id reach a shell as text |
| `dbctl list --json` | every refresh | Reads instance state |
| `dbctl start` / `stop <id>` | the row switch | The only state this widget changes |
| `dbctl conn <id>` | copy action | Produces the connection string |
| `wl-copy` | copy action | Puts it on the clipboard |
| `systemctl --user start dbforged` | the offline row | Starts the daemon, when you ask it to |
| `omarchy-launch-tui`, or `uwsm-app` / `xdg-terminal-exec` | terminal action | Opens the DBForge TUI |

Connection strings contain generated passwords. They go to the clipboard only
when you ask for them, and are never written to a file or logged.

## Install

Two halves, installed separately. DBForge first, or the widget will have
nothing to talk to:

```bash
git clone https://github.com/fiifiofosu/omarchy-dbforge
cd omarchy-dbforge/dbforge
make install && ./packaging/install.sh
```

That builds `dbforge`, `dbctl` and `dbforge-tui` into `~/.local/bin`, installs
the systemd user unit, and enables the daemon. It needs Go 1.26+ and rootless
Podman; `dbforge/README.md` covers the requirements and `dbctl doctor` checks
them.

Then the widget:

```bash
omarchy plugin add https://github.com/fiifiofosu/omarchy-dbforge --enable
```

This is the path to prefer for the widget. Omarchy clones the repo, validates
the manifest, asks before enabling, and lets you pick a bar section. Later
versions arrive with:

```bash
omarchy plugin update io.github.fiifiofosu.dbforge
```

Move it along the bar with:

```bash
omarchy bar move io.github.fiifiofosu.dbforge
```

### Installing the widget from your clone

If you already cloned to build DBForge, you can install the widget from there
too rather than letting Omarchy clone it a second time:

```bash
cd omarchy-dbforge
./install.sh
```

That validates the manifest, copies the plugin into
`~/.config/omarchy/plugins/io.github.fiifiofosu.dbforge/`, and asks a running
shell to rescan.

Putting the widget on your bar is a separate question, because it edits
`~/.config/omarchy/shell.json`, which is your configuration and not the
installer's. So it asks first, and installs without enabling if you decline or
if there is no terminal to ask in. `--enable` and `--no-enable` answer ahead of
time.

The plugin is copied rather than symlinked, because Omarchy rejects symlinks
inside a plugin folder — so a clone install does not update itself. Re-run
`./install.sh` after pulling, or use `omarchy plugin add` above and let Omarchy
manage the checkout.

## Usage

**In the bar**, the icon is lit when something is running and dimmed when
nothing is, and turns urgent when an instance needs attention. Hovering shows
the counts.

| Gesture | Action |
|---|---|
| Left click | Open the panel |
| Middle click | Open the DBForge TUI |
| Right click | Refresh now |

**In the panel**, each instance is one row: its name, its engine, version, port
and state, a button to copy its connection string, and a switch to start or
stop it. Clicking a row copies its connection string.

| Key | Action |
|---|---|
| `j` / `k` or arrows | Move the cursor |
| `enter` / `space` | Start or stop the selected instance |
| `c` | Copy the selected instance's connection string |
| `t` | Open the DBForge TUI |
| `r` | Refresh now |
| `esc` | Close |

Creating and destroying instances is deliberately not here. Those are
destructive enough to deserve a confirmation step, which is what the TUI is
for; `t` gets you there.

When the daemon is not running the panel says so and offers to start it, which
is a different situation from having no instances — the panel distinguishes
the two rather than showing an empty list for both.

## Settings

Configurable per widget from Omarchy's bar settings:

| Setting | Default | Meaning |
|---|---|---|
| Refresh interval | 10s | How often to poll `dbforged`. The panel also refreshes when opened and after every action, so a long interval costs little. |
| `dbctl` binary | auto | Leave empty to search `$DBFORGE_DBCTL`, `~/.local/bin`, `/usr/local/bin`, `/usr/bin`, then `PATH`. |
| `dbforge-tui` binary | auto | Same search, for the terminal action. |

The search exists because the shell is started by the systemd user manager,
whose environment has no `~/.local/bin` on `PATH`. A bare `dbctl` would resolve
to nothing and the widget would sit there empty with no explanation.

## Remove

```bash
./uninstall.sh
```

That takes the widget off the bar and deletes
`~/.config/omarchy/plugins/io.github.fiifiofosu.dbforge/`. By hand it is:

```bash
omarchy plugin remove io.github.fiifiofosu.dbforge
```

Either way it removes the widget only. DBForge, its daemon and your database
instances are untouched — [`dbforge/README.md`](dbforge/README.md#upgrading-and-uninstalling)
covers removing those, and is careful to leave your data alone unless you ask.

## License

MIT, both halves. See [LICENSE](LICENSE).

The widget bundles no third-party code: the QML uses only Omarchy's own
`qs.Ui` / `qs.Commons` components and Qt, and `Model.js` is plain JavaScript
with no dependencies. DBForge's Go dependencies are declared in
[`dbforge/go.mod`](dbforge/go.mod) — chiefly Podman's client bindings and
Bubble Tea for the TUI.

## Development

```bash
make check   # tests, validate, lint
```

or by hand:

```bash
node model_test.mjs
omarchy plugin validate .
/usr/lib/qt6/bin/qmllint -I /usr/share/omarchy/shell Panel.qml Service.qml DbForgeIcon.qml
```

Use the Qt 6 `qmllint`, not the `qmllint` on `PATH` — on Arch that one is the
Qt 5 build and passes everything.

Expect warnings about `qs.Ui` and `qs.Commons` failing to import, and the
unqualified-access and missing-type warnings that follow from it. `qmllint`
cannot resolve Quickshell's module layout; Omarchy's own first-party plugins
produce the same warnings in the same categories. What matters is that no new
*category* of warning appears.

| File | Role |
|---|---|
| `manifest.json` | Plugin metadata, entry point and settings schema |
| `Panel.qml` | Bar widget and its panel — the `barWidget` entry point |
| `Service.qml` | Talks to `dbforged` via `dbctl`; owns all polling and actions |
| `Model.js` | Pure shaping: parsing, sorting, counting, row text |
| `model_test.mjs` | Checks for `Model.js` — the states that are awkward to reach live |
| `DbForgeIcon.qml` | The drawn database mark |
| `install.sh` / `uninstall.sh` | Clone-install helpers; `omarchy plugin add` does not use them |
| `dbforge/` | The daemon, CLI and TUI — its own `make test`, and CI covers both halves |

### Three decisions worth knowing

**It polls; it does not listen.** DBForge signals its waybar module with
`SIGRTMIN+8`, and there is no equivalent here — a plugin shares the long-lived
shell process, and installing a signal handler in the bar would be a hazard for
every other widget. So it runs one short-lived `dbctl` per interval, and
refreshes on panel open and after every action. The alternative is a socket
client inside the shell process, which is a lot of surface area for a value
that changes a few times a day.

**It resolves `dbctl` itself.** The shell is started by the systemd user
manager, whose `PATH` has no `~/.local/bin`, because user services never source
`~/.bashrc`. A `Process` running a bare `dbctl` finds nothing and the widget
renders empty with no error anywhere. So it searches on startup and says so
when it comes up empty.

**The lifecycle methods are inherited, not written.** `qs.Ui`'s `Panel`
supplies `open`/`close`/`toggle`/`closeForPopoutSwitch` and the
`opened`/`popoutSwitchClosing` properties, wired to the bar's popout
coordinator. Reimplementing them by hand works until the bar tries to switch
popouts.
