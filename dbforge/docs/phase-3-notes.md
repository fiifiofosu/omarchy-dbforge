# Phase 3 — the TUI

Built with Bubble Tea, verified by driving the real binary in a pty against
live containers.

## The TUI is a separate binary

`cmd/dbforge-tui` rather than a subcommand of `cmd/dbforge`, and this is not
stylistic.

Bubble Tea v1.3.10 ships this in `tea_init.go`:

```go
func init() {
	// XXX: This is a workaround to make assure that Lip Gloss and Termenv
	// query the terminal before any Bubble Tea Program runs...
	_ = lipgloss.HasDarkBackground()
}
```

`HasDarkBackground` writes an OSC 11 query *and* a cursor-position report
(`ESC [ 6n`), then waits for both. `termenv.OSCTimeout` is 5 seconds. Because
this is a package `init`, it runs on import — before `main` — for any binary
that links Bubble Tea, whether or not it ever starts a program.

Measured, with the TUI linked into the CLI:

```
dbctl list (on a tty that does not answer)   5.03s
dbctl list (after moving the TUI out)        0.05s
```

Interactive terminals answer the cursor-position report automatically, so a
person at a real terminal never saw this — confirmed by answering the CPR in
the harness, which drops first paint to 0.04s. But CI runners, pty harnesses
and some multiplexer configurations do not answer, and a CLI should not have a
latency cliff that depends on the terminal. `cmd/dbforge/deps_test.go` fails
the build if the dependency comes back.

Three wrong guesses preceded the right answer, which is worth recording: it was
not `lipgloss.AdaptiveColor` (switching to palette colours changed nothing), not
the OSC reply format, and not canonical-mode buffering in the pty. Answering
OSC 11 alone did not help either. Only answering the cursor-position report did.

## Engine-specific stop timeouts

Stopping Postgres produced `exited(137)`, meaning SIGKILL: podman's default
stop timeout is 10s, and Postgres was still finishing `initdb`. Every ordinary
stop would then trigger crash recovery on the next start.

Engines now declare their own shutdown budget — 60s for the SQL engines, 15s
for Redis — and it is baked into the container as well as used by `dbctl stop`,
so a `podman stop` or a host shutdown gets the same budget. Postgres now exits
0 on a normal stop.

## Offline version lists

The create form needs a version list from Docker Hub, and a laptop is often
offline. `internal/registry` is built around the cache rather than around the
network call succeeding:

1. live fetch, which repopulates the cache
2. the cache, labelled with its age
3. a builtin list compiled into DBForge

The form never shows an empty list, and it always says which of the three it is
showing. Tags are filtered to plain versions — `16`, `8.4`, `10.11.2` — because
`latest`, `16-alpine` and `17rc1` are noise when picking a database version.
Sorting is numeric, so 10 sorts above 9.

## Destroying

Two outcomes that must never be one keystroke apart:

- *Remove the container, keep the data* is preselected. A reflexive enter is
  always the recoverable option.
- *Remove the container and delete the data* additionally requires typing the
  instance name. A mismatch clears the field and removes nothing.

Both paths state where the data lives. Verified by driving the real TUI:
selecting the destructive option and pressing enter reaches a name prompt, and
typing the wrong name is refused.

## Log streaming

A followed tail must not block the interface or leak the connection when the
user backs out. The stream runs in its own goroutine feeding a channel, and
`esc` cancels the context that owns the HTTP response body.

Two tests assert this against a real server that reports whether its handler
ever returned: one for `esc`, one for `ctrl+c`. Both would fail if the stream
were merely abandoned. The buffer is capped at 2000 lines so a chatty database
cannot grow it without bound.

## Verified by driving the real binary

A pty harness runs `dbforge-tui` and sends keystrokes, answering the terminal
queries a real terminal would. Confirmed live: the list with status, restart
policy and uptime; the guided create form through all five steps with a live
version list from Docker Hub; the destroy dialog including a rejected
confirmation; and log streaming from a real Postgres container.

## Not covered

The connection panel is unit tested rather than driven in the harness. Copying
to the clipboard is deliberately absent — the panel shows the string for the
terminal's own selection, since a clipboard dependency is not worth it for one
field.
