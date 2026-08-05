package dispatch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These run a real subprocess over a real pipe rather than a fake stream. The
// two things most likely to break are the handshake and the shutdown — an
// unanswered control request hangs the agent, an unclosed stdin hangs the
// runner — and neither is visible to a test that feeds bytes to a parser.

// fakeAgent installs a script named agent on PATH and returns nothing; every
// test that needs one calls this first. The script is written to a temp dir
// placed at the front of PATH, so exec's own lookup finds it exactly the way
// it would find the real CLI.
func fakeAgent(t *testing.T, agent, script string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, agent)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// blockRecordDir puts a plain file where the record directory should be, so
// every write into it fails.
func blockRecordDir(t *testing.T) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(Dir()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Dir(), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func newRecord(t *testing.T, agent string) *Record {
	t.Helper()
	return &Record{
		ID:        NewID(),
		Agent:     agent,
		SessionID: "sess-1",
		Cwd:       t.TempDir(),
		Prompt:    "look at feature X",
		Status:    Running,
		Started:   time.Now(),
	}
}

func TestRunClaudeToCompletion(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fakeAgent(t, "claude", `
read -r prompt
echo "$prompt" > "$ORBIT_TEST_PROMPT"
printf '%s\n' '{"type":"system","subtype":"init","session_id":"claude-said-this"}'
printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"go test ./..."}}]}}'
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"tests pass"}'
`)
	promptFile := filepath.Join(t.TempDir(), "prompt")
	t.Setenv("ORBIT_TEST_PROMPT", promptFile)

	rec := newRecord(t, "claude")
	var log bytes.Buffer
	if err := Run(context.Background(), rec, Options{}, &log); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if rec.Status != Done {
		t.Errorf("status = %q, want %q (log: %s)", rec.Status, Done, log.String())
	}
	if rec.Activity != "" {
		t.Errorf("a finished run kept activity %q", rec.Activity)
	}
	if !strings.Contains(log.String(), "Bash: go test ./...") {
		t.Errorf("the tool call was not narrated:\n%s", log.String())
	}

	// The prompt goes in as a stream-json user message, not as an argument.
	sent, err := os.ReadFile(promptFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sent), `"look at feature X"`) ||
		!strings.Contains(string(sent), `"type":"user"`) {
		t.Errorf("prompt arrived as %s", sent)
	}
}

// TestRunClaudeHandsOff is the feature the design turns on: the moment claude
// asks for permission, the run stops and the session is marked for a person.
func TestRunClaudeHandsOff(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fakeAgent(t, "claude", `
read -r prompt
printf '%s\n' '{"type":"system","subtype":"init","session_id":"sess-1"}'
printf '%s\n' '{"type":"control_request","request_id":"req-42","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"rm -rf build"},"decision_reason":"This command requires approval"}}'
read -r answer
echo "$answer" > "$ORBIT_TEST_ANSWER"
# Claude really does exit non-zero after an interrupt; a handoff must not be
# reported as a failure because of it.
exit 1
`)
	answerFile := filepath.Join(t.TempDir(), "answer")
	t.Setenv("ORBIT_TEST_ANSWER", answerFile)

	rec := newRecord(t, "claude")
	var log bytes.Buffer
	if err := Run(context.Background(), rec, Options{}, &log); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if rec.Status != NeedsYou {
		t.Errorf("status = %q, want %q (log: %s)", rec.Status, NeedsYou, log.String())
	}
	if !strings.Contains(rec.Pending, "rm -rf build") {
		t.Errorf("pending = %q, want the command that asked", rec.Pending)
	}
	if !strings.Contains(rec.Pending, "This command requires approval") {
		t.Errorf("pending = %q, want the reason claude gave", rec.Pending)
	}
	if rec.Err != "" {
		t.Errorf("a handoff was recorded as an error: %q", rec.Err)
	}

	answer, err := os.ReadFile(answerFile)
	if err != nil {
		t.Fatal(err)
	}
	// interrupt is what stops the turn dead. Without it claude carries on and
	// talks its way around the refusal, which is the outcome this design
	// exists to avoid.
	for _, want := range []string{`"request_id":"req-42"`, `"behavior":"deny"`, `"interrupt":true`} {
		if !strings.Contains(string(answer), want) {
			t.Errorf("answer %s missing %s", answer, want)
		}
	}
}

// TestRunAnswersUnknownControlRequests: every control request blocks the CLI
// until something replies, so one orbit does not understand still has to be
// acknowledged. A run that hangs here would hang until the timeout.
func TestRunAnswersUnknownControlRequests(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fakeAgent(t, "claude", `
read -r prompt
printf '%s\n' '{"type":"control_request","request_id":"req-9","request":{"subtype":"invented_later"}}'
read -r answer
echo "$answer" > "$ORBIT_TEST_ANSWER"
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"fine"}'
`)
	answerFile := filepath.Join(t.TempDir(), "answer")
	t.Setenv("ORBIT_TEST_ANSWER", answerFile)

	rec := newRecord(t, "claude")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := Run(ctx, rec, Options{}, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rec.Status != Done {
		t.Errorf("status = %q, want %q", rec.Status, Done)
	}
	answer, err := os.ReadFile(answerFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(answer), `"request_id":"req-9"`) {
		t.Errorf("unknown control request went unanswered: %s", answer)
	}
}

// TestRunCodexLinksLateID covers the one agent that will not accept a session
// id. Codex reveals its thread_id on the first event, and until orbit hears it
// the run cannot be joined to a session at all.
func TestRunCodexLinksLateID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fakeAgent(t, "codex", `
printf '%s\n' '{"type":"thread.started","thread_id":"019fd26c-d00c-7413-a16b-956a44842bd0"}'
printf '%s\n' '{"type":"item.completed","item":{"type":"command_execution","command":"ls","exit_code":0,"status":"completed"}}'
printf '%s\n' '{"type":"turn.completed","usage":{"input_tokens":1}}'
`)
	rec := newRecord(t, "codex")
	rec.SessionID = "" // codex starts without one, by construction

	var linked []string
	err := Run(context.Background(), rec, Options{
		Link: func(id string) { linked = append(linked, id) },
	}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	const want = "019fd26c-d00c-7413-a16b-956a44842bd0"
	if rec.SessionID != want {
		t.Errorf("SessionID = %q, want %q", rec.SessionID, want)
	}
	if len(linked) != 1 || linked[0] != want {
		t.Errorf("Link called with %v, want exactly [%s]", linked, want)
	}
}

func TestRunTimesOut(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// Nothing on the stream and no exit — the copilot failure mode, which sat
	// for four minutes in a measured run.
	fakeAgent(t, "codex", "sleep 60")

	rec := newRecord(t, "codex")
	start := time.Now()
	if err := Run(context.Background(), rec, Options{Timeout: 300 * time.Millisecond}, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if took := time.Since(start); took > 20*time.Second {
		t.Errorf("the timeout took %v to take effect", took)
	}
	if rec.Status != Failed || rec.Err != "timed out" {
		t.Errorf("status = %q err = %q, want failed / timed out", rec.Status, rec.Err)
	}
}

func TestRunReportsAStartupFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fakeAgent(t, "codex", `
echo "warming up" >&2
echo "not logged in — run codex login" >&2
exit 1
`)
	rec := newRecord(t, "codex")
	if err := Run(context.Background(), rec, Options{}, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rec.Status != Failed {
		t.Fatalf("status = %q, want %q", rec.Status, Failed)
	}
	// The last line of stderr, not the first: agents print startup chatter
	// before they get to the complaint.
	if rec.Err != "not logged in — run codex login" {
		t.Errorf("err = %q, want the actual complaint", rec.Err)
	}
}

func TestRunRefusesWhatItCannotDo(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	copilot := newRecord(t, "copilot")
	if err := Run(context.Background(), copilot, Options{}, nil); !errors.Is(err, ErrCopilotConsent) {
		t.Errorf("copilot without consent: err = %v, want ErrCopilotConsent", err)
	}

	unknown := newRecord(t, "gemini")
	if err := Run(context.Background(), unknown, Options{}, nil); !errors.Is(err, ErrUnknownAgent) {
		t.Errorf("unknown agent: err = %v, want ErrUnknownAgent", err)
	}
}

// TestRunSavesAsItGoes: the dashboard reads the record every couple of
// seconds, so a run that only reported at the end would be indistinguishable
// from one that had hung.
func TestRunSavesAsItGoes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	seen := make(chan string, 1)
	fakeAgent(t, "claude", `
read -r prompt
printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"sleep"}}]}}'
# Wait for the reader to have seen it before finishing.
while [ ! -f "$ORBIT_TEST_GO" ]; do sleep 0.05; done
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"done"}'
`)
	goFile := filepath.Join(t.TempDir(), "go")
	t.Setenv("ORBIT_TEST_GO", goFile)

	rec := newRecord(t, "claude")
	if err := Save(rec); err != nil {
		t.Fatal(err)
	}
	go func() {
		defer os.WriteFile(goFile, nil, 0o600)
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if r, ok := Load(rec.ID); ok && r.Activity != "" {
				seen <- r.Activity
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		seen <- ""
	}()

	if err := Run(context.Background(), rec, Options{}, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := <-seen; got != "Bash: sleep" {
		t.Errorf("mid-run the record said activity %q, want %q", got, "Bash: sleep")
	}
}

// The argv is the interface to each CLI, and every flag in it was arrived at
// by running the thing. Pinning them here means a change has to be deliberate.
func TestArgv(t *testing.T) {
	rec := &Record{Agent: "claude", SessionID: "sid", Cwd: "/w", Prompt: "do it"}

	claude := strings.Join(claudeArgv(rec, ""), " ")
	for _, want := range []string{
		"--input-format stream-json",     // opens the control channel
		"--output-format stream-json",    // ...in the other direction
		"--permission-prompt-tool stdio", // without this, approvals are silently auto-denied
		"--session-id sid",               // orbit picks the id before the process starts
	} {
		if !strings.Contains(claude, want) {
			t.Errorf("claude argv %q missing %q", claude, want)
		}
	}
	if strings.Contains(claude, "--permission-mode") {
		t.Error("claude argv pins a permission mode; the user's own settings should decide")
	}
	if !strings.Contains(strings.Join(claudeArgv(rec, "manual"), " "), "--permission-mode manual") {
		t.Error("a configured permission mode was not passed through")
	}

	codex := codexArgv(rec)
	if got := codex[len(codex)-1]; got != "do it" {
		t.Errorf("codex prompt = %q, want it last on the command line", got)
	}
	joined := strings.Join(codex, " ")
	if !strings.Contains(joined, "--skip-git-repo-check") {
		t.Error("codex argv would refuse to run outside a repository")
	}
	for _, unwanted := range []string{"--sandbox", "approval_policy"} {
		if strings.Contains(joined, unwanted) {
			t.Errorf("codex argv pins %q; ~/.codex/config.toml should decide", unwanted)
		}
	}

	if strings.Contains(strings.Join(copilotArgv(rec, false), " "), "--allow-all-tools") {
		t.Error("copilot argv allowed all tools without consent")
	}
	if !strings.Contains(strings.Join(copilotArgv(rec, true), " "), "--allow-all-tools") {
		t.Error("copilot argv omits the flag its CLI requires non-interactively")
	}
}

// Both CLIs say the same thing twice in different ways, and the pane should
// not read as though the run did the work twice. Codex emits item.started and
// item.completed for one command; claude's last assistant message and its
// result event carry identical text.
func TestRunDoesNotNarrateTheSameThingTwice(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fakeAgent(t, "codex", `
printf '%s\n' '{"type":"thread.started","thread_id":"t1"}'
printf '%s\n' '{"type":"item.started","item":{"type":"command_execution","command":"ls -la"}}'
printf '%s\n' '{"type":"item.completed","item":{"type":"command_execution","command":"ls -la","exit_code":0,"status":"completed"}}'
printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"it worked"}}'
printf '%s\n' '{"type":"turn.completed","usage":{}}'
`)
	rec := newRecord(t, "codex")
	var log bytes.Buffer
	if err := Run(context.Background(), rec, Options{}, &log); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n := strings.Count(log.String(), "shell: ls -la"); n != 1 {
		t.Errorf("the command was narrated %d times, want 1:\n%s", n, log.String())
	}

	// A repeat that carries something new — a non-zero exit — must still show.
	fakeAgent(t, "codex", `
printf '%s\n' '{"type":"item.started","item":{"type":"command_execution","command":"false"}}'
printf '%s\n' '{"type":"item.completed","item":{"type":"command_execution","command":"false","exit_code":1,"status":"failed"}}'
printf '%s\n' '{"type":"turn.completed","usage":{}}'
`)
	log.Reset()
	if err := Run(context.Background(), newRecord(t, "codex"), Options{}, &log); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(log.String(), "exit 1") {
		t.Errorf("a failing command's exit code was swallowed as a duplicate:\n%s", log.String())
	}
}

// The hang this cost four minutes to find: claude does not close stdout when a
// turn ends, because with stream-json input it is waiting for another message.
// Reading to EOF before closing stdin waits for an ending only closing stdin
// can cause.
func TestRunDoesNotWaitForAnEOFThatNeverComes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fakeAgent(t, "claude", `
read -r prompt
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"done"}'
# Exactly what the real CLI does: keep waiting for another message.
while read -r more; do :; done
`)
	rec := newRecord(t, "claude")
	done := make(chan error, 1)
	go func() { done <- Run(context.Background(), rec, Options{Timeout: 30 * time.Second}, nil) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Run hung waiting for stdout to close")
	}
	if rec.Status != Done {
		t.Errorf("status = %q, want %q", rec.Status, Done)
	}
}

// And the mislabelling that followed it: a run interrupted a moment after it
// finished its work still finished its work. Driven through finish directly,
// because the race it guards — the cancellation landing between the result
// event and the record being written — cannot be arranged reliably from
// outside.
func TestFinishPrefersTheStreamOverTheContext(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, stop := context.WithTimeout(context.Background(), time.Nanosecond)
	defer stop()
	<-expired.Done()

	tests := []struct {
		name       string
		ctx        context.Context
		res        result
		wantStatus Status
		wantErr    string
	}{
		{"finished then cancelled", cancelled, result{done: true}, Done, ""},
		{"handed back then cancelled", cancelled, result{handedTo: "Bash: rm -rf x"}, NeedsYou, ""},
		{"cancelled with nothing to show", cancelled, result{}, Failed, "cancelled"},
		{"finished on the deadline", expired, result{done: true}, Done, ""},
		{"the deadline caught it mid-run", expired, result{}, Failed, "timed out"},
		{"the stream reported a failure", cancelled, result{err: "model overloaded"}, Failed, "model overloaded"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRecord(t, "claude")
			if err := finish(tt.ctx, r, tt.res, nil, "", func(string, ...any) {}); err != nil {
				t.Fatal(err)
			}
			if r.Status != tt.wantStatus || r.Err != tt.wantErr {
				t.Errorf("status = %q err = %q, want %q / %q", r.Status, r.Err, tt.wantStatus, tt.wantErr)
			}
		})
	}
}

// A CLI that dies before reading the prompt breaks the pipe under the write.
// The broken pipe is the symptom; what it printed on the way out is the cause,
// and reporting EPIPE instead of that loses the only actionable thing there is.
func TestRunPrefersTheCLIsComplaintOverABrokenPipe(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fakeAgent(t, "claude", `
echo "not logged in — run claude auth login" >&2
exit 1
`)
	rec := newRecord(t, "claude")
	var log bytes.Buffer
	if err := Run(context.Background(), rec, Options{Timeout: 30 * time.Second}, &log); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rec.Status != Failed {
		t.Fatalf("status = %q, want %q", rec.Status, Failed)
	}
	if rec.Err != "not logged in — run claude auth login" {
		t.Errorf("err = %q, want the CLI's complaint, not the pipe error", rec.Err)
	}
	if strings.Contains(rec.Err, "broken pipe") || strings.Contains(rec.Err, "send prompt") {
		t.Errorf("err = %q leaked the symptom", rec.Err)
	}
}

// And a CLI that exits 0 without reading the prompt did no work; recording
// that as a success would put a "your turn" on a conversation that never
// happened.
func TestRunDoesNotCallAnEmptyExitSuccess(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fakeAgent(t, "claude", "exit 0")
	rec := newRecord(t, "claude")
	if err := Run(context.Background(), rec, Options{Timeout: 30 * time.Second}, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rec.Status != Failed {
		t.Errorf("status = %q, want %q", rec.Status, Failed)
	}
	if !strings.Contains(rec.Err, "without starting a turn") {
		t.Errorf("err = %q, want it to say no turn happened", rec.Err)
	}
}

// The terminal record is the last thing written about a run: if it does not
// land, the dashboard keeps showing whatever the previous one said. That has
// to reach the caller rather than exiting 0 on an unrecorded outcome — unlike
// the progress saves, which are corrected by the next event.
func TestTerminalSaveFailureIsReported(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// A file where the record directory should be: every write into it fails.
	blockRecordDir(t)

	var log bytes.Buffer
	r := newRecord(t, "claude")
	err := finish(context.Background(), r, result{done: true}, nil, "",
		func(f string, a ...any) { fmt.Fprintf(&log, f+"\n", a...) })
	if err == nil {
		t.Fatal("finish reported success though the record could not be written")
	}
	if !strings.Contains(log.String(), "could not record the outcome") {
		t.Errorf("nothing said about it in the log:\n%s", log.String())
	}

	// The handoff path is the one where silence would hurt most: a session
	// waiting for a person that nothing tells anyone about.
	if err := finish(context.Background(), r, result{handedTo: "Bash: rm -rf x"}, nil, "",
		func(string, ...any) {}); err == nil {
		t.Error("a handoff whose record could not be written reported success")
	}
}

// Progress saves are the opposite call: the agent is doing real work in
// someone's repository, and killing it because a cache file could not be
// written would destroy more than it protects. But it is said out loud.
func TestProgressSaveFailureNarratesAndCarriesOn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	blockRecordDir(t)
	fakeAgent(t, "claude", `
read -r prompt
printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"go build ./..."}}]}}'
printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"go test ./..."}}]}}'
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"green"}'
while read -r more; do :; done
`)
	var log bytes.Buffer
	rec := newRecord(t, "claude")
	// The terminal save fails too, so Run reports it — the point here is what
	// happened on the way, not the return value.
	Run(context.Background(), rec, Options{Timeout: 30 * time.Second}, &log) //nolint:errcheck

	out := log.String()
	if !strings.Contains(out, "could not record") {
		t.Errorf("a save failure went unmentioned:\n%s", out)
	}
	if n := strings.Count(out, "could not record progress"); n > 1 {
		t.Errorf("the same complaint was repeated %d times; once is enough:\n%s", n, out)
	}
	// The run itself must have carried on to the end.
	if !strings.Contains(out, "go test ./...") {
		t.Errorf("the run stopped at the first failed save:\n%s", out)
	}
	if rec.Status != Done {
		t.Errorf("status = %q, want the run to have completed anyway", rec.Status)
	}
}
