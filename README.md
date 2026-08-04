# orbit

A dashboard for the coding-agent sessions running on your machine — Claude Code,
Codex and GitHub Copilot CLI, across every project. Meant to live in its own
terminal tab.

It answers the question you can't answer by staring at a wall of tabs: **which
session is blocked on me right now?** Sessions run inside tmux, so closing a tab
never kills work.

```
 ██████╗ ██████╗ ██████╗ ██╗████████╗
██╔═══██╗██╔══██╗██╔══██╗██║╚══██╔══╝
██║   ██║██████╔╝██████╔╝██║   ██║
██║   ██║██╔══██╗██╔══██╗██║   ██║      ▲ 1 needs you   ◆ 1 your turn   ● 2 working
╚██████╔╝██║  ██║██████╔╝██║   ██║     18 of 77 sessions · claude · codex · copilot
 ╚═════╝ ╚═╝  ╚═╝╚═════╝ ╚═╝   ╚═╝
╭─ SESSIONS · RECENT ────────────────────────────╮ ╭─ LIVE · WORK/API-GATEWAY ──────────────────╮
│ ▌ ▲ cl work/api-gateway                     2m │ │ Refactor batch runner                      │
│ ▌   Refactor batch runner            needs you │ │ ~/work/api-gateway                         │
│   ⠸ cx src/widgets                         40m │ │ claude · main · 46 msgs · 2m ago  ▲ needs you│
│     Evaluate the docs of this repo     working │ │                                            │
│   ◆ cl services/billing                     1h │ │ ▸ last prompt                              │
│     Add retry to the webhook queue   your turn │ │   now run the tests                        │
│   · cp work/docs-site                       3d │ │                                            │
│     Integrate Copilot in Actions               │ │ ▸ live output                              │
│                                                │ │   ● Bash(go test ./...)                    │
│                                                │ │     Do you want to proceed?                │
╰────────────────────────────────────────────────╯ ╰────────────────────────────────────────────╯
  ⏎  attach   i  here   w  window   n  new   1/2/3  agent   x  kill   /  filter   a  all   q  quit
```

## Why

Agent CLIs each ship a `--resume` picker, but every one of them only sees the
directory you're standing in. If you run several agents across several repos,
nothing tells you where they all are, which are still alive, or which one has
been sitting on a permission prompt for ten minutes. orbit is that missing view.

## Install

```sh
brew install tmux
go install github.com/sadrig91/orbit@latest
```

Requires macOS or Linux, Go 1.25+, and tmux. [Ghostty](https://ghostty.org) is
optional but gets you tab spawning and desktop notifications.

## Keys

| key     | action                                                    |
|---------|-----------------------------------------------------------|
| `⏎`     | attach — resumes the session first if it isn't running     |
| `i`     | attach in this terminal; returns to orbit when you detach  |
| `t`     | attach in a new Ghostty tab                                |
| `w`     | attach in a new Ghostty window                             |
| `s`     | summarise this session with a cheap model, then cache it   |
| `S`     | queue every visible session that has no summary yet        |
| `f`     | full-text search inside transcripts                        |
| `o`     | cycle sort: age / tokens / project / agent                 |
| `p`     | group the list under project headings                      |
| `n`     | new session, same agent, in the selected project's dir     |
| `1/2/3` | new claude / codex / copilot session in that dir           |
| `x`     | kill the tmux session (the transcript is untouched)        |
| `/`     | filter by title, path, branch or agent                     |
| `a`     | show everything (default hides untitled + older than 30d)  |
| `r`     | refresh now                                                |
| `q`     | quit the dashboard — running sessions carry on             |

`⏎` picks the best available: a Ghostty tab on macOS, otherwise in-place.
`/` filters titles and paths as you type; `f` searches message bodies and runs
on Enter. `esc` clears a search.

## Search and summaries

Titles are terse — "Check branch status against main" doesn't say which branch —
so `f` searches the transcript bodies themselves and shows the matching text in
the detail pane. Nothing is held in memory; the files are scanned on demand,
which across ~80 sessions takes under 200ms.

`s` summarises a session in two or three sentences and caches the result.

A summary records how much of the conversation it covers. When you continue a
session it goes **stale rather than invalid**: the detail pane says how many
messages it hasn't seen, and the next regeneration sends the existing summary
plus only the new messages. Input stays roughly constant no matter how long a
conversation runs, instead of re-reading the whole transcript every turn — which
on an active session would bill a full summarisation per prompt.

Rolling summaries compound their own omissions, so after five incremental
updates the next one rebuilds from the transcript. An update is also skipped in
favour of a rebuild when the unseen part is most of the conversation, where
building on the old text buys nothing.

Automatic regeneration (`auto = true`) is the only thing that spends money
without being asked, so it is guarded: it waits until a session is
`auto_min_new_messages` behind (default 8) and never fires while a turn is in
flight, since the transcript is still being written.

Summaries run through each provider's **own CLI** in non-interactive mode
(`claude -p`, `codex exec`, `copilot -p`), so the work is billed to that agent's
existing subscription and orbit needs no API keys. The command is configurable
per provider — point it at the cheapest model each one offers, since this is a
summarising job rather than a reasoning one:

```toml
[summary.claude]
command = ["claude", "-p", "--model", "claude-haiku-4-5-20251001", "--allowed-tools", ""]
```

Generation takes around ten seconds and runs in the background. The bar in the
header is global and measures *coverage* — how many of the sessions on screen
have a summary — so it advances only when one finishes, never on time elapsed.
`S` queues everything still missing one, two at a time, since each job is a
whole agent process and running more in parallel just makes each slower.
Ghostty also gets a native progress indicator on the tab while work is in hand.

Because summarising deliberately uses cheap models, and cheap models have the
smallest context windows, only a thin slice of the transcript is sent: the
opening of the conversation plus its tail, skipping the bulky tool output in
the middle. `max_input_chars` caps it (12000 ≈ 3k tokens), overridable per
provider if you point one at a bigger model.

Set `auto = true` under `[summary]` to summarise whatever you're sitting on
without pressing `s`. It's off by default because it spends tokens as you browse.

## State

| icon | meaning                                                       |
|------|---------------------------------------------------------------|
| `▲`  | needs you — parked on a permission prompt                      |
| `◆`  | your turn — the agent finished and is waiting                  |
| `●`  | working                                                        |
| `○`  | tmux session alive, but the agent has exited (just a shell)    |
| `·`  | not running — `⏎` resumes it                                   |

Each row also carries the session's token usage, read from whatever the agent
recorded — Claude's per-message `usage`, Codex's `total_token_usage`, Copilot's
`assistant_usage_events` table. `o` sorts by it.

State comes from joining each agent's own transcript with live tmux facts. For
Claude and Codex that's precise enough to tell "running a slow tool" apart from
"waiting on approval" — an unanswered tool call that stops advancing is a
permission prompt. Copilot keeps a database rather than an event stream, so its
live states are coarser.

## How it works

**Sessions** are read from each agent's own store, in parallel, cached on mtime
so a refresh is normally just `stat()` calls:

| agent | store | resume |
|-------|-------|--------|
| Claude Code | `~/.claude/projects/*/*.jsonl` | `claude --resume <id>` |
| Codex | `~/.codex/sessions/**/rollout-*.jsonl` | `codex resume <id>` |
| Copilot CLI | `~/.copilot/session-store.db` (sqlite3) | `copilot --resume=<id>` |

Claude's per-project directory name is lossy — `/` and `.` both become `-` — so
the working directory is read out of the records rather than decoded from the
path. Older Codex rollouts key the session as `id` rather than `session_id`, and
fall back to the uuid in the filename.

**tmux** runs on a private server (`tmux -L orbit`) with its own config at
`~/.config/orbit/tmux.conf`, installed from a copy embedded in the binary. It
never touches a normal `tmux`. The status bar is off and the prefix is moved to
`C-o` so it can't steal `C-b` from an agent's input line. Agents are launched by
typing into an interactive login shell rather than via `new-session <cmd>`,
because agents are often shell functions or version-manager shims that don't
exist in a bare `sh -c`.

**Tab titles** are pushed as `<short pwd> · <session title>` through tmux's
`set-titles`, configured to ignore `pane_title` so agents can't overwrite them.

**New tabs** are opened by sending Ghostty its own `cmd+T` via System Events.
Ghostty has no CLI action for this on macOS (`+new-window` is Linux-only), so
this is the only route that lands a tab in the current window instead of
spawning a detached one. It needs Accessibility permission; without it, use `i`.

**Notifications** use OSC 9 when a session flips to "needs you" or "your turn".

## Layout

```
cmd/orbit/          entry point: flags, wiring, --list/--json output
internal/
  config/           config.toml and its defaults
  format/           shared string and time helpers
  session/          the session model: agents, parsers, index, sorting
  tmux/             the private tmux server; knows nothing about agents
  search/           full-text search over transcript bodies
  summary/          cached, incrementally-updated summaries
  ui/               Bubble Tea model, rendering, logos, notifications
test/               integration tests that touch the real system
```

Dependencies run one way: `format` and `config` sit at the bottom and import
nothing local, `tmux` is deliberately ignorant of agents (it deals in names and
strings), `session` is the domain model, and `ui` composes the lot. Anything
needing both an agent and tmux — resuming a session, starting a new one — lives
in `ui`, which is the only layer entitled to know about both.

Unit tests sit beside the code they cover, as Go expects, so they can reach
unexported internals. `test/` is for integration tests that spawn a real tmux
server or read the actual session stores.

## Caveats

- tmux owns the scrollback, so your terminal's find-in-page only searches the
  visible pane inside a session. Use tmux copy-mode (`C-o [`).
- Closing a session tab detaches; it does not kill. Sessions accumulate until
  you `x` them.
- Reading only. orbit will not answer prompts on your behalf.

## Config

`~/.config/orbit/config.toml` is written with annotated defaults on first run:
icons, attach behaviour, notifications, `recent_days`, sort order, grouping,
spawn delays and the per-provider summary commands. Environment variables
(`ORBIT_ICONS`, `ORBIT_SPAWN_DELAY`, `ORBIT_TAB_DELAY`) still win over the file,
so a single run can be changed without editing it.

### Agent logos

Sessions are tagged `cl` / `cx` / `cp` by default, which works in any terminal.
Set `ORBIT_ICONS=logo` and orbit renders the actual Claude, OpenAI and Copilot
marks instead, as images, via the Kitty graphics protocol — supported by Ghostty
and Kitty. Check yours first:

```sh
orbit --probe-logos
```

The marks are transmitted once at startup and placed with Unicode placeholders,
so they flow with the text rather than being pinned to screen coordinates, and
occupy exactly the two columns the text tag did. tmux doesn't pass placeholders
through, so logos switch themselves off if orbit is running inside one.

Artwork is from [Simple Icons](https://simpleicons.org) (CC0), recoloured for a
dark terminal. Trademarks belong to their respective owners; the marks identify
which agent owns a session and imply no affiliation or endorsement.

Flags: `--inline`, `--window`, `--no-notify`, `--list`, `--json`, `--probe-logos`, `--version`.

## License

MIT
