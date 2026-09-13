// Checks for Model.js, the pure half of the widget.
//
// Run with: node model_test.mjs   (or `make omarchy-plugin-test`)
//
// These are the cases that are awkward to reach in the live shell: a daemon
// that is down, a container that vanished, an instance that exited 137. Driving
// the real widget into them would mean stopping someone's databases, so the
// shaping rules are tested here and the QML is left to do layout.
//
// Model.js is a QML JS library, so it opens with `.pragma library`, which node
// cannot parse. Strip that one line and evaluate the rest -- the alternative is
// keeping a second copy of these functions in sync, which is worse.

import { readFileSync } from "node:fs"
import { dirname, join } from "node:path"
import { fileURLToPath } from "node:url"

const here = dirname(fileURLToPath(import.meta.url))
const source = readFileSync(join(here, "Model.js"), "utf8").replace(/^\.pragma library$/m, "")
const Model = {}
new Function("exports", source + "\n" + [
  "parseList", "summarize", "summaryText", "barLabel",
  "statusWord", "severity", "rowMeta", "isRunning", "isActionable", "elide"
].map(n => `exports.${n} = ${n}`).join("\n"))(Model)

let failures = 0
function eq(got, want, label) {
  const g = JSON.stringify(got), w = JSON.stringify(want)
  if (g === w) {
    console.log("ok    " + label)
  } else {
    console.log(`FAIL  ${label}\n        got  ${g}\n        want ${w}`)
    failures++
  }
}

// `dbctl list --json` prints `null` rather than `[]` for an empty list, which
// is exactly the input a fresh install produces.
eq(Model.parseList("null"), { ok: true, instances: [] }, "null is an empty list")
eq(Model.parseList(""), { ok: true, instances: [] }, "empty stdout is an empty list")
eq(Model.parseList("not json").ok, false, "garbage is reported, not thrown")
eq(Model.parseList('{"a":1}').ok, false, "an object is not a list")

const sample = JSON.stringify([
  { id: "zed", engine: "redis", version: "8", port: 15379, status: "stopped", last_exit_code: 0 },
  { id: "alpha", engine: "postgres", version: "18", port: 15432, status: "running", last_exit_code: 0 },
  { id: "gone", engine: "mysql", version: "9", port: 15306, status: "missing", last_exit_code: 0 },
  { id: "crashed", engine: "postgres", version: "16", port: 15433, status: "stopped", last_exit_code: 137 }
])
const parsed = Model.parseList(sample)

eq(parsed.instances.map(i => i.id), ["gone", "crashed", "alpha", "zed"],
   "anything broken sorts first, then running, ids break ties")
eq(Model.summarize(parsed.instances), { running: 1, stopped: 1, problems: 2, total: 4 },
   "every instance is counted exactly once")

// "the daemon is down" and "you have no instances" are both an absence and
// must never render the same way -- the same distinction DBForge's own status
// output makes.
eq(Model.summaryText(Model.summarize(parsed.instances), false),
   "1 running · 1 stopped · 2 need attention", "summary names all three states")
eq(Model.summaryText(Model.summarize(parsed.instances), true), "Daemon not running",
   "offline outranks any count")
eq(Model.summaryText(Model.summarize([]), false), "No instances yet",
   "empty is not offline")

eq(Model.barLabel(Model.summarize(parsed.instances), false), "2",
   "the bar shows the problem count, not the running count")
eq(Model.barLabel(Model.summarize([{ id: "a", status: "running" }]), false), "1",
   "the bar shows the running count when nothing is wrong")
eq(Model.barLabel(Model.summarize([{ id: "a", status: "stopped" }]), false), "",
   "an idle DBForge shows no number rather than a zero")
eq(Model.barLabel(Model.summarize([]), true), "", "offline shows no number")

eq(Model.statusWord({ status: "stopped", last_exit_code: 137 }), "exited 137",
   "an unclean exit says so")
eq(Model.statusWord({ status: "stopped", last_exit_code: 0, suspended: true }), "suspended",
   "suspended is distinct from stopped")
eq(Model.statusWord({ status: "missing" }), "missing", "missing is named")

// A missing container has drifted out from under the daemon. Offering a switch
// would imply the widget can fix it.
eq(Model.isActionable({ status: "missing" }), false, "missing offers no switch")
eq(Model.isActionable({ status: "creating" }), false, "creating offers no switch")
eq(Model.isActionable({ status: "stopped" }), true, "stopped can be started")

eq(Model.rowMeta({ engine: "postgres", version: "18", port: 15432, status: "running" }),
   "postgres:18  ·  :15432  ·  running", "row meta reads as one line")
eq(Model.elide("a ".repeat(200), 20).length, 20, "long errors are capped")

console.log(failures === 0 ? "\nALL PASS" : `\n${failures} FAILED`)
process.exit(failures === 0 ? 0 : 1)
