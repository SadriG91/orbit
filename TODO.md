# TODO

Open work, and the findings that constrain it. Kept here rather than in an
issue tracker because most of what matters below is measurements — several of
which took a few failed attempts to get — and losing them means someone
re-derives them or, worse, guesses.

## Open risk: an unexplained freeze

Orbit locked up once after a session was attached to in another tab. The
process was still alive, 0% CPU, all threads parked — blocked, not spinning.

**Do not cut a release until this has a cause.** Merging to main is safe;
orbit only ships on a `v*` tag push.

Two hypotheses were tested and neither reproduced: a control client streaming a
session that a second real client floods with output, and the same with the
cursor moving so `switch-client` commands are written into a pty saturated in
the other direction. Both stayed responsive. `sample` on the live wedged
process gave nothing — it sees OS threads, and parked goroutines have none.

Review then found a real defect in the same area, since fixed: `waitForPaneCmd`
waited only on `Dirty()`, so a control client that died left the wait blocked
forever and the Model still holding a stream, which stopped `capture` falling
back to polling. **That is probably not this.** It would freeze the *preview*
while the list, the keys and the tick carried on, and the report was of the
whole dashboard. Worth knowing it has already been ruled in and fixed, so a
recurrence is something else.

The same review found five defects across three rounds and **every one was in
an error or teardown path** — a connection dying, a write failing, a dead
field, file modes. None in the paths that were tested hardest. If the freeze is
hunted again, that is the neighbourhood.

So the next occurrence has to be caught in the act:

```sh
kill -USR1 $(pgrep -x orbit)     # before killing it
cat ~/.cache/orbit/goroutines.txt
```

`SIGUSR1` is always armed (`internal/debug`). It exists because `SIGQUIT`,
Go's own answer, writes the traceback to stderr — which for a full-screen
program is the terminal it has been painting over, and takes the process with
it. The one moment the state exists is the one moment it cannot be read.

The streaming preview is the prime suspect only because it is what changed.
It is deliberately left enabled rather than defaulted off: it works well in
practice, and switching it off guarantees never learning anything.

## 1. Get session state from hooks instead of inferring it

**The signal orbit exists to provide is wrong about 1 in 9 times.** Measured
over 8,327 real tool calls in local transcripts:

```
median 1.4s    p90 13.4s    p99 117.6s    max 2862s
longer than 12s: 896  (10.8%)
```

`Session.Resolve` promotes an unanswered tool call to `NeedsApproval` after 12
seconds of stillness. During a long tool call the transcript gets nothing until
the result arrives, so **every tool call over 12s shows as "needs you"** for its
whole duration. The threshold cannot fix this: lower it and false positives
rise, raise it and real prompts sit unflagged. It is a forced trade between two
errors, forced because the information is not in the transcript.

Two reasons inference cannot win:

- Claude's hook docs say `transcript_path` is *"written asynchronously, may
  lag the in-memory conversation"*. Codex says its transcript *"isn't a stable
  interface and may change"*. Orbit derives state from a file documented to be
  both late and unstable.
- A pending permission prompt **is not written to the transcript at all**.
  Verified against a real one: the only `system` subtypes present are
  `turn_duration`.

Hooks deliver it as events instead.

| | approval event | `session_id` | how orbit installs it |
|---|---|---|---|
| Claude Code | `PermissionRequest`, `Notification[permission_prompt]` | yes | `--settings '<inline json>'` — no file at all |
| Codex | `PermissionRequest` | yes | `~/.codex/hooks.json`, a dedicated file; layers are additive |
| Copilot | none documented (`preToolUse` closest) | not in the sample payload | `~/.copilot/hooks/orbit.json`, a directory |

Nothing needs to modify a file the user wrote. Claude's `--settings` accepts
inline JSON and was verified end to end: the hook fired, the payload carried
`session_id`, `cwd` and `transcript_path`, and it did not displace global
settings.

### Two traps

**The hook command string must never change.** Codex records trust against the
hook definition's *hash* and silently skips hooks whose definition changed until
re-reviewed via `/hooks`. Edit the command in a release and codex detection dies
quietly on every existing install. So it must be a permanent minimal shape —
`<abs path>/orbit hook <event>` — with everything that might evolve living
inside the binary. `os.Executable()`, since orbit may not be on `PATH` inside a
tmux shell. Never `--dangerously-bypass-hook-trust`: it bypasses trust for every
hook on the machine, not just orbit's.

**The hook runs inside the user's agent loop.** Claude and Codex execute hooks
synchronously, and Codex's default timeout is 600 seconds. A slow or hung
`orbit hook` does not degrade orbit, it stalls the agent being observed. No
network, no locks, no scanning: one small file write and exit, with an explicit
short `timeout` in every hook config rather than the default.

### Shape

A hidden `orbit hook` subcommand reads the JSON on stdin and writes a
per-session state file under `~/.cache/orbit/state/`. The scan prefers it and
falls back to transcript inference when absent, so sessions started outside
orbit keep working — this is a partial fix by construction, and the 10.8% stays
for those. Stale files need pruning the way the summary cache does.

Do Claude first: it is verified, needs no file, and covers most sessions here.

### Rejected

A pane pattern matcher was built and reverted (`7ba6b6e`, reverted in
`b1abc9c`). An agent writing prose like *"What are the options? 1. … 2. …"* and
then calling a tool lands in the ambiguous state with choice-shaped text on
screen, producing a false ▲ for the entire tool run. A late flag is annoying; a
wrong one corrodes the only thing orbit is for. The codex and copilot patterns
were also invented rather than sampled.

Pane *stillness* — working agents animate their UI, parked ones do not — is
format-agnostic and might work, but three attempts to measure it failed (one
session was idle, one never left manual mode). Unverified, and moot if hooks
land.

## 2. Dispatch sessions over the CLIs' structured event streams

For the `@codex look at feature X` idea. All three CLIs emit structured JSONL,
verified by running each:

| | flag | join key | turn end |
|---|---|---|---|
| Claude | `-p --output-format stream-json --verbose` | `session_id` in every event | result event |
| Codex | `exec --json` | `thread_id` on `thread.started` | `turn.completed` (+`usage`) |
| Copilot | `-p --output-format json` | inside `data` — needs confirming | `assistant.turn_end`, `assistant.idle` |

Copilot's is the richest of the three: `user.message`, `assistant.turn_start`,
`model.call_start`, `assistant.message_delta`, `assistant.turn_end`,
`session.usage_checkpoint`, `assistant.idle`. The README's claim that copilot's
live states are coarser is true of its database and the opposite of true of its
CLI — worth correcting when this lands.

**Do not adopt the SDKs.** The Claude Agent SDK (checked the installed copy,
v0.3.160) spawns `cli.js` and drives it with `--print --output-format
stream-json`. The SDK *is* a CLI wrapper, so its only advantage is ergonomics in
TS/Python — and orbit is a static `CGO_ENABLED=0` Go binary on four targets.
Adopting one means shipping a Node or Python runtime and losing "brew install,
nothing to compile", to obtain a protocol Go can speak directly.

**This is the dispatch path, not the observation path.** `stream-json` only
works for sessions orbit runs, non-interactively, with no TUI to attach to. It
cannot see a session started in someone's own terminal. Hooks and dispatch are
complementary; neither replaces the other.

Worth verifying: dispatched sessions should stay resumable (`claude --resume`,
`codex resume`) since these are the same binaries writing the same stores —
dispatch headlessly, take over interactively later. And the approval events in
stream mode were never observed, because the test runs passed
`--allow-all-tools` and `--sandbox read-only` so nothing needed approval. Check
that before relying on it.

## Smaller

- **Colour in the live preview.** It renders `Pane.Text()`, not `Render()`,
  because the render path calls `format.Clean` (strips control characters) and
  `format.Truncate` (counts runes) — both destroy ANSI. Needs ANSI-aware
  measuring and truncation in `render.go`. Judge first whether truncation at
  preview width loses the important part; if it does, width handling matters
  more than colour.
- **Copilot's join key** for both hooks and dispatch.
- **`Pane.SetWatching`** is built and tested but unused: with one connection
  following the cursor there is never an unwatched session streaming. It only
  earns its place if streaming is ever paused wholesale.
