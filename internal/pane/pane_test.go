package pane

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
)

// newTestPane builds the emulator half without a tmux server behind it.
func newTestPane(w, h int) *Pane {
	return &Pane{
		emu:   vt.NewEmulator(w, h),
		dirty: make(chan struct{}, 1),
		done:  make(chan struct{}),
	}
}

// Full-screen TUIs query their terminal and wait for a device response. The
// emulator writes that response to its read side; leaving it undrained blocks
// Emulator.Write while Pane.mu is held, freezing the entire dashboard the
// next time it asks for Text, Render or Session.
func TestTerminalQueryDoesNotHoldPaneLock(t *testing.T) {
	p := newTestPane(40, 5)
	read := make(chan string, 1)
	go func() {
		buf := make([]byte, 64)
		n, _ := p.emu.Read(buf)
		read <- string(buf[:n])
	}()
	wrote := make(chan struct{})
	go func() {
		p.write([]byte("\x1b[6n")) // Device Status Report: cursor position.
		close(wrote)
	}()

	select {
	case <-wrote:
	case <-time.After(time.Second):
		t.Fatal("a terminal query blocked Pane.write")
	}
	select {
	case got := <-read:
		if !strings.Contains(got, "R") {
			t.Errorf("cursor response = %q, want a CSI ... R response", got)
		}
	case <-time.After(time.Second):
		t.Fatal("the emulator produced no cursor response")
	}
}

func drain(p *Pane) int {
	n := 0
	for {
		select {
		case <-p.dirty:
			n++
		default:
			return n
		}
	}
}

func TestWriteReachesTheScreen(t *testing.T) {
	p := newTestPane(40, 5)
	p.write([]byte("hello world"))

	if got := p.Render(); !strings.Contains(got, "hello world") {
		t.Errorf("Render() = %q, want it to contain the written text", got)
	}
}

// The emulator has to interpret escape sequences, not print them. This is the
// whole reason streaming needs a terminal emulator rather than string handling:
// %output carries cursor moves, and a pane that redraws a line in place would
// otherwise accumulate every version of it.
func TestWriteInterpretsEscapeSequences(t *testing.T) {
	p := newTestPane(40, 5)
	p.write([]byte("first\r\nsecond\r\n"))
	// Carriage return to the start of the line, erase it, write over it.
	p.write([]byte("\x1b[1;1H\x1b[2Krewritten"))

	got := p.Render()
	if strings.Contains(got, "first") {
		t.Errorf("the erased line survived: %q", got)
	}
	if !strings.Contains(got, "rewritten") || !strings.Contains(got, "second") {
		t.Errorf("Render() = %q, want rewritten and second", got)
	}
}

// A burst of output must not become a burst of redraws. This is what keeps an
// agent dumping a test run from flooding the Bubble Tea message loop.
func TestDirtyCoalescesBursts(t *testing.T) {
	p := newTestPane(40, 10)
	for i := 0; i < 500; i++ {
		p.write([]byte("line\r\n"))
	}
	if n := drain(p); n != 1 {
		t.Errorf("500 writes produced %d wakeups, want exactly 1", n)
	}
	// And the single wakeup shows the latest state, not the first.
	if got := p.Render(); !strings.Contains(got, "line") {
		t.Errorf("Render() = %q, want the accumulated output", got)
	}
}

// Having consumed a wakeup, the next write must produce another one — a
// coalescing channel that latches would stop the preview updating entirely.
func TestDirtyRearmsAfterBeingRead(t *testing.T) {
	p := newTestPane(40, 5)
	p.write([]byte("one"))
	if n := drain(p); n != 1 {
		t.Fatalf("first write gave %d wakeups, want 1", n)
	}
	p.write([]byte("two"))
	if n := drain(p); n != 1 {
		t.Errorf("write after a read gave %d wakeups, want 1", n)
	}
}

// An empty payload is not a screen change, and waking the UI for one would
// have the dashboard redrawing on every keystroke echo of nothing.
func TestEmptyWriteIsNotDirty(t *testing.T) {
	p := newTestPane(40, 5)
	p.write(nil)
	p.write([]byte{})
	if n := drain(p); n != 0 {
		t.Errorf("empty writes produced %d wakeups, want 0", n)
	}
}

func TestRenderBeforeAttachIsEmpty(t *testing.T) {
	p := &Pane{dirty: make(chan struct{}, 1), done: make(chan struct{})}
	if got := p.Render(); got != "" {
		t.Errorf("Render() with no emulator = %q, want empty", got)
	}
}

func TestHistoryViewportAndFollowTail(t *testing.T) {
	p := newTestPane(40, 5)
	p.write([]byte("live tail"))
	p.history = []string{"one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten"}
	p.scrollOffset = 3

	got := p.Render()
	if !strings.Contains(got, "three") || !strings.Contains(got, "seven") || strings.Contains(got, "eight") {
		t.Errorf("history viewport = %q, want rows three through seven", got)
	}
	if !p.Scrolled() {
		t.Fatal("history viewport did not report its scrollback state")
	}

	p.FollowTail()
	if p.Scrolled() || !strings.Contains(p.Render(), "live tail") {
		t.Errorf("FollowTail did not restore the live emulator: %q", p.Render())
	}
}

// The two accessors are not interchangeable, and mixing them up is how a
// preview ends up with escape sequences printed as literal text: the render
// path strips control characters and truncates by rune count, both of which
// destroy ANSI.
func TestTextIsPlainAndRenderIsStyled(t *testing.T) {
	p := newTestPane(40, 5)
	p.write([]byte("\x1b[31mred\x1b[0m plain"))

	text := p.Text()
	if strings.ContainsRune(text, 0x1b) {
		t.Errorf("Text() carries escape sequences: %q", text)
	}
	if !strings.Contains(text, "red plain") {
		t.Errorf("Text() = %q, want the words without styling", text)
	}
	if !strings.ContainsRune(p.Render(), 0x1b) {
		t.Error("Render() dropped the styling it exists to carry")
	}
}
