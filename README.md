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

`s` summarises a session in two or three sentences and caches the result. The
cache key includes the session's last event, so a summary is computed once per
conversation state and quietly regenerates if you continue the session.

Summaries run through each provider's **own CLI** in non-interactive mode
(`claude -p`, `codex exec`, `copilot -p`), so the work is billed to that agent's
existing subscription and orbit needs no API keys. The command is configurable
per provider — point it at the cheapest model each one offers, since this is a
summarising job rather than a reasoning one:

```toml
[summary.claude]
command = ["claude", "-p", "--model", "claude-haiku-4-5-20251001", "--allowed-tools", ""]
```

Generation takes around ten seconds, so it runs in the background behind a
progress bar. The providers report no progress of their own, so the bar is
elapsed time against a rolling estimate that adapts to your machine and models,
capped short of full — a bar sitting at 100% while still working reads as a
hang. Ghostty also gets a native progress indicator on the tab.

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
