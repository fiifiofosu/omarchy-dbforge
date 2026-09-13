# DBForge — Omarchy bar widget

Local database instances in the Omarchy bar. See what is running, start and
stop it, and copy a connection string, without leaving the bar.

This is the Omarchy 4.0 shell plugin for [DBForge](https://github.com/fiifiofosu/dbforge), which manages local
Postgres/MySQL/Redis instances as rootless Podman containers. The widget needs
DBForge installed; it does not bundle it.

If you are still on a waybar setup, DBForge ships a waybar module
([`packaging/waybar`](https://github.com/fiifiofosu/dbforge/tree/main/packaging/waybar)) instead. The two are
alternatives, not companions.

## Requirements

- Omarchy 4.0 or newer (`omarchy plugin` and the Quickshell-based bar)
- DBForge installed, with `dbctl` and `dbforge-tui` on disk
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

```bash
omarchy plugin add https://github.com/fiifiofosu/omarchy-dbforge --enable
```

This is the path to prefer. Omarchy clones the repo, validates the manifest,
asks before enabling, and lets you pick a bar section. Later versions arrive
with:

```bash
omarchy plugin update io.github.fiifiofosu.dbforge
```

Move it along the bar with:

```bash
omarchy bar move io.github.fiifiofosu.dbforge
```

### From a clone

If you would rather read the code first, or are working on it:

```bash
git clone https://github.com/fiifiofosu/omarchy-dbforge
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
instances are untouched — see the [DBForge README](https://github.com/fiifiofosu/dbforge#upgrading-and-uninstalling)
for uninstalling those.

## License

MIT, the same as DBForge itself. See [LICENSE](LICENSE).

The plugin bundles no third-party code: the QML uses only Omarchy's own
`qs.Ui` / `qs.Commons` components and Qt, and `Model.js` is plain JavaScript
with no dependencies.

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

Design notes for the widget — why it polls instead of using signals, why the
panel has no create/destroy, and the `qmllint` trap on Arch — are in DBForge's
[phase 4b notes](https://github.com/fiifiofosu/dbforge/blob/main/docs/phase-4b-notes.md).
