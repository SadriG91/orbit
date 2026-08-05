package pane

import (
	"fmt"
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
	return p.emu.Render()
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
	p.mu.Lock()
	session := p.session
	p.mu.Unlock()
	return p.conn.SendKeys(session, keys)
}

func (p *Pane) Close() error {
	p.once.Do(func() { close(p.done) })
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
	defer p.mu.Unlock()
	if p.emu != nil {
		p.emu.Close()
	}
	p.session = session
	p.emu = vt.NewEmulator(w, h)
	if seed != "" {
		p.emu.WriteString(seed)
	}
}

// windowSize asks how big the pane actually is. Formats must be quoted: tmux's
// own parser treats a bare # as a comment, so an unquoted #{…} silently
// becomes no argument at all.
func (p *Pane) windowSize(session string) (int, int) {
	out, err := p.conn.Command(fmt.Sprintf(
		"display-message -p -t %s '#{window_width} #{window_height}'", session))
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
	out, err := p.conn.Command(fmt.Sprintf("capture-pane -p -e -J -t %s", session))
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
	p.markDirty() // a closed connection is itself a change worth showing
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
