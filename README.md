# orbit

A dashboard for the coding-agent sessions running on your machine — Claude Code,
Codex and GitHub Copilot CLI, across every project. Meant to live in its own
terminal tab.

It answers the question you can't answer by staring at a wall of tabs: **which
session is blocked on me right now?** Sessions run inside tmux, so closing a tab
never kills work.

```
 orbit  77 sessions · 2 working · 1 needs you
──────────────────────────────────────────────────────────────────────────
▌ ▲ cl work/api-gateway               2m │ Refactor batch runner
▌   Refactor batch runner      needs you │ ~/work/api-gateway
  ◆ cx src/widgets                   40m │ codex · main · 12 msgs · 40m ago
    Evaluate the docs of this repo  your turn │
  · cp work/docs-site                 3d │ last prompt
    Integrate Copilot in Actions        │   now run the tests
──────────────────────────────────────────────────────────────────────────
 ⏎ attach · i here · n new · 1/2/3 cl/cx/cp · x kill · / filter · a all · q
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

Requires macOS or Linux, Go 1.24+, and tmux. [Ghostty](https://ghostty.org) is
optional but gets you tab spawning and desktop notifications.

## Keys

| key     | action                                                    |
|---------|-----------------------------------------------------------|
| `⏎`     | attach — resumes the session first if it isn't running     |
| `i`     | attach in this terminal; returns to orbit when you detach  |
| `t`     | attach in a new Ghostty tab                                |
| `w`     | attach in a new Ghostty window                             |
| `n`     | new session, same agent, in the selected project's dir     |
| `1/2/3` | new claude / codex / copilot session in that dir           |
| `x`     | kill the tmux session (the transcript is untouched)        |
| `/`     | filter by title, path, branch or agent                     |
| `a`     | show everything (default hides untitled + older than 30d)  |
| `r`     | refresh now                                                |
| `q`     | quit the dashboard — running sessions carry on             |

`⏎` picks the best available: a Ghostty tab on macOS, otherwise in-place.

## State

| icon | meaning                                                       |
|------|---------------------------------------------------------------|
| `▲`  | needs you — parked on a permission prompt                      |
| `◆`  | your turn — the agent finished and is waiting                  |
| `●`  | working                                                        |
| `○`  | tmux session alive, but the agent has exited (just a shell)    |
| `·`  | not running — `⏎` resumes it                                   |

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

| env                 | default | meaning                                   |
|---------------------|---------|-------------------------------------------|
| `ORBIT_SPAWN_DELAY` | `900ms` | wait before typing into a new tmux pane    |
| `ORBIT_TAB_DELAY`   | `1s`    | wait before typing into a new Ghostty tab  |

Flags: `--inline`, `--window`, `--no-notify`, `--list`, `--version`.

## License

MIT
