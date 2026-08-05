package test

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/sadrig91/orbit/internal/pane"
	"github.com/sadrig91/orbit/internal/tmux"
)

// Exercises the control-mode client against a real tmux server: the attach
// handshake, a command with a reply, live %output from a pane, switching the
// client between sessions, and — the property the whole design rests on — that
// watching a session does not resize it.
func TestControlModeRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	if err := tmux.InstallConf(); err != nil {
		t.Fatalf("InstallConf: %v", err)
	}

	const (
		watched = "orbit-ctlmode-a"
		other   = "orbit-ctlmode-b"
		marker  = "ORBIT_CTLMODE_MARKER"
	)
	cwd, _ := os.Getwd()
	for _, name := range []string{watched, other} {
		if err := tmux.Spawn(name, cwd, "true", "ctlmode · "+name, "codex", ""); err != nil {
			t.Fatalf("Spawn %s: %v", name, err)
		}
		defer tmux.Kill(name)
	}

	sizeBefore := windowSize(t, watched)

	conn, err := pane.Dial(watched)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	// A command with a reply proves the %begin/%end framing and the FIFO
	// matching against a server that assigns its own serials.
	// Formats must be quoted: tmux's own parser treats a bare # as the start
	// of a comment, so an unquoted #{pane_id} silently becomes no argument at
	// all and display-message answers with its default format instead.
	reply, err := conn.Command("display-message -p '#{pane_id}'")
	if err != nil {
		t.Fatalf("display-message: %v", err)
	}
	if len(reply) != 1 || !strings.HasPrefix(reply[0], "%") {
		t.Fatalf("pane id reply = %q, want one %%N line", reply)
	}
	paneID := reply[0]

	// A command tmux rejects must come back as an error, not a hang.
	if _, err := conn.Command("no-such-command"); err == nil {
		t.Error("an unknown command did not produce an error")
	}

	// The preview must not make a session look open in a terminal tab. This is
	// the whole reason List subtracts control clients: #{session_attached} is
	// 1 right now, and if that reached alreadyOpen, Enter would try to focus a
	// tab nobody ever opened.
	var found bool
	for _, ts := range tmux.List() {
		if ts.Name != watched {
			continue
		}
		found = true
		if ts.Attached {
			t.Error("a session with only a control client attached reports as Attached")
		}
	}
	if !found {
		t.Fatalf("%s missing from tmux.List()", watched)
	}

	// Drain notifications in the background, watching for the marker.
	seen := make(chan string, 1)
	go func() {
		var buf strings.Builder
		for n := range conn.Notifications() {
			if n.Kind != "output" {
				continue
			}
			buf.Write(n.Data)
			if strings.Contains(buf.String(), marker+"\r") || strings.Count(buf.String(), marker) > 1 {
				select {
				case seen <- buf.String():
				default:
				}
			}
		}
	}()

	if err := conn.SendKeys(paneID, "'echo "+marker+"' Enter"); err != nil {
		t.Fatalf("SendKeys: %v", err)
	}

	select {
	case got := <-seen:
		if !strings.Contains(got, marker) {
			t.Errorf("output never carried the marker: %q", got)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("no output notification carrying the marker within 15s")
	}

	// One connection has to serve the whole dashboard, so switching sessions
	// must work over the same client rather than needing a new process.
	if err := conn.Switch(other); err != nil {
		t.Fatalf("Switch: %v", err)
	}
	reply, err = conn.Command("display-message -p '#{session_name}'")
	if err != nil {
		t.Fatalf("display-message after switch: %v", err)
	}
	if len(reply) != 1 || reply[0] != other {
		t.Errorf("after Switch the client is on %q, want %q", reply, other)
	}

	// Idling the connection must actually stop the byte stream.
	if err := conn.SetOutput(false); err != nil {
		t.Errorf("SetOutput(false): %v", err)
	}

	// The design deliberately never calls refresh-client -C, because that
	// resizes the session permanently — for every session the client visits.
	if after := windowSize(t, watched); after != sizeBefore {
		t.Errorf("watching resized the session: %s -> %s", sizeBefore, after)
	}
	if after := windowSize(t, other); after != sizeBefore {
		t.Errorf("switching resized the other session: %s -> %s", sizeBefore, after)
	}
}

func windowSize(t *testing.T, session string) string {
	t.Helper()
	out, err := exec.Command("tmux", tmux.Args("display-message", "-p", "-t", session,
		"#{window_width}x#{window_height}")...).Output()
	if err != nil {
		t.Fatalf("display-message size for %s: %v", session, err)
	}
	return strings.TrimSpace(string(out))
}

// The emulator half, end to end: a pane seeded from what is already on screen,
// updated live from the byte stream, and reset when the view moves elsewhere.
func TestPaneStreamsLiveOutput(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	if err := tmux.InstallConf(); err != nil {
		t.Fatalf("InstallConf: %v", err)
	}

	const (
		watched = "orbit-pane-a"
		other   = "orbit-pane-b"
		marker  = "ORBIT_PANE_MARKER"
	)
	cwd, _ := os.Getwd()
	for _, name := range []string{watched, other} {
		if err := tmux.Spawn(name, cwd, "true", "pane · "+name, "codex", ""); err != nil {
			t.Fatalf("Spawn %s: %v", name, err)
		}
		defer tmux.Kill(name)
	}

	p, err := pane.Open(watched)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer p.Close()

	if p.Session() != watched {
		t.Errorf("Session() = %q, want %q", p.Session(), watched)
	}

	// Control mode only reports new output, so without the capture-pane seed a
	// session that is sitting idle would render as a blank box forever.
	if seeded := p.Render(); strings.TrimSpace(stripANSI(seeded)) == "" {
		t.Error("the pane rendered empty — the capture-pane seed did not land")
	}

	if err := p.SendKeys("'echo " + marker + "' Enter"); err != nil {
		t.Fatalf("SendKeys: %v", err)
	}
	if !waitForRender(t, p, marker) {
		t.Fatalf("the marker never reached the emulator")
	}

	// Moving the view must not leak the old session's screen into the new one.
	if err := p.Switch(other); err != nil {
		t.Fatalf("Switch: %v", err)
	}
	if p.Session() != other {
		t.Errorf("after Switch, Session() = %q, want %q", p.Session(), other)
	}
	if got := stripANSI(p.Render()); strings.Contains(got, marker) {
		t.Errorf("the previous session's output survived the switch: %q", got)
	}

	// Idling must not break the connection — it has to keep serving commands.
	if err := p.SetWatching(false); err != nil {
		t.Errorf("SetWatching(false): %v", err)
	}
	if err := p.Switch(watched); err != nil {
		t.Errorf("Switch after idling: %v", err)
	}
}

func waitForRender(t *testing.T, p *pane.Pane, want string) bool {
	t.Helper()
	deadline := time.After(15 * time.Second)
	for {
		select {
		case <-p.Dirty():
			if strings.Contains(stripANSI(p.Render()), want) {
				return true
			}
		case <-deadline:
			return false
		}
	}
}

// Render encodes styles as ANSI, so assertions have to look past them.
func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			for i < len(s) && !isANSIFinal(s[i]) {
				i++
			}
			i++
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func isANSIFinal(c byte) bool { return c >= 0x40 && c <= 0x7e && c != '[' }

// Close has to remove the client from the server, not merely stop the process.
// A control client left on tmux's client list is worse than a leaked process:
// the session keeps reporting as attached, so orbit claims it is open in a
// terminal tab that does not exist, for as long as the server lives.
//
// Today's implementation satisfies this — the invariant is guarded here
// because nothing else in the suite looks at the server's client list after a
// Close, so a future change to shutdown could break it silently.
func TestCloseRemovesTheControlClient(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	if err := tmux.InstallConf(); err != nil {
		t.Fatalf("InstallConf: %v", err)
	}

	const name = "orbit-close-check"
	cwd, _ := os.Getwd()
	if err := tmux.Spawn(name, cwd, "true", "close · check", "codex", ""); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	defer tmux.Kill(name)

	// Someone else's clients are not ours to account for.
	baseline := controlClientCount(t)

	conn, err := pane.Dial(name)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	if got := controlClientCount(t); got != baseline+1 {
		t.Fatalf("after Dial there are %d control clients, want %d", got, baseline+1)
	}

	if err := conn.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// The server drops the client asynchronously once it detaches.
	deadline := time.Now().Add(10 * time.Second)
	for {
		got := controlClientCount(t)
		if got <= baseline {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d control clients still attached 10s after Close, want %d", got, baseline)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func controlClientCount(t *testing.T) int {
	t.Helper()
	out, err := exec.Command("tmux", tmux.Args("list-clients", "-F", "#{client_control_mode}")...).Output()
	if err != nil {
		return 0 // no server, or no clients at all
	}
	n := 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.TrimSpace(line) == "1" {
			n++
		}
	}
	return n
}
