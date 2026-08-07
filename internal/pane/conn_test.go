package pane

import (
	"os"
	"strings"
	"testing"
)

func TestClean(t *testing.T) {
	tests := []struct{ in, want string }{
		// The stream's first line carries the opening DCS.
		{"\x1bP1000p%begin 1785920907 291 0", "%begin 1785920907 291 0"},
		// A pty ends lines with CRLF, so the CR survives the scanner.
		{"%end 1785920907 291 0\r", "%end 1785920907 291 0"},
		{"%output %0 hi\x1b\\", "%output %0 hi"},
		{"%output %0 plain", "%output %0 plain"},
		// A backslash that isn't the terminator must survive untouched.
		{`%output %0 C:\path`, `%output %0 C:\path`},
	}
	for _, tt := range tests {
		if got := clean(tt.in); got != tt.want {
			t.Errorf("clean(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestUnescape(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"tick 2", "tick 2"},
		{`tick 2\015\015\012`, "tick 2\r\r\n"},
		{`\033[2K`, "\x1b[2K"},
		// A trailing backslash with nothing to consume is kept, not dropped.
		{`oops\`, `oops\`},
		{`bad\09z`, `bad\09z`},
	}
	for _, tt := range tests {
		if got := string(unescape(tt.in)); got != tt.want {
			t.Errorf("unescape(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestParseNotification(t *testing.T) {
	n, ok := parseNotification(`%output %0 tick 2\015\012`)
	if !ok || n.Kind != "output" || n.Pane != "%0" || string(n.Data) != "tick 2\r\n" {
		t.Fatalf("output notification = %+v (data %q), ok=%v", n, n.Data, ok)
	}

	n, ok = parseNotification("%session-changed $1 other")
	if !ok || n.Kind != "session-changed" || n.Args != "$1 other" {
		t.Errorf("session-changed = %+v, ok=%v", n, ok)
	}
	if n.Data != nil {
		t.Errorf("non-output notification carried data: %q", n.Data)
	}

	if _, ok := parseNotification("%"); ok {
		t.Error("a bare %% parsed as a notification")
	}
}

func newTestConn() *Conn {
	return &Conn{
		notes: make(chan Notification, 64),
		done:  make(chan struct{}),
		ready: make(chan struct{}),
	}
}

func TestCommandTargetRejectsTmuxSyntax(t *testing.T) {
	for _, target := range []string{"cl-work-orbit", "cx_project_2", "A1", "%0", "@12", "$3"} {
		if got, err := commandTarget(target); err != nil || got != target {
			t.Errorf("commandTarget(%q) = %q, %v", target, got, err)
		}
	}
	for _, target := range []string{"", "%", "%x", "%0; kill-server", "name; kill-server", "name\nkill-server", "two words", `a'b`, `a\\b`, "#comment"} {
		if got, err := commandTarget(target); err == nil || got != "" {
			t.Errorf("commandTarget(%q) = %q, %v; want rejection", target, got, err)
		}
	}
}

// A verbatim capture from tmux 3.7b, CRs included: the attach handshake, two
// successful commands, a %output between them, and a parse error.
const transcript = "\x1bP1000p%begin 1785920907 291 0\r\n" +
	"%end 1785920907 291 0\r\n" +
	"%session-changed $0 demo\r\n" +
	"%window-renamed @0 bash\r\n" +
	"%begin 1785920908 298 1\r\n" +
	"%end 1785920908 298 1\r\n" +
	"%layout-change @0 a87d,100x30,0,0,0 a87d,100x30,0,0,0 *\r\n" +
	"%begin 1785920908 301 1\r\n" +
	"demo attached=1 size=100x30\r\n" +
	"other attached=0 size=80x24\r\n" +
	"%end 1785920908 301 1\r\n" +
	`%output %0 tick 2\015\015\012` + "\r\n" +
	"%begin 1785920909 310 1\r\n" +
	"parse error: unknown command: abcdef\r\n" +
	"%error 1785920909 310 1\r\n"

func TestFramingMatchesRepliesToCommandsInOrder(t *testing.T) {
	c := newTestConn()
	replies := []chan reply{
		make(chan reply, 1),
		make(chan reply, 1),
		make(chan reply, 1),
	}
	c.pending = append([]chan reply(nil), replies...)

	c.readFrom(strings.NewReader(transcript))

	// The handshake block is unsolicited and must not be handed to a caller.
	select {
	case <-c.ready:
	default:
		t.Fatal("the handshake block did not mark the connection ready")
	}

	first := <-replies[0]
	if first.err != nil || len(first.lines) != 0 {
		t.Errorf("refresh-client reply = %+v, want empty and no error", first)
	}

	second := <-replies[1]
	if second.err != nil {
		t.Fatalf("list-sessions reply errored: %v", second.err)
	}
	want := []string{"demo attached=1 size=100x30", "other attached=0 size=80x24"}
	if strings.Join(second.lines, "|") != strings.Join(want, "|") {
		t.Errorf("list-sessions lines = %q, want %q", second.lines, want)
	}

	third := <-replies[2]
	if third.err == nil {
		t.Error("an error block did not produce an error")
	} else if !strings.Contains(third.err.Error(), "unknown command: abcdef") {
		t.Errorf("error lost its detail: %v", third.err)
	}
}

func TestFramingRoutesNotificationsAroundBlocks(t *testing.T) {
	c := newTestConn()
	c.pending = []chan reply{
		make(chan reply, 1),
		make(chan reply, 1),
		make(chan reply, 1),
	}

	c.readFrom(strings.NewReader(transcript))
	close(c.notes)

	var kinds []string
	var output *Notification
	for n := range c.notes {
		kinds = append(kinds, n.Kind)
		if n.Kind == "output" {
			cp := n
			output = &cp
		}
	}

	got := strings.Join(kinds, ",")
	want := "session-changed,window-renamed,layout-change,output"
	if got != want {
		t.Errorf("notifications = %q, want %q", got, want)
	}

	// The lines inside a reply block must never surface as notifications, and
	// the payload must arrive unescaped and ready for an emulator.
	if output == nil {
		t.Fatal("no output notification")
	}
	if output.Pane != "%0" || string(output.Data) != "tick 2\r\r\n" {
		t.Errorf("output = pane %q data %q", output.Pane, output.Data)
	}
}

// A command that never reached tmux must not leave a waiter behind.
//
// The queue is matched by position, because tmux assigns the serials rather
// than us. So an entry waiting for a reply that can never come does not just
// leak — it shifts every later reply by one, and the next command gets an
// answer belonging to somebody else. The timeout path deliberately does the
// opposite and keeps its entry, because there the command *was* sent.
func TestFailedWriteLeavesNoWaiterBehind(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	r.Close()
	w.Close() // any write now fails

	c := &Conn{ptmx: w, notes: make(chan Notification, 1), done: make(chan struct{})}

	if _, err := c.Command("list-sessions"); err == nil {
		t.Fatal("a write to a closed pty reported success")
	}
	if n := len(c.pending); n != 0 {
		t.Fatalf("%d waiter(s) left after a failed write, want 0 — the next reply would go to the wrong caller", n)
	}

	// And the queue still works afterwards rather than being permanently
	// skewed: the next command's reply must reach the next command.
	c.ptmx = nil // unused; Command is not reached again
	ch := make(chan reply, 1)
	c.pending = append(c.pending, ch)
	c.deliver(reply{lines: []string{"the right answer"}})
	select {
	case got := <-ch:
		if len(got.lines) != 1 || got.lines[0] != "the right answer" {
			t.Errorf("reply = %q, want it delivered to the caller that asked", got.lines)
		}
	default:
		t.Error("the reply reached nobody")
	}
}

// A remote tmux exit reaches shutdown before the UI calls Close. The logical
// closed flag must not make resource cleanup return early: doing so leaks one
// PTY per reconnect until macOS refuses to allocate any more.
func TestShutdownReleasesPTY(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	c := newTestConn()
	c.ptmx = w
	c.shutdown()

	if _, err := w.Write([]byte("still open")); err == nil {
		t.Fatal("shutdown left the PTY descriptor open")
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close after shutdown: %v", err)
	}
}

func TestCloseReportsPTYReleaseFailure(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	c := newTestConn()
	c.ptmx = w
	first := c.Close()
	if first == nil {
		t.Fatal("Close swallowed the PTY close failure")
	}
	if second := c.Close(); second == nil || second.Error() != first.Error() {
		t.Errorf("second Close returned %v, want the recorded error %v", second, first)
	}
}
