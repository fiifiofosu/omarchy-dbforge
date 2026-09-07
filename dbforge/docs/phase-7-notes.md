# Pull progress

`dbctl create` and the TUI's create form both went silent for however long an
image pull took — the TUI said "creating... (pulling the image can take a
while)" and then "please wait...", unchanged, for minutes.

## What podman will actually tell us

Before designing anything, a probe against the real socket, pulling an image
that was not cached:

```
[001] "Trying to pull docker.io/library/redis:8...\n"
[002] "Getting image source signatures\n"
[003] "Copying blob sha256:6310eb16bf42...\n"
...
[011] "Writing manifest to image destination\n"
```

That is all of it. `images.PullOptions` has a `ProgressWriter`, but what it
receives are podman's human-readable phase lines: **no byte counts, no totals,
no per-layer completion.** Layers are announced as they *start*, so even the
number of them is only known in hindsight.

So a real progress bar is not available through the bindings. It could be had
by shelling out to `podman pull`, which renders bars itself, but that means
parsing an ANSI progress display and a second execution path for something we
otherwise do over the API. Not worth it.

What can be reported honestly is the phase, how many layers have started, and
how long it has been going. That is what is shown. There is a test asserting
the progress line never contains a `%`, because the temptation to invent one
is exactly the kind of thing that gets added later by someone being helpful.

The integration test measured a real pull: **5 events across 30.6 seconds.**
That is up to half a minute of silence between phases, and it is why the
spinner and the elapsed clock matter more than the phase text does. Without
something moving, the interface looks hung precisely when it is busiest.

## Streaming without breaking the existing API

`POST /instances` gains `?stream=true`, which returns newline-delimited JSON:
progress events, then either the instance or an error. Without the parameter
the endpoint behaves exactly as before — there is a test for that, since every
existing client depends on it.

A streamed response has to send its status code before the work starts, so a
streaming create is always `200` and failures arrive as a final event. The
error's *kind* travels with it, so a client can still tell a conflict from a
broken daemon without the status line. `errKind` is now shared between the
streaming and non-streaming paths rather than duplicated, so the two cannot
drift.

The TUI consumes this with the same channel-and-command pattern the log tail
already uses. Progress sends are non-blocking: a full channel means the
interface is behind, and dropping a status line is better than stalling the
pull that produces them.

## Details worth keeping

- **Partial lines.** Nothing guarantees one `Write` per line, so the parser
  holds an incomplete line until its newline arrives. Without that, a slow
  connection produces events containing half a phase name.
- **Unknown lines pass through.** Podman knows more about what it is doing
  than the parser's switch does, so an unrecognised line is shown as-is —
  keeping its layer count, so the counter does not reset when podman says
  something new.
- **Digests are dropped.** A 64-character hex digest tells the reader nothing
  and pushes the useful part off the line. An integration test asserts no
  `sha256:` ever reaches a user-facing message.
- **A cached image still reports something.** "Nothing happened and then it
  worked" is less reassuring than being told the image was already present.

## A TUI left open across an upgrade

The first report after shipping this was that the TUI still showed
"creating... (pulling the image can take a while)". The installed binary was
current -- it had the new help text and not the old one -- but the *running
process* had started at 11:44 and the binary was replaced at 12:02.

That is the ordinary consequence of an upgrade: replacing a file does not
restart a process already executing it. The TUI is the one component people
leave open for days, so it is the one that drifts, and the symptom is that
everything the upgrade added simply appears not to work. Nothing said so.

The daemon now serves `GET /version` and the TUI compares it against its own
build, showing a persistent line under the title when they differ:

```
  this window is running 0.6.1, the daemon is 0.7.0 -- quit and reopen to pick up the new build
```

Persistent rather than a transient status line, because the consequence is
persistent: a message that scrolls away after three seconds would be read once
and wondered about for an hour. The check fails silently when the daemon is
unreachable -- that is already reported by the instance fetch, and complaining
twice about one problem is noise.

## Verified

| Behaviour | How |
|---|---|
| Podman really sends progress, and the parser matches its wording | integration test against a live pull, after removing the image |
| Phases are classified correctly | unit tests over captured podman output |
| A line split across writes is reassembled | unit test |
| Unknown phases survive with their layer count | unit test |
| No digest reaches the user | integration test assertion |
| No percentage is ever claimed | unit test on the rendered line |
| The layer counter never goes backwards | unit test through the update loop |
| The spinner stops when the create ends | unit test on both states |
| Streaming reports progress then the instance | server test with a scripted fake |
| A streamed failure keeps its error kind | server test |
| The non-streaming path is unchanged | server test |
| End to end against a real daemon | `dbctl create redis:7.4`, 11 phase lines then the result |
| A stale TUI window says so | unit tests on the notice and the list view; live `GET /version` |
