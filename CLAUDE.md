# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

orbit is a Go TUI (Bubble Tea) that lists the coding-agent sessions on the
machine — Claude Code, Codex, Copilot CLI — and attaches to them through a
private tmux server. The README is the user-facing reference and is unusually
complete; read it for behaviour, keys, config and the release process. This
file covers what you need to *work on* the code.

## Commands

```sh
go build ./cmd/orbit                # build (the root ./orbit binary is gitignored)
go test ./...                       # full suite; CI runs it with -race on macOS and Ubuntu
go test -race ./internal/session    # one package
go test -run TestTmuxRoundTrip ./test
gofmt -l .                          # CI fails on any output from this
go vet ./...
```

CI (`.github/workflows/test.yml`, shared by `ci.yml` and `release.yml`) runs
gofmt, vet and `go test -race ./...` on macOS *and* Ubuntu — an Ubuntu-only bug
once shipped. Match that before pushing.

Tests that touch the real world are skipped unless asked for:

```sh
ORBIT_TERM_LIVE=1 go test ./internal/term -run TestLive -v      # opens real Ghostty tabs
ORBIT_UPDATE_LIVE=1 go test ./internal/update -run TestLive -v  # hits GitHub, downloads a release
./scripts/check-self-update.sh                                   # needs expect; pty-driven
```

`scripts/check-self-update.sh` covers the one step Go tests cannot: orbit
`exec`ing the new build over itself. It builds a deliberately stale orbit in
`.selfupdate-check/` with its own `HOME`, so it never touches your install,
config or update cache, and never runs brew.

`test/tmux_integration_test.go` spawns a real tmux server on the orbit socket
and skips when tmux is absent.

Releasing is a `v*` tag push — nothing manual. GoReleaser stamps
`main.version` from `.Tag` (not `.Version`, which drops the leading `v` and
would break version comparison in the updater).

## Architecture

Dependencies run strictly one way:

```
format, config      no local imports
hooks               agent hook events → state files; imports format only
tmux                names and strings only — knows nothing about agents
session             the domain model (parsers, index, state, sorting)
search, summary     read transcripts; depend on session
term                Ghostty/iTerm2 AppleScript; knows nothing about sessions
update              release check and self-replacement
ui                  composes everything
```

`ui` is the only layer allowed to know about both an agent and tmux — resuming
a session, spawning a new one, and attaching all live in `internal/ui/attach.go`.
Keep it that way: pushing agent knowledge into `tmux`, or tmux knowledge into
`session`, is the main structural regression to avoid.

**Session discovery** (`internal/session/`): one file per agent — `claude.go`
reads `~/.claude/projects/*/*.jsonl`, `codex.go` reads
`~/.codex/sessions/**/rollout-*.jsonl`, `copilot.go` shells out to the
`sqlite3` CLI against `~/.copilot/session-store.db` (CLI, not a cgo driver, so
the binary stays static — `CGO_ENABLED=0`).

`Index` is the cache: transcripts are re-parsed only when mtime *or* size
changes, so a warm tick is `stat()` calls. Two invariants that have already
cost bugs:

- `Index` locks its own mutex. Single-flight scanning in `ui` is an
  efficiency measure, not the safety mechanism — a concurrent map write here
  is fatal.
- `Scan` returns *shallow copies*. The UI writes `Tmux`/`State` on what it
  gets while the next scan may already be running.

Session recency comes from `EventTime` — the timestamp the agent recorded, not
file mtime. Agents rewrite old transcripts in batches, which makes mtime lie by
weeks.

State is `hint` (what the transcript says) joined with live tmux facts in
`Session.Resolve`. `HintMaybeApproval` — an unanswered tool call — becomes
"needs you" only once it has sat still for 12s; before that it's just a slow tool.

**The Bubble Tea model** (`internal/ui/ui.go`, ~1000 lines) holds all state;
`render.go` draws, `kitty.go` handles the graphics-protocol logos, `notify.go`
sends OSC 9. Scans are single-flighted behind `m.scanning` with a generation
counter (`scanGen`) so stale results are discarded, plus a watchdog
(`scanStuck`) so a dropped flag can't wedge the dashboard permanently. Results
are sorted *on arrival*, not inside the scan, so pressing `o`/`p` mid-scan
isn't undone.

**tmux** (`internal/tmux/`) runs on a private server (`tmux -L orbit`) with an
embedded config installed to `~/.config/orbit/tmux.conf`; it never touches a
normal tmux. Two hard-won details live in `parseListLine` and `Args`: tmux 3.4
escapes the `\x1f` field separator as a literal `\037` (the parser accepts
either), and every invocation must pass `-u` or tmux mangles multi-byte
characters in titles, causing an endless retitle loop.

`MinVersion` sets the supported floor at tmux 3.4 — what Ubuntu 24.04 LTS
ships — and `requireTmux` enforces it on start. The floor is measured, not
assumed: read the comment on `MinVersion` before raising it, and don't raise it
on the strength of a changelog entry without a failing test to go with it.

**Agents are launched by typing into an interactive login shell**, not via
`new-session <cmd>` — agents are frequently shell functions or version-manager
shims that don't exist in a bare `sh -c`. Hence `spawn_delay`.

**Summaries** (`internal/summary/`) run each provider's own CLI in
non-interactive mode (`claude -p`, `codex exec`, `copilot -p`), so there are no
API keys anywhere in this codebase and the cost lands on the user's existing
subscription. Records track `CoveredMsgs`, so a continued session goes *stale,
not invalid*, and the next generation sends the old summary plus only the new
messages. Rebuilds are forced every 5 incremental updates, since rolling
summaries compound their own omissions.

**Config** (`internal/config/`): `config.toml` is embedded and written on first
run. `sync.go` appends settings added since a user's file was written,
preserving comments and TOML table structure — it *only ever adds*, and never
rewrites, reorders or reformats an existing key. When adding a config setting,
add it to `config.toml` with its explanatory comment; `Sync` picks it up. Note
the ordering trap `sync.go` guards: a bare key appended at the end of a file
falls under whatever `[table]` precedes it, silently becoming a different
setting.

**Environment overrides win over the config file**: `ORBIT_ICONS`,
`ORBIT_SPAWN_DELAY`, `ORBIT_TAB_DELAY`, `ORBIT_NO_UPDATE`.

## Open work

`TODO.md` holds what's outstanding and — more usefully — the measurements
behind it: why session state is wrong about 1 in 9 times, why the transcript
can't fix it, and which approaches were tried and rejected. Read it before
touching state resolution or the live preview. It also records the one open
risk: an unexplained freeze, and the instruction not to cut a release until it
has a cause.

## Conventions

- Unit tests sit beside the code, so they can reach unexported internals.
  `test/` is only for tests that touch the real system.
- Comments in this codebase explain *why*, at length, and usually cite the bug
  or constraint that forced the code to be that shape. Match that register —
  don't add comments that restate the code.
- Commit messages are prose: a sentence-case imperative subject describing the
  change's intent ("Append settings to config files as orbit gains them"), then
  a body explaining what was wrong, why the fix takes this shape, and what was
  deliberately not done.
- Measure rendered width with `lipgloss.Width`, never `len([]rune(...))` — a
  logo cell is a placeholder rune plus diacritics and counts as 2 columns.
- Anything user-visible that changes behaviour belongs in the README too; it is
  kept current.
