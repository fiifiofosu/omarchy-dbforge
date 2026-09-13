// Pure data helpers for the DBForge bar widget.
//
// Everything that turns `dbctl list --json` into something the panel can
// render lives here rather than in QML bindings, so the shaping rules stay in
// one readable place and the QML is left to do layout.

.pragma library

// Severity ranks instances so both the bar label and the panel list lead with
// whatever is wrong. It mirrors the ranking DBForge's own `dbctl status` uses,
// so this widget and DBForge never disagree about what counts as a problem.
function severity(instance) {
  if (!instance) return 0
  if (instance.status === "missing") return 3
  if (instance.status !== "running" && Number(instance.last_exit_code || 0) !== 0) return 2
  if (instance.status === "running") return 1
  return 0
}

// statusWord is the short human label for a row.
function statusWord(instance) {
  if (!instance) return "unknown"
  if (instance.status === "missing") return "missing"
  if (instance.status === "creating") return "creating"
  if (instance.status !== "running" && Number(instance.last_exit_code || 0) !== 0) {
    return "exited " + Number(instance.last_exit_code)
  }
  if (instance.status === "running") return "running"
  if (instance.suspended === true) return "suspended"
  return "stopped"
}

// parseList turns raw stdout into a sorted instance array.
//
// `dbctl list --json` prints `null` rather than `[]` for an empty list, which
// is why the array check below is not redundant.
function parseList(raw) {
  var text = String(raw || "").trim()
  if (text === "") return { ok: true, instances: [] }
  var parsed
  try {
    parsed = JSON.parse(text)
  } catch (e) {
    return { ok: false, instances: [], error: "Could not read dbctl output" }
  }
  if (!parsed) return { ok: true, instances: [] }
  if (!Array.isArray(parsed)) return { ok: false, instances: [], error: "Unexpected dbctl output" }

  var out = []
  for (var i = 0; i < parsed.length; i++) {
    var it = parsed[i]
    if (!it || !it.id) continue
    out.push({
      id: String(it.id),
      engine: String(it.engine || ""),
      version: String(it.version || ""),
      port: Number(it.port || 0),
      status: String(it.status || ""),
      restart: String(it.restart || ""),
      desired: String(it.desired || ""),
      suspended: it.suspended === true,
      last_exit_code: Number(it.last_exit_code || 0)
    })
  }
  return { ok: true, instances: sortForDisplay(out) }
}

// sortForDisplay puts anything broken at the top, then running, then the rest,
// with ids breaking ties so the list does not reshuffle between refreshes.
function sortForDisplay(instances) {
  var copy = (instances || []).slice()
  copy.sort(function(a, b) {
    var d = severity(b) - severity(a)
    if (d !== 0) return d
    return a.id < b.id ? -1 : (a.id > b.id ? 1 : 0)
  })
  return copy
}

// summarize counts the three states the bar cares about. An instance is
// counted exactly once, and a problem outranks being running.
function summarize(instances) {
  var running = 0, stopped = 0, problems = 0
  for (var i = 0; i < (instances || []).length; i++) {
    var s = severity(instances[i])
    if (s >= 2) problems += 1
    else if (s === 1) running += 1
    else stopped += 1
  }
  return { running: running, stopped: stopped, problems: problems, total: (instances || []).length }
}

// summaryText is the panel hero's subtitle: the whole picture in one line.
function summaryText(summary, offline) {
  if (offline) return "Daemon not running"
  if (!summary || summary.total === 0) return "No instances yet"
  var parts = [summary.running + " running"]
  if (summary.stopped > 0) parts.push(summary.stopped + " stopped")
  if (summary.problems > 0) parts.push(summary.problems + " need attention")
  return parts.join(" · ")
}

// barLabel is the text beside the bar icon. Keeping it to a single number
// matters: the bar has no room to explain itself, so it shows the count that
// most deserves a click. A problem count wins over a running count, and a
// healthy-but-idle DBForge shows nothing rather than a zero.
function barLabel(summary, offline) {
  if (offline) return ""
  if (!summary || summary.total === 0) return ""
  if (summary.problems > 0) return String(summary.problems)
  if (summary.running > 0) return String(summary.running)
  return ""
}

// rowMeta is the dim second line of an instance row.
function rowMeta(instance) {
  if (!instance) return ""
  var engine = instance.engine + (instance.version ? ":" + instance.version : "")
  var port = instance.port > 0 ? "  ·  :" + instance.port : ""
  return engine + port + "  ·  " + statusWord(instance)
}

// isRunning is what the row's switch reflects.
function isRunning(instance) {
  return !!instance && instance.status === "running"
}

// isActionable says whether start/stop makes sense. A missing container has
// drifted out from under the daemon; offering to start it would hide that.
function isActionable(instance) {
  if (!instance) return false
  return instance.status !== "missing" && instance.status !== "creating"
}

// elide keeps a command's error output to something a panel can show.
function elide(text, limit) {
  var value = String(text || "").replace(/\s+/g, " ").trim()
  var cap = limit || 140
  return value.length > cap ? value.substring(0, cap - 1) + "…" : value
}
