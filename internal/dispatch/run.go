package dispatch

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Options are the parts of a run that come from config rather than from the
// record, plus the seams the dashboard fills in.
type Options struct {
	// Timeout ends a run that will not. Copilot has been measured sitting for
	// four minutes on a single shell call with nothing arriving on the stream,
	// so this is load-bearing rather than defensive.
	Timeout time.Duration

	// AllowAllTools opts copilot in to the only mode its CLI will run
	// non-interactively. Nothing else reads it; codex is governed entirely by
	// the user's own settings.
	AllowAllTools bool

	// PermissionMode overrides claude's --permission-mode for this run. Empty
	// inherits the user's settings, which is the default — see claude.go.
	PermissionMode string

	// Link is called the first time the agent reveals its session id, so the
	// caller can record it somewhere the dashboard will find — for codex that
	// is the only way the run can be joined to a session at all. Optional.
	Link func(sessionID string)
}

// ErrUnknownAgent is returned for an agent orbit cannot dispatch.
var ErrUnknownAgent = errors.New("dispatch: unknown agent")

// ErrCopilotConsent is returned when a copilot dispatch is asked for without
// the setting that permits it. Separate from a plain error because the
// dashboard turns it into an explanation rather than a failure.
var ErrCopilotConsent = errors.New("dispatch: copilot needs allow_all_tools")

// Run drives one dispatch to completion, updating its record as the agent's
// own event stream arrives.
//
// It is the whole of what `orbit dispatch` does. The record is saved on every
// meaningful change rather than at the end, because the dashboard is reading
// it every couple of seconds and a run that only reported when it finished
// would be indistinguishable from one that had hung.
//
// narrate receives a human-readable account of the run. In practice that is
// the tmux pane the runner lives in, and a log file beside the record — the
// pane dies with the run, and the log is what is left to read afterwards.
func Run(ctx context.Context, r *Record, opts Options, narrate io.Writer) error {
	argv, err := argvFor(r, opts)
	if err != nil {
		return err
	}

	say := func(format string, args ...any) {
		if narrate != nil {
			fmt.Fprintf(narrate, format+"\n", args...)
		}
	}
	say("orbit dispatch · %s · %s", r.Agent, r.Cwd)
	say("› %s", oneLine(r.Prompt))
	say("")

	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = r.Cwd
	// Its own process group, so a timeout or a kill takes the agent's children
	// — the shells it spawns, the servers it starts — with it. Killing only
	// the CLI leaves those attached to the pane and running.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	// A grace period after SIGTERM, then the group is killed outright.
	cmd.WaitDelay = 5 * time.Second

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", r.Agent, err)
	}

	// Claude takes its prompt as a stream-json message on stdin and keeps
	// reading, because that is also where control responses go. The other two
	// take the prompt as an argument and want stdin closed — codex otherwise
	// waits on it, announcing "Reading additional input from stdin…".
	w := &syncWriter{w: stdin}
	if r.Agent == "claude" {
		if _, err := w.Write(claudeInput(r.Prompt)); err != nil {
			cmd.Process.Kill()
			cmd.Wait()
			return fmt.Errorf("send prompt: %w", err)
		}
	} else {
		stdin.Close()
	}

	res := consume(stdout, r, w, opts, say)

	// The shutdown order matters, and getting it wrong deadlocks the run.
	//
	// Claude does not close its stdout when the turn ends: with a stream-json
	// input it goes on waiting for another message, so reading to EOF before
	// closing stdin waits for an ending that only closing stdin can cause.
	// Measured as a four-minute hang on a run that had finished its work in
	// eleven seconds. So consume stops at the terminal event, and stdin closes
	// here — which is what tells the CLI to exit.
	w.Close()
	// Then drain whatever it writes on the way out. Stopping the read while
	// the child still has something to say would refill the pipe buffer and
	// block it forever, which is the same deadlock from the other end.
	io.Copy(io.Discard, stdout) //nolint:errcheck // a broken pipe here is the child exiting
	waitErr := cmd.Wait()

	return finish(ctx, r, res, waitErr, stderr.String(), say)
}

// result is what the stream said happened, as distinct from what the process
// exit code said — the two disagree on purpose in the handoff case.
type result struct {
	done     bool
	handedTo string // the tool call the run was handed back on, if any
	err      string
}

// consume reads the agent's stream until it says the turn is over, updating
// the record as it goes. It stops at the terminal event rather than at EOF —
// see the shutdown comment in Run for why there may not be one.
func consume(stdout io.Reader, r *Record, w *syncWriter, opts Options, say func(string, ...any)) result {
	parse := parserFor(r.Agent)
	sc := bufio.NewScanner(stdout)
	// Agent events carry whole assistant messages and tool inputs; the default
	// 64KB line limit is comfortably exceeded by a single file edit.
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	var res result
	var lastNote, lastActivity string
	for sc.Scan() {
		line := sc.Bytes()
		st, ok := parse(line)
		if !ok {
			continue
		}
		// Both CLIs report the same thing twice, in different ways, and saying
		// it twice reads like the run did it twice. Claude's final assistant
		// message and its result event carry identical text; codex emits
		// item.started and item.completed for one command.
		if st.note != "" && st.note == lastNote {
			st.note = ""
		} else if st.note != "" {
			lastNote = st.note
		}
		if st.activity != "" && st.activity == lastActivity && st.note == "" {
			continue // the same command completing, with nothing new to add
		}
		if st.activity != "" {
			lastActivity = st.activity
		}

		if st.sessionID != "" && st.sessionID != r.SessionID {
			r.SessionID = st.sessionID
			if opts.Link != nil {
				opts.Link(st.sessionID)
			}
			Save(r)
		}

		switch {
		case st.ask != nil && st.ask.tool == "":
			// A control request orbit does not handle. It still has to be
			// answered: every one of them blocks the CLI until something
			// replies, so ignoring an unknown request hangs the agent.
			w.Write(claudeAck(st.ask.requestID))

		case st.ask != nil:
			// The handoff. See claudeHandoff for why this interrupts rather
			// than merely denying.
			say("▲ needs you — %s", st.ask.detail)
			say("  handed back; ⏎ in orbit takes it over")
			w.Write(claudeHandoff(st.ask.requestID))
			res.handedTo = st.ask.detail
			r.Status, r.Pending, r.Activity = NeedsYou, st.ask.detail, ""
			Save(r)

		case st.err != "":
			res.err = st.err

		case st.done:
			res.done = true
			if st.note != "" {
				say("%s", st.note)
			}

		case st.activity != "":
			say("● %s", st.activity)
			if st.note != "" {
				// Both, when a step carries both: a codex command that exited
				// non-zero is reported as activity plus the exit code, and the
				// exit code is the interesting half.
				say("  %s", st.note)
			}
			// A run that has been handed back keeps its pending tool on
			// screen: claude emits a little more before the interrupt lands,
			// and letting that overwrite the reason would lose the one thing
			// worth reading.
			if r.Status == Running {
				r.Activity = st.activity
				Save(r)
			}

		case st.note != "":
			say("  %s", st.note)
		}

		// Stop at the terminal event rather than reading to EOF. See the
		// shutdown comment in Run: for claude there is no EOF to wait for.
		if res.done || res.err != "" {
			break
		}
	}
	if err := sc.Err(); err != nil && res.err == "" && !res.done && res.handedTo == "" {
		res.err = "reading the event stream: " + err.Error()
	}
	return res
}

// finish reconciles what the stream said with how the process exited, and
// writes the terminal record.
//
// The two disagree by design after a handoff: claude exits 1 with subtype
// error_during_execution and terminal_reason aborted_streaming, because from
// its point of view the turn was interrupted. From orbit's it did exactly what
// it was told. The stream is therefore believed first, and the exit code only
// consulted when the stream said nothing conclusive.
func finish(ctx context.Context, r *Record, res result, waitErr error, stderr string, say func(string, ...any)) error {
	r.Ended = time.Now()

	switch {
	case res.handedTo != "":
		r.Status, r.Pending = NeedsYou, res.handedTo
		Save(r)
		return nil

	case res.err != "":
		r.Status, r.Err = Failed, res.err

	// Before the context checks, not after. The stream is the account of what
	// the agent did; the context only says what happened to the process
	// afterwards, and a run interrupted a moment after it finished its work
	// still finished its work. Ordering these the other way round reported a
	// completed run as "cancelled" the first time this met a real CLI.
	case res.done:
		r.Status, r.Activity = Done, ""

	case ctx.Err() == context.DeadlineExceeded:
		r.Status, r.Err = Failed, "timed out"

	case ctx.Err() != nil:
		r.Status, r.Err = Failed, "cancelled"

	case waitErr != nil:
		// No result event and a non-zero exit: the CLI failed before it had
		// anything to say. Its stderr is the only account of why.
		r.Status, r.Err = Failed, firstProblem(stderr, waitErr)

	default:
		// The stream ended cleanly without a terminal event. Nothing is
		// obviously wrong and the transcript is there to resume, so this is
		// reported as finished rather than invented as a failure.
		r.Status = Done
	}

	if r.Status == Failed {
		say("")
		say("✗ %s", r.Err)
	} else {
		say("")
		say("◆ done — ⏎ in orbit resumes it")
	}
	Save(r)
	return nil
}

// firstProblem picks the most useful line of a failed CLI's stderr, falling
// back to the exit status. Agents print startup chatter to stderr, so the last
// non-empty line is a better guess at the complaint than the first.
func firstProblem(stderr string, waitErr error) string {
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return oneLine(l)
		}
	}
	return waitErr.Error()
}

func argvFor(r *Record, opts Options) ([]string, error) {
	switch r.Agent {
	case "claude":
		return claudeArgv(r, opts.PermissionMode), nil
	case "codex":
		return codexArgv(r), nil
	case "copilot":
		if !opts.AllowAllTools {
			return nil, ErrCopilotConsent
		}
		return copilotArgv(r, true), nil
	}
	return nil, fmt.Errorf("%w: %s", ErrUnknownAgent, r.Agent)
}

// syncWriter serialises writes to the agent's stdin and tolerates a closed
// pipe.
//
// Both matter on the claude path. The prompt is written from Run and control
// responses from consume, and a half-written control response is a hung agent
// — it blocks until it gets a complete, well-formed answer. And a CLI that has
// already exited turns the next write into EPIPE, which is normal at the end
// of a run and not worth failing over.
type syncWriter struct {
	mu     sync.Mutex
	w      io.WriteCloser
	closed bool
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, os.ErrClosed
	}
	return s.w.Write(p)
}

func (s *syncWriter) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return s.w.Close()
}
