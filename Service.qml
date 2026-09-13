import QtQuick
import Quickshell
import Quickshell.Io
import "Model.js" as Model

// Talks to dbforged through the dbctl CLI, which is already the daemon's
// supported client. Going through the socket directly from QML would mean
// reimplementing the wire protocol inside the shell process for no gain.
Item {
  id: root

  property var settings: ({})

  // Resolved state.
  property var instances: []
  property var summary: Model.summarize([])
  property bool offline: false       // dbctl found, daemon not answering
  property bool missingBinary: false // dbctl not on disk at all
  property bool refreshing: false
  property string lastError: ""
  property string actionStatus: ""

  readonly property string dbctlPath: _dbctl
  readonly property string tuiPath: _tui
  readonly property bool ready: _dbctl !== ""
  readonly property bool busy: actionProcess.running || copyProcess.running || daemonProcess.running
  readonly property int refreshIntervalSec: intSetting("refreshIntervalSec", 10, 2, 3600)

  property string _dbctl: ""
  property string _tui: ""
  property bool _resolved: false

  signal copied(string label)

  function setting(name, fallback) {
    var value = settings ? settings[name] : undefined
    return value === undefined || value === null ? fallback : value
  }

  function intSetting(name, fallback, min, max) {
    var n = parseInt(String(setting(name, fallback)), 10)
    if (!isFinite(n)) n = fallback
    return Math.max(min, Math.min(max, n))
  }

  // The shell is started by uwsm/systemd, whose environment has no
  // ~/.local/bin on PATH -- a user manager never sources ~/.bashrc. A bare
  // "dbctl" would resolve to nothing and the widget would render empty, which
  // is the same trap packaging/waybar/module.jsonc documents. So look in the
  // places the installer actually puts the binary before falling back to PATH.
  function resolveBinaries() {
    if (resolveProcess.running) return
    resolveProcess.command = [
      "sh", "-c",
      'find_bin() {\n' +
      '  hint="$1"; name="$2"\n' +
      '  if [ -n "$hint" ] && [ -x "$hint" ]; then printf "%s\\n" "$hint"; return; fi\n' +
      '  for p in "$HOME/.local/bin/$name" "/usr/local/bin/$name" "/usr/bin/$name"; do\n' +
      '    if [ -x "$p" ]; then printf "%s\\n" "$p"; return; fi\n' +
      '  done\n' +
      '  p=$(command -v "$name" 2>/dev/null) || p=""\n' +
      '  printf "%s\\n" "$p"\n' +
      '}\n' +
      'hint="$1"; [ -z "$hint" ] && hint="$DBFORGE_DBCTL"\n' +
      'find_bin "$hint" dbctl\n' +
      'find_bin "$2" dbforge-tui\n',
      "sh",
      String(setting("dbctlPath", "")),
      String(setting("tuiPath", ""))
    ]
    resolveProcess.running = true
  }

  function refresh() {
    if (!_resolved) { resolveBinaries(); return }
    if (_dbctl === "") { missingBinary = true; return }
    if (listProcess.running) return
    refreshing = true
    listProcess.command = [_dbctl, "list", "--json"]
    listProcess.running = true
  }

  function applyList(raw) {
    var parsed = Model.parseList(raw)
    if (!parsed.ok) {
      lastError = parsed.error
      return
    }
    instances = parsed.instances
    summary = Model.summarize(parsed.instances)
    offline = false
    missingBinary = false
    lastError = ""
  }

  // start/stop are one call each. The optimistic path is deliberately absent:
  // starting a Postgres container takes long enough that a switch which flips
  // instantly and then flips back would be a lie, so the row waits for the
  // settle poll and the panel shows what the daemon is doing meanwhile.
  function start(id) { runAction([_dbctl, "start", id], "Starting " + id + "…") }
  function stop(id) { runAction([_dbctl, "stop", id], "Stopping " + id + "…") }

  function toggleInstance(instance) {
    if (!instance || !Model.isActionable(instance) || busy) return
    if (Model.isRunning(instance)) stop(instance.id)
    else start(instance.id)
  }

  function runAction(command, message) {
    if (!ready || actionProcess.running) return
    actionStatus = message
    lastError = ""
    actionProcess.command = command
    actionProcess.running = true
  }

  // Copying goes through argv rather than an interpolated shell string: an
  // instance id is user-supplied text and has no business being parsed by sh.
  function copyConnString(instance) {
    if (!ready || !instance || copyProcess.running) return
    copyProcess.command = [
      "sh", "-c",
      'out=$("$1" conn "$2") || exit 1\n' +
      'printf "%s" "$out" | wl-copy\n',
      "sh", _dbctl, instance.id
    ]
    copyProcess._label = instance.id
    copyProcess.running = true
  }

  function startDaemon() {
    if (daemonProcess.running) return
    actionStatus = "Starting dbforged…"
    daemonProcess.command = ["systemctl", "--user", "start", "dbforged"]
    daemonProcess.running = true
  }

  function openTui() {
    // omarchy-launch-tui is how Omarchy opens terminal apps in a floating
    // window; without it fall back to the session's terminal launcher. The
    // choice is made in the child shell because the helper may be installed
    // after the widget loaded.
    if (_tui === "") return
    Quickshell.execDetached([
      "sh", "-c",
      'if command -v omarchy-launch-tui >/dev/null 2>&1; then exec omarchy-launch-tui "$1"; fi\n' +
      'exec uwsm-app -- xdg-terminal-exec -e "$1"\n',
      "sh", _tui
    ])
  }

  Component.onCompleted: resolveBinaries()

  onSettingsChanged: {
    _resolved = false
    resolveBinaries()
  }

  Timer {
    id: refreshTimer
    interval: root.refreshIntervalSec * 1000
    repeat: true
    running: root._resolved
    triggeredOnStart: true
    onTriggered: root.refresh()
  }

  // A container takes a few seconds to come up or shut down, so poll a handful
  // of times after an action instead of leaving the row stale until the next
  // periodic refresh.
  Timer {
    id: settleTimer
    property int ticks: 0
    interval: 1200
    repeat: true
    running: false
    onTriggered: {
      ticks += 1
      root.refresh()
      if (ticks >= 6) { ticks = 0; running = false; root.actionStatus = "" }
    }
  }

  Timer {
    id: actionStatusTimer
    interval: 2400
    repeat: false
    onTriggered: root.actionStatus = ""
  }

  Process {
    id: resolveProcess
    running: false
    command: []
    stdout: StdioCollector { id: resolveOut; waitForEnd: true }
    onExited: function(exitCode) {
      var lines = String(resolveOut.text || "").split("\n")
      root._dbctl = String(lines[0] || "").trim()
      root._tui = String(lines[1] || "").trim()
      root._resolved = true
      root.missingBinary = root._dbctl === ""
      if (root._dbctl !== "") root.refresh()
    }
  }

  Process {
    id: listProcess
    running: false
    command: []
    stdout: StdioCollector { id: listOut; waitForEnd: true }
    stderr: StdioCollector { id: listErr; waitForEnd: true }
    onExited: function(exitCode) {
      root.refreshing = false
      if (exitCode === 0) {
        root.applyList(String(listOut.text || ""))
        return
      }
      // dbctl exits nonzero when it cannot reach the socket. That is a
      // different situation from "no instances" and needs a different answer
      // from the user, so the panel is told which one it is.
      root.offline = true
      root.instances = []
      root.summary = Model.summarize([])
      root.lastError = Model.elide(String(listErr.text || "") || "dbforged is not running")
    }
  }

  Process {
    id: actionProcess
    running: false
    command: []
    stdout: StdioCollector { id: actionOut; waitForEnd: true }
    stderr: StdioCollector { id: actionErr; waitForEnd: true }
    onExited: function(exitCode) {
      if (exitCode !== 0) {
        root.lastError = Model.elide(String(actionErr.text || actionOut.text || "") || "Command failed")
        root.actionStatus = ""
      }
      settleTimer.ticks = 0
      settleTimer.restart()
      root.refresh()
    }
  }

  Process {
    id: copyProcess
    property string _label: ""
    running: false
    command: []
    stderr: StdioCollector { id: copyErr; waitForEnd: true }
    onExited: function(exitCode) {
      if (exitCode === 0) {
        root.actionStatus = "Copied connection string for " + copyProcess._label
        root.copied(copyProcess._label)
      } else {
        root.lastError = Model.elide(String(copyErr.text || "") || "Could not copy (is wl-copy installed?)")
        root.actionStatus = ""
      }
      actionStatusTimer.restart()
    }
  }

  Process {
    id: daemonProcess
    running: false
    command: []
    stderr: StdioCollector { id: daemonErr; waitForEnd: true }
    onExited: function(exitCode) {
      if (exitCode !== 0) {
        root.lastError = Model.elide(String(daemonErr.text || "") || "Could not start dbforged")
        root.actionStatus = ""
      }
      settleTimer.ticks = 0
      settleTimer.restart()
    }
  }
}
