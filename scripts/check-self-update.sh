#!/usr/bin/env bash
#
# Drives the self-update end to end, in a real terminal, against the real
# latest release — the one path the Go tests can't reach, because the final
# step is orbit exec'ing over itself and that needs a pty.
#
#   ./scripts/check-self-update.sh
#
# What it proves, in order:
#   1. an out-of-date orbit finds the release and says it is updating
#   2. the binary on disk is replaced, verified against published checksums
#   3. orbit execs the new build in place of itself — same process, new code
#
# Nothing outside a scratch directory is touched: the test orbit runs with its
# own HOME, so your config, your update cache and your real install are not
# involved. It never runs brew.

set -uo pipefail

say() { printf '\033[36m%s\033[0m\n' "$*"; }
ok() { printf '\033[32m✓ %s\033[0m\n' "$*"; }
bad() {
  printf '\033[31m✗ %s\033[0m\n' "$*"
  FAILED=1
}
FAILED=0

cd "$(dirname "$0")/.." || exit 1
command -v go >/dev/null || {
  echo "go is required"
  exit 1
}
command -v expect >/dev/null || {
  echo "expect is required (it ships with macOS)"
  exit 1
}
[ -t 1 ] || echo "note: no tty on stdout — expect still allocates its own pty"

# A scratch HOME, deliberately NOT under $TMPDIR: orbit treats a binary in the
# temp directory as `go run` and refuses to update it, which would make this
# script pass by doing nothing.
SCRATCH="$PWD/.selfupdate-check"
rm -rf "$SCRATCH"
mkdir -p "$SCRATCH/bin"
BIN="$SCRATCH/bin/orbit"
LOG="$SCRATCH/session.log"
trap 'rm -rf "$SCRATCH"' EXIT

# Built before HOME is redirected, so the Go build and module caches are the
# real ones — a scratch HOME here would re-download the whole module graph.
say "building an out-of-date orbit (v0.0.1) at $BIN"
go build -ldflags "-X main.version=v0.0.1" -o "$BIN" ./cmd/orbit || {
  bad "build failed"
  exit 1
}

LATEST=$(curl -fsSL -H 'User-Agent: orbit' \
  https://api.github.com/repos/SadriG91/orbit/releases/latest |
  sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1)
[ -n "$LATEST" ] || {
  bad "could not reach the GitHub releases API"
  exit 1
}
say "latest release is $LATEST — expecting orbit to install it"

# expect drives the TUI: it provides the pty, waits for each stage, and — the
# part that matters — checks the process is still alive after the restart.
# syscall.Exec keeps the pid, so a live pid after "restarting" means the exec
# replaced the program rather than the program dying.
# HOME is redirected for the run itself, and only here. orbit keeps its
# once-a-day answer in ~/.cache/orbit/update.json, so without this the test
# reads whatever the real one happens to say — which is how this script first
# "failed": it installed the version cached from an earlier run rather than
# the release it had just looked up.
HOME="$SCRATCH" expect -f - "$BIN" "$LOG" "$LATEST" <<'EXPECT'
set bin     [lindex $argv 0]
set logfile [lindex $argv 1]
set latest  [lindex $argv 2]

log_file -noappend $logfile
set timeout 180

# The pty must be given a size before spawning. expect's default pty reports
# 0x0, and Bubble Tea renders a zero-width view into it — a blank screen with
# the status line orbit is trying to show nowhere in it. Wide enough here that
# the footer isn't truncated mid-message.
set stty_init "rows 45 cols 150"

spawn -noecho $bin
set pid [exp_pid]
puts "\n\[pid $pid]"

expect {
    -re {updating orbit to v[0-9]+\.[0-9]+\.[0-9]+} { puts "\n\[saw: updating]" }
    timeout { puts "\n\[FAIL: never announced an update]"; exit 2 }
    eof     { puts "\n\[FAIL: orbit exited before updating]"; exit 2 }
}

expect {
    -re {updated to .* restarting} { puts "\n\[saw: restarting]" }
    -re {failed}                   { puts "\n\[FAIL: update reported a failure]"; exit 3 }
    timeout { puts "\n\[FAIL: update never finished]"; exit 3 }
    eof     { puts "\n\[FAIL: orbit exited mid-update]"; exit 3 }
}

# Give the pause plus the exec time to happen, then confirm the process is
# still there. If exec had failed, main would have died and taken the pid.
sleep 6
if {[catch {exec kill -0 $pid}]} {
    puts "\n\[FAIL: pid $pid is gone — the exec did not take]"
    exit 4
}
puts "\n\[pid $pid still alive after the restart]"

# The relaunched orbit is a fresh dashboard; quit it the normal way.
send "q"
expect {
    eof     { puts "\n\[quit cleanly]" }
    timeout { puts "\n\[warn: did not quit on q]"; exec kill -TERM $pid }
}
exit 0
EXPECT
STAGE=$?

echo
case $STAGE in
0) ok "the TUI showed the update and survived the restart" ;;
2) bad "no update was offered or announced" ;;
3) bad "the update did not complete" ;;
4) bad "orbit did not exec the new binary (the process died)" ;;
*) bad "expect exited $STAGE" ;;
esac

# The independent check: what is actually on disk now?
GOT=$("$BIN" --version 2>/dev/null | tr -d '\r')
WANT_A="orbit $LATEST"
WANT_B="orbit ${LATEST#v}" # releases before v0.2.2 print no leading v
if [ "$GOT" = "$WANT_A" ] || [ "$GOT" = "$WANT_B" ]; then
  ok "binary on disk is now $GOT (was v0.0.1)"
else
  bad "binary reports '$GOT', expected '$WANT_A'"
fi

# Isolation is a claim this script makes, so it checks it rather than
# asserting it in a comment: the run's update cache must be in the scratch
# HOME. If it isn't, the version installed above came from whatever the real
# cache held and this whole run proved nothing.
if [ -f "$SCRATCH/.cache/orbit/update.json" ]; then
  ok "ran against an isolated HOME ($(cat "$SCRATCH/.cache/orbit/update.json"))"
else
  bad "no update cache in the scratch HOME — the run used your real one"
fi

# And no debris: a partial download must never be left beside the binary.
if compgen -G "$SCRATCH/bin/.orbit-update-*" >/dev/null; then
  bad "left a temp file behind in $SCRATCH/bin"
else
  ok "no temp files left behind"
fi

echo
if [ "$FAILED" -eq 0 ]; then
  printf '\033[32m%s\033[0m\n' "PASS — self-update works end to end"
else
  printf '\033[31m%s\033[0m\n' "FAIL — see above; the session transcript was at $LOG"
  # Keep the log for inspection when something went wrong.
  trap - EXIT
  say "scratch dir kept at $SCRATCH"
fi
exit "$FAILED"
