# orbit

A terminal dashboard for Claude Code, Codex, and GitHub Copilot CLI sessions
across all your projects.

Orbit shows which agents are working, finished, or waiting for you. Sessions
run inside tmux, so closing Orbit or a terminal tab does not stop them.

```text
 ██████╗ ██████╗ ██████╗ ██╗████████╗
██╔═══██╗██╔══██╗██╔══██╗██║╚══██╔══╝
██║   ██║██████╔╝██████╔╝██║   ██║
██║   ██║██╔══██╗██╔══██╗██║   ██║      ▲ needs attention 1 · ● working 2
╚██████╔╝██║  ██║██████╔╝██║   ██║     18 of 77 sessions shown
 ╚═════╝ ╚═╝  ╚═╝╚═════╝ ╚═╝   ╚═╝
╭─ ▶ SESSIONS · RECENT ────────────────────────╮ ╭─ PREVIEW · WORK/API ────────────────────╮
│ ▌ ▲ cl work/api                           2m │ │ Refactor batch runner                   │
│ ▌   Refactor batch runner    needs attention │ │ ~/work/api                              │
│   ⠸ cx src/widgets                      40m │ │ claude · main · 46 msgs · 2m ago       │
│     Evaluate the docs              working │ │                                         │
│   ◆ cl services/billing                  1h │ │ ▸ last prompt                           │
│     Add retry to webhook queue     finished │ │   now run the tests                     │
╰─────────────────────────────────────────────╯ ╰─────────────────────────────────────────╯
  ⏎ attach   tab drive   n new   d dispatch   / filter   ? shortcuts
```

## Install

macOS with Homebrew:

```sh
brew install sadrig91/tap/orbit
```

Linux, or without Homebrew:

```sh
go install github.com/sadrig91/orbit/cmd/orbit@latest
```

Prebuilt binaries are also available from
[GitHub Releases](https://github.com/SadriG91/orbit/releases).

Orbit requires tmux 3.4 or newer. Ghostty 1.3+ or iTerm2 is optional and adds
native tab opening and desktop notifications.

Run it from any directory:

```sh
orbit
```

Orbit checks for updates at most once a day. Disable this in
`~/.config/orbit/config.toml`:

```toml
[update]
auto = false
```

You can also use `orbit --no-update` or `ORBIT_NO_UPDATE=1` for one run.

## Using Orbit

The left pane lists sessions. The right pane shows details or a live terminal.
The `▶` marker shows which pane owns keyboard input.

- Press `Enter` to resume or attach to the selected session.
- Press `Tab` to drive the selected agent inside Orbit. If it is dormant,
  Orbit resumes it first.
- Press `Tab` or `Ctrl+G` to return from the live terminal to the session list.
- Press `[` or `]` to jump directly between sessions needing attention.
- Press `?` at any time for the complete shortcut reference.

When a live terminal is focused, `Ctrl+F` toggles full screen. `Ctrl+Alt+-`
makes the detail pane narrower and `Ctrl+Alt++` makes it wider from either side
of the split. Mouse-wheel scrollback shows how many lines you are above the
live output.

### Keys

| Key | Action |
|---|---|
| `Enter` | Attach or resume the selected session |
| `Tab` | Switch between the session list and live terminal |
| `Ctrl+Alt+-` / `Ctrl+Alt++` | Resize the detail or terminal pane |
| `[` / `]` | Previous / next session needing attention |
| `j` / `k`, arrows | Move through sessions |
| `g` / `G` | First / last session |
| `/` | Filter titles, paths, branches, and agents |
| `f` | Search inside transcripts |
| `Esc` | Clear search or dismiss a persistent error |
| `o` / `p` | Cycle sorting / grouping |
| `a` | Toggle recent and all sessions |
| `n` | Compose a new interactive session |
| `d` | Compose a headless task dispatch |
| `R` / `L` | Retry the selected dispatch / open its run log |
| `s` / `S` | Summarise one / all visible sessions |
| `x` | Confirm and kill the selected tmux session |
| `r` | Refresh |
| `D` | Open diagnostics |
| `?` | Open shortcut help |
| `q` | Quit; running sessions continue |

Attach overrides are also available: `i` attaches in the current terminal,
`t` opens a tab, and `w` opens a window. Orbit reuses an existing tab when it
can find one instead of opening a duplicate.

### Session state

| Mark | Meaning |
|---|---|
| `▲` | Needs attention, such as a permission prompt |
| `◆` | Finished its last response |
| `●` | Working |
| `○` | tmux is alive, but the agent exited |
| `·` | Not running; attach to resume it |

## New sessions and dispatch

Press `n` to start an interactive agent. The composer lets you choose the
agent and directory, and works even when the dashboard is empty. Use
Left/Right to select an agent, `Tab` to move to the directory, and Up/Down to
choose a recent project.

Press `d` to run a task headlessly. Its composer keeps the session list visible
and preserves the draft until launch succeeds:

- `Enter` adds a task line; `Ctrl+Enter` or `Alt+Enter` opens the review screen.
- `Tab` or `Shift+Tab` moves between the task and directory.
- Up/Down selects a recent project directory.
- A leading `@claude`, `@codex`, or `@copilot` is the only agent selector; without one, Orbit uses the selected session's agent.

For example:

```text
@codex can you check feature X?
```

A dispatch appears in the normal session list immediately and survives quitting
Orbit. Its detail view shows timing, recent activity, and the final result.
Attach to take it over, press `x` to cancel it, `R` to retry it, or `L` to read
the complete run log.

Claude dispatches stop and show `▲` when a tool needs approval; Orbit does not
approve it for you. Codex and Copilot do not expose an approval channel in
their non-interactive modes. Copilot additionally requires explicit opt-in:

```toml
[dispatch]
timeout = "30m"
copilot_allow_all_tools = false
```

## Search and summaries

`/` filters visible metadata as you type. `f` searches transcript contents
after you press Enter. `Esc` clears either search.

`s` creates a short cached summary of the selected session. `S` queues all
visible sessions without summaries. Orbit uses each provider's installed CLI,
so no separate API key is needed. Automatic summaries are off by default
because they spend tokens while you browse:

```toml
[summary]
auto = true
auto_min_new_messages = 8
max_input_chars = 12000
```

Provider commands can be overridden when you want a different model:

```toml
[summary.codex]
command = ["codex", "exec", "--ephemeral", "--model", "your-model"]
```

## Configuration and accessibility

Orbit creates an annotated config at `~/.config/orbit/config.toml`. It covers
attach behavior, notifications, recent-session limits, sort and grouping,
dispatch, updates, summaries, and agent icons. Existing settings are preserved
when new defaults are added.

Useful environment settings:

| Variable | Effect |
|---|---|
| `NO_COLOR=1` | Remove ANSI styling and use text agent tags |
| `ORBIT_REDUCED_MOTION=1` | Replace animated indicators with a static dot |
| `ORBIT_ICONS=text` | Always use `cl`, `cx`, and `cp` tags |
| `ORBIT_NO_UPDATE=1` | Disable the update check for this run |

Ghostty and Kitty can display agent logos through the Kitty graphics protocol.
Other terminals automatically use text tags. Run `orbit --probe-logos` to
check support.

Errors stay visible until `Esc` dismisses them. Press `D` for diagnostics about
the latest scan, preview mode, tmux and agent availability, stack-dump path,
and most recent error. Inside diagnostics, `r` refreshes and `c` clears the
recorded error.

## Notes

- Orbit uses a private tmux server and does not change your normal tmux setup.
- Closing a tab detaches; it does not kill the session. Use `x` when you intend
  to stop one. The transcript and cached summary remain available.
- tmux owns live-pane scrollback. Use its copy mode (`Ctrl+O`, then `[`) for
  selection and search.
- Flags: `--inline`, `--window`, `--no-notify`, `--list`, `--json`,
  `--probe-logos`, `--no-update`, and `--version`.

## License

MIT
