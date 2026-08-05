package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sadrig91/orbit/internal/config"
	"github.com/sadrig91/orbit/internal/dispatch"
	"github.com/sadrig91/orbit/internal/tmux"
)

// runDispatch is `orbit dispatch <id>` — the headless half of the feature.
//
// It runs inside a tmux session on orbit's private server, put there by the
// dashboard, which is why the whole invocation is an id and nothing else: the
// command is typed into an interactive shell, and a prompt is arbitrary user
// text. Everything the run needs was written to the record first.
//
// This process is the run. It outlives the dashboard that started it, and the
// dashboard learns what it is doing only by reading the record it keeps
// updating.
func runDispatch(id string) int {
	rec, ok := dispatch.Load(id)
	if !ok {
		fmt.Fprintln(os.Stderr, "orbit: no dispatch record for", id)
		return 1
	}

	cfg, _ := config.Load() // defaults are already in place on an error

	// The pane is where you look while it runs; the log is what is left to
	// read after the pane has gone, which it does the moment the run ends. A
	// log that cannot be opened is not worth failing a run over.
	out := io.Writer(os.Stdout)
	if f, err := os.OpenFile(dispatch.LogPath(id), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
		defer f.Close()
		out = io.MultiWriter(os.Stdout, f)
	}

	// Ctrl-C in the pane, or the SIGHUP tmux sends when the session is killed,
	// should end the run rather than orphan the agent underneath it.
	ctx, stop := signal.NotifyContext(context.Background(),
		os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()

	err := dispatch.Run(ctx, rec, dispatch.Options{
		Timeout:        cfg.Dispatch.TimeoutDur(),
		AllowAllTools:  cfg.Dispatch.CopilotAllowAllTools,
		PermissionMode: cfg.Dispatch.PermissionMode,
		// Codex does not accept a session id, so orbit only learns its
		// thread_id from the first event. Recording it on the tmux session is
		// what lets the next scan join this run to the transcript it is
		// writing, instead of guessing by directory and modification time.
		Link: func(sessionID string) { tmux.Link(rec.Tmux, sessionID) },
	}, out)

	if err != nil {
		rec.Status, rec.Err, rec.Ended = dispatch.Failed, err.Error(), time.Now()
		dispatch.Save(rec)
		fmt.Fprintln(out, "\n✗ "+err.Error())
		if errors.Is(err, dispatch.ErrCopilotConsent) {
			fmt.Fprintln(out, "  set dispatch.copilot_allow_all_tools in "+config.Path())
		}
	}

	// The pane goes with the run. This is what makes a finished dispatch
	// resolve to "your turn" and Enter resume the conversation interactively —
	// a pane left behind would be a dead shell for Enter to attach to instead.
	// It is deliberately last: the record is already written, and the log
	// holds everything that was on screen.
	//
	// Leaving the pane up on a failure would be kinder to debug, and is not
	// done, because "sometimes it disappears and sometimes it doesn't" is a
	// worse thing to have to explain than one rule with a log file.
	if rec.Tmux != "" {
		tmux.Kill(rec.Tmux)
	}
	if rec.Status == dispatch.Failed {
		return 1
	}
	return 0
}
