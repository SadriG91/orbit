package pane

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"

	"github.com/charmbracelet/x/vt"
)

// Pane is a live view of one tmux pane: a control connection feeding a
// terminal emulator, and a signal that says the screen changed.
//
// The emulator is sized to the tmux window rather than to whatever space the
// dashboard has for it. Sizing it to the preview would mean telling tmux the
// client is that size, which resizes the session for everyone attached to it —
// permanently, and for every session the preview visits. Rendering the pane at
// its true size and letting the caller clip is the honest trade: you see
// exactly what the agent sees.
type Pane struct {
	mu      sync.Mutex
	emu     *vt.Emulator
	conn    *Conn
	session string
	// history is a frozen tmux scrollback snapshot. offset is the number of
	// rows above the live tail. Control-mode streams pane output, not a tmux
	// client's copy-mode viewport, so Orbit owns this viewport itself.
	history      []string
	scrollOffset int

	dirty chan struct{}
	done  chan struct{}
	once  sync.Once
}

// defaultSize is used when tmux won't say how big the window is. Wrong is
// better than zero here: an emulator with no columns renders nothing at all,
// which looks like a broken feature rather than a bad guess.
const defaultCols, defaultRows = 80, 24

// Open attaches to a session and starts streaming it.
func Open(session string) (*Pane, error) {
	conn, err := Dial(session)
	if err != nil {
		return nil, err
	}
	p := &Pane{
		conn:  conn,
		dirty: make(chan struct{}, 1),
		done:  make(chan struct{}),
	}
	p.reset(session)
	go p.pump()
	return p, nil
}

// Dirty fires when the screen has changed. It is a size-one channel with a
// non-blocking send behind it, so a burst of output collapses into a single
// wakeup — the reader always sees the latest screen regardless of how many
// writes it missed. That is the whole point of streaming over polling: the
// redraw rate is decoupled from the output rate, and nothing is ever lost
// between samples the way it is with capture-pane.
func (p *Pane) Dirty() <-chan struct{} { return p.dirty }

// Done is closed once the pane will never change again — the control client
// exited, or Close was called.
//
// Anything waiting on Dirty has to wait on this too. A dead connection sends
// one last wakeup and then nothing, so a reader watching only Dirty blocks on a
// screen that can no longer move, and the caller goes on believing it has a
// live stream when it has a corpse.
func (p *Pane) Done() <-chan struct{} { return p.done }

// Session is what the pane is currently showing.
func (p *Pane) Session() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.session
}

// Render returns the screen with styles encoded as ANSI.
//
// Anything consuming this has to measure and cut with ANSI-aware helpers —
// counting runes splits escape sequences, and stripping control characters
// removes them entirely. Use Text where the caller does its own truncation.
func (p *Pane) Render() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.emu == nil {
		return ""
	}
	if p.scrollOffset > 0 && len(p.history) > 0 {
		h := p.emu.Height()
		end := max(0, len(p.history)-p.scrollOffset)
		start := max(0, end-h)
		return strings.Join(p.history[start:end], "\n")
	}
	return p.emu.Render()
}

// Scrolled reports whether Render is showing history instead of the live tail.
func (p *Pane) Scrolled() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.scrollOffset > 0
}

// FollowTail returns Render to the live pane. Keyboard input always calls this
// first, matching ordinary terminal behavior where typing resumes at the prompt.
func (p *Pane) FollowTail() {
	p.mu.Lock()
	changed := p.scrollOffset > 0
	p.history = nil
	p.scrollOffset = 0
	p.mu.Unlock()
	if changed {
		p.markDirty()
	}
}

// Text returns the screen as plain text, safe to truncate by rune count.
func (p *Pane) Text() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.emu == nil {
		return ""
	}
	return p.emu.String()
}

// Switch points the pane at another session, over the same connection.
func (p *Pane) Switch(session string) error {
	if p.Session() == session {
		return nil
	}
	if err := p.conn.Switch(session); err != nil {
		return err
	}
	p.reset(session)
	p.markDirty()
	return nil
}

// SetWatching turns the byte stream on and off. Off is what an unwatched
// session should cost: nothing.
func (p *Pane) SetWatching(on bool) error { return p.conn.SetOutput(on) }

// SendKeys relays the user's keystrokes to the pane.
func (p *Pane) SendKeys(keys string) error {
	p.FollowTail()
	p.mu.Lock()
	session := p.session
	p.mu.Unlock()
	return p.conn.SendKeys(session, keys)
}

// SendKeyTo relays one named tmux key to a specific session. The explicit
// target matters when input is queued: the dashboard may move its preview to
// another session before an earlier keystroke has finished crossing the
// control connection.
func (p *Pane) SendKeyTo(session, key string) error {
	p.FollowTail()
	return p.conn.SendKeys(session, key)
}

// SendTextTo relays literal UTF-8 to a specific session.
func (p *Pane) SendTextTo(session, value string) error {
	p.FollowTail()
	return p.conn.SendText(session, value)
}

// SendWheelTo forwards a wheel event with coordinates relative to the embedded
// terminal, preserving mouse-aware TUI behavior as well as tmux scrollback.
func (p *Pane) SendWheelTo(session string, x, y int, direction WheelDirection) error {
	mode, err := p.conn.inputMode(session)
	if err != nil {
		return err
	}
	if mode.mouse || mode.alternate {
		p.FollowTail()
		return p.conn.sendWheel(session, x, y, direction, mode)
	}
	return p.scrollHistory(session, direction)
}

func (p *Pane) scrollHistory(session string, direction WheelDirection) error {
	target, err := commandTarget(session)
	if err != nil {
		return err
	}
	p.mu.Lock()
	if p.session != session {
		p.mu.Unlock()
		return fmt.Errorf("pane switched from %s to %s", session, p.session)
	}
	needSnapshot := len(p.history) == 0
	p.mu.Unlock()

	var snapshot []string
	if needSnapshot {
		out, err := p.conn.Command(fmt.Sprintf("capture-pane -p -e -S -5000 -t %s", target))
		if err != nil {
			return err
		}
		snapshot = out
	}

	p.mu.Lock()
	if p.session != session {
		p.mu.Unlock()
		return fmt.Errorf("pane switched from %s to %s", session, p.session)
	}
	if len(p.history) == 0 {
		p.history = snapshot
	}
	height := defaultRows
	if p.emu != nil {
		height = p.emu.Height()
	}
	maxOffset := max(0, len(p.history)-height)
	if direction == WheelUp {
		p.scrollOffset = min(maxOffset, p.scrollOffset+3)
	} else {
		p.scrollOffset = max(0, p.scrollOffset-3)
		if p.scrollOffset == 0 {
			p.history = nil
		}
	}
	p.mu.Unlock()
	p.markDirty()
	return nil
}

// Resize changes the control client and emulator to the space Orbit gives an
// explicitly focused terminal. Merely previewing a session never calls this.
func (p *Pane) Resize(width, height int) error {
	if width < 1 || height < 1 {
		return nil
	}
	if err := p.conn.Resize(width, height); err != nil {
		return err
	}
	// tmux redraws after the client resize, but reseeding immediately means the
	// focused view does not spend a frame showing the old 220x60 screen.
	p.reset(p.Session())
	p.markDirty()
	return nil
}

func (p *Pane) Close() error {
	p.once.Do(func() {
		close(p.done)
		p.mu.Lock()
		if p.emu != nil {
			stopEmulator(p.emu)
		}
		p.mu.Unlock()
	})
	return p.conn.Close()
}

// reset builds a fresh emulator for a session and seeds it with what is
// already on screen.
//
// Control mode only reports what a pane writes from now on, so an emulator
// attached to a long-running session would sit blank until the agent happened
// to print something — which, for a session parked on a permission prompt, is
// never. capture-pane supplies the starting screen and the stream takes it
// from there.
func (p *Pane) reset(session string) {
	w, h := p.windowSize(session)
	seed := p.capture(session)

	p.mu.Lock()
	if p.emu != nil {
		stopEmulator(p.emu)
	}
	p.session = session
	p.history = nil
	p.scrollOffset = 0
	emu := vt.NewEmulator(w, h)
	p.emu = emu
	p.mu.Unlock()

	// A terminal is bidirectional even when nobody is typing. Full-screen
	// programs ask it questions such as "where is the cursor?" (CSI 6n); the
	// emulator writes the answer to its read side. If nobody drains that side,
	// Write blocks while holding Pane.mu and freezes both the stream and the UI.
	// Forward those device replies to the program through tmux. Closing an old
	// emulator during a reset ends its reader.
	go p.reply(emu, session)

	if seed != "" {
		p.write([]byte(seed))
	}
}

// stopEmulator closes the input pipe rather than Emulator.Close. The latter
// reads and writes the emulator's unsynchronized closed flag concurrently with
// Read, which the race detector catches whenever a pane is switched while its
// reply goroutine is blocked. io.Pipe explicitly supports closing one end to
// wake the other, and the old emulator is never written again after reset.
func stopEmulator(emu *vt.Emulator) {
	if closer, ok := emu.InputPipe().(io.Closer); ok {
		_ = closer.Close()
	}
}

func (p *Pane) reply(emu *vt.Emulator, session string) {
	buf := make([]byte, 1024)
	for {
		n, err := emu.Read(buf)
		if n > 0 {
			// Best effort: a failed reply means the pane or connection went away,
			// and the ordinary lifecycle notifications will reconcile that. Keep
			// draining even after a send failure: abandoning the read side would
			// make the next terminal query deadlock Emulator.Write again.
			if p.conn == nil {
				return
			}
			_ = p.conn.SendText(session, string(buf[:n]))
		}
		if err != nil {
			return
		}
	}
}

// windowSize asks how big the pane actually is. Formats must be quoted: tmux's
// own parser treats a bare # as a comment, so an unquoted #{…} silently
// becomes no argument at all.
func (p *Pane) windowSize(session string) (int, int) {
	target, err := commandTarget(session)
	if err != nil {
		return defaultCols, defaultRows
	}
	out, err := p.conn.Command(fmt.Sprintf(
		"display-message -p -t %s '#{window_width} #{window_height}'", target))
	if err != nil || len(out) == 0 {
		return defaultCols, defaultRows
	}
	wStr, hStr, ok := strings.Cut(strings.TrimSpace(out[0]), " ")
	if !ok {
		return defaultCols, defaultRows
	}
	w, err1 := strconv.Atoi(wStr)
	h, err2 := strconv.Atoi(hStr)
	if err1 != nil || err2 != nil || w <= 0 || h <= 0 {
		return defaultCols, defaultRows
	}
	return w, h
}

// capture reads the pane's current screen, escape sequences included, as the
// emulator's starting state. -e keeps the colours, -J unwraps folded lines.
func (p *Pane) capture(session string) string {
	target, err := commandTarget(session)
	if err != nil {
		return ""
	}
	out, err := p.conn.Command(fmt.Sprintf("capture-pane -p -e -J -t %s", target))
	if err != nil {
		return ""
	}
	return strings.Join(out, "\r\n")
}

// pump feeds output into the emulator until the connection ends.
func (p *Pane) pump() {
	for n := range p.conn.Notifications() {
		switch n.Kind {
		case "output":
			p.write(n.Data)
		case "pane-exited", "window-close", "session-changed":
			// Not screen content, but the pane the caller is watching just
			// became something else — worth a redraw.
			p.markDirty()
		}
	}
	// The connection has ended. Show the last screen it managed, then say so —
	// in that order, since a reader selecting on both should be able to take
	// the final state whichever branch it happens to pick.
	p.markDirty()
	p.once.Do(func() { close(p.done) })
}

// write is separated from pump so the emulator and the dirty signalling can be
// tested without a tmux server.
func (p *Pane) write(data []byte) {
	if len(data) == 0 {
		return
	}
	p.mu.Lock()
	if p.emu != nil {
		p.emu.Write(data)
	}
	p.mu.Unlock()
	p.markDirty()
}

func (p *Pane) markDirty() {
	select {
	case p.dirty <- struct{}{}:
	default: // a wakeup is already pending, and it will see this write too
	}
}
