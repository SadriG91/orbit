// Package pane streams a tmux pane's live output over control mode.
//
// Everything else in orbit reads tmux by polling: one list-sessions per tick,
// one capture-pane for the preview. That is right for a list that changes
// every few seconds and wrong for a pane that changes every few milliseconds.
// A poll forks a process, arrives late, and silently loses whatever appeared
// and vanished between two samples — a test run that scrolled past between
// ticks was never there as far as the dashboard is concerned.
//
// Control mode is tmux's push interface: one long-lived client that emits
// %output as the pane writes it. What arrives is the raw byte stream, not
// rendered text, so a terminal emulator has to turn it back into a screen —
// that part lives in emulator.go. This file is only the transport.
package pane

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"

	"github.com/sadrig91/orbit/internal/tmux"
)

// A control client must be on a pty, both directions. Given plain pipes tmux
// exits immediately with "tcgetattr failed: Inappropriate ioctl for device" —
// it calls tcgetattr on its input before it will speak the protocol at all.
// pty.Start hands back one file that is both, which is what we want anyway.

var (
	ErrClosed  = errors.New("control connection closed")
	ErrTimeout = errors.New("control command timed out")
)

// commandTimeout bounds a single command. The dashboard must never block on a
// wedged server, for the same reason internal/tmux bounds its own invocations.
const commandTimeout = 10 * time.Second

// outputBuffer is deliberately large. Dropping notifications is not an option
// the way it would be for a status feed: %output carries the byte stream an
// emulator is reconstructing a screen from, and a hole in it corrupts every
// frame after it, permanently. Better to make the reader wait.
const outputBuffer = 4096

// maxLine caps a single control-mode line. A pane dumping a long line escapes
// to four bytes per character, so this is smaller than it looks.
const maxLine = 4 * 1024 * 1024

// Notification is an asynchronous message from the server: pane output, or a
// lifecycle event. Kind is the notification name without its % sigil.
type Notification struct {
	Kind string // "output", "pane-exited", "session-changed", …
	Pane string // "%0" on output notifications, empty otherwise
	Args string // the unparsed remainder, for the events we don't decode
	Data []byte // unescaped payload, output notifications only
}

type reply struct {
	lines []string
	err   error
}

// Conn is a tmux control-mode client: one process, one pty, for as long as the
// dashboard is up. Commands go out over the same connection the notifications
// come back on, so driving a pane needs no second mechanism.
type Conn struct {
	cmd  *exec.Cmd
	ptmx *os.File

	notes chan Notification
	done  chan struct{}

	mu      sync.Mutex
	pending []chan reply // FIFO; see deliver
	closed  bool

	ready     chan struct{}
	readyOnce sync.Once
}

// Dial attaches a control client to a session on orbit's tmux server.
func Dial(session string) (*Conn, error) {
	cmd := exec.Command("tmux", tmux.Args("-CC", "attach", "-t", session)...)
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return nil, fmt.Errorf("start control client: %w", err)
	}
	c := &Conn{
		cmd:   cmd,
		ptmx:  ptmx,
		notes: make(chan Notification, outputBuffer),
		done:  make(chan struct{}),
		ready: make(chan struct{}),
	}
	go func() {
		c.readFrom(ptmx)
		c.shutdown()
	}()

	// Attaching emits one unsolicited block before anything is asked of it.
	// Waiting for it here means every later block can be matched against a
	// command we actually sent.
	select {
	case <-c.ready:
		return c, nil
	case <-c.done:
		c.Close()
		return nil, fmt.Errorf("control client exited during handshake")
	case <-time.After(commandTimeout):
		c.Close()
		return nil, fmt.Errorf("control client never completed its handshake")
	}
}

// Notifications is closed when the connection ends.
func (c *Conn) Notifications() <-chan Notification { return c.notes }

// Command sends one tmux command and waits for its reply block.
func (c *Conn) Command(cmd string) ([]string, error) {
	ch := make(chan reply, 1)

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, ErrClosed
	}
	// Registered before the write, so the reader can never see the %begin
	// before it knows a command is outstanding.
	c.pending = append(c.pending, ch)
	if _, err := io.WriteString(c.ptmx, cmd+"\n"); err != nil {
		// Take it back off. The command never reached tmux, so no reply will
		// ever arrive for it, and an entry waiting for a reply that cannot
		// come misaligns every later one — the next block would be handed to
		// this caller instead of whoever actually asked for it.
		//
		// The opposite of the timeout below, where the command *was* sent and
		// the entry has to stay for exactly the same reason. Holding the lock
		// across the write is what makes this safe: nothing else can have
		// taken the entry in between, so the last one is still ours.
		c.pending = c.pending[:len(c.pending)-1]
		c.mu.Unlock()
		return nil, fmt.Errorf("write command: %w", err)
	}
	c.mu.Unlock()

	select {
	case r := <-ch:
		return r.lines, r.err
	case <-c.done:
		return nil, ErrClosed
	case <-time.After(commandTimeout):
		// The entry deliberately stays in the queue. Removing it would
		// misalign every later reply, and delivering to it is harmless
		// because the channel is buffered and nobody is listening.
		return nil, ErrTimeout
	}
}

// Switch points the client at another session. This is why one connection is
// enough for the whole dashboard: moving focus between sessions costs a
// command, not a process.
func (c *Conn) Switch(session string) error {
	_, err := c.Command("switch-client -t " + session)
	return err
}

// SetOutput turns %output notifications on and off. Off is the idle state:
// the connection stays up for lifecycle events without paying for the byte
// stream of a session nobody is looking at.
func (c *Conn) SetOutput(on bool) error {
	flag := "no-output"
	if on {
		flag = "!no-output"
	}
	_, err := c.Command("refresh-client -f " + flag)
	return err
}

// SendKeys types into a pane. Note this is the user's keystrokes being
// relayed — orbit still originates nothing on its own.
func (c *Conn) SendKeys(pane, keys string) error {
	_, err := c.Command(fmt.Sprintf("send-keys -t %s %s", pane, keys))
	return err
}

// SendText sends arbitrary UTF-8 without letting tmux interpret any part of
// it as a key name or command syntax.
//
// This deliberately uses send-keys -H rather than quoting text into a tmux
// command. The command parser has its own quoting rules, while input here may
// contain quotes, semicolons, newlines and control bytes from bracketed paste.
// -H accepts hexadecimal bytes, so the command itself contains only a fixed
// vocabulary plus [0-9a-f]. UTF-8 is sent as its constituent bytes.
func (c *Conn) SendText(pane, value string) error {
	const chunk = 512
	b := []byte(value)
	for len(b) > 0 {
		n := min(chunk, len(b))
		args := make([]string, 0, n)
		for _, v := range b[:n] {
			args = append(args, fmt.Sprintf("%02x", v))
		}
		if _, err := c.Command("send-keys -H -t " + pane + " " + strings.Join(args, " ")); err != nil {
			return err
		}
		b = b[n:]
	}
	return nil
}

// WheelDirection is the direction of one physical mouse-wheel event.
type WheelDirection int

const (
	WheelUp   WheelDirection = 1
	WheelDown WheelDirection = -1
)

type paneInputMode struct {
	mouse, sgr, alternate bool
}

func (c *Conn) inputMode(pane string) (paneInputMode, error) {
	out, err := c.Command("display-message -p -t " + pane +
		" '#{mouse_any_flag} #{mouse_sgr_flag} #{alternate_on}'")
	if err != nil {
		return paneInputMode{}, err
	}
	if len(out) == 0 {
		return paneInputMode{}, errors.New("tmux did not report pane input mode")
	}
	flags := strings.Fields(out[0])
	if len(flags) != 3 {
		return paneInputMode{}, fmt.Errorf("unexpected pane input mode %q", out[0])
	}
	return paneInputMode{mouse: flags[0] == "1", sgr: flags[1] == "1", alternate: flags[2] == "1"}, nil
}

// sendWheel forwards one wheel event to an application. Normal-screen
// scrollback is deliberately handled by Pane: tmux copy mode belongs to the
// control client and its viewport is not part of the streamed pane output.
func (c *Conn) sendWheel(pane string, x, y int, direction WheelDirection, mode paneInputMode) error {
	if mode.mouse {
		button := ansi.MouseWheelUp
		if direction == WheelDown {
			button = ansi.MouseWheelDown
		}
		encoded := ansi.EncodeMouseButton(button, false, false, false, false)
		seq := ansi.MouseX10(encoded, x, y)
		if mode.sgr {
			seq = ansi.MouseSgr(encoded, x, y, false)
		}
		return c.SendText(pane, seq)
	}

	if mode.alternate {
		key := "Up"
		if direction == WheelDown {
			key = "Down"
		}
		_, err := c.Command(fmt.Sprintf("send-keys -N 3 -t %s %s", pane, key))
		return err
	}
	return nil
}

// Resize tells tmux the dimensions of this control-mode client. It is only
// used while Orbit is explicitly driving a pane; the read-only dashboard
// preview keeps observing the pane at its existing size.
func (c *Conn) Resize(width, height int) error {
	_, err := c.Command(fmt.Sprintf("refresh-client -C %dx%d", width, height))
	return err
}

func (c *Conn) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()

	c.ptmx.Close()
	if c.cmd.Process != nil {
		c.cmd.Process.Kill()
	}
	c.cmd.Wait()
	return nil
}

// shutdown wakes anything blocked on the connection once the reader stops.
func (c *Conn) shutdown() {
	c.mu.Lock()
	c.closed = true
	pending := c.pending
	c.pending = nil
	c.mu.Unlock()

	for _, ch := range pending {
		ch <- reply{err: ErrClosed}
	}
	close(c.done)
	close(c.notes)
}

// readFrom runs the protocol state machine. Split out from the pty so the
// framing can be tested against a canned transcript.
func (c *Conn) readFrom(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), maxLine)

	var block []string
	var inBlock, solicited bool

	for sc.Scan() {
		line := clean(sc.Text())
		switch {
		case strings.HasPrefix(line, "%begin"):
			block, inBlock = nil, true
			// The handshake block arrives with flags 0 and with nothing
			// outstanding. Both guards matter: flags alone is undocumented,
			// and an empty queue alone would misread a spontaneous block.
			_, flags, _ := parseBlockHeader(line)
			solicited = flags != 0 && c.hasPending()

		case strings.HasPrefix(line, "%end"), strings.HasPrefix(line, "%error"):
			if inBlock && solicited {
				var err error
				if strings.HasPrefix(line, "%error") {
					err = fmt.Errorf("tmux: %s", strings.Join(block, "; "))
				}
				c.deliver(reply{lines: block, err: err})
			}
			if inBlock && !solicited {
				c.readyOnce.Do(func() { close(c.ready) })
			}
			block, inBlock, solicited = nil, false, false

		case inBlock:
			// Blocks are atomic — tmux does not interleave notifications
			// inside one — so everything here belongs to the reply.
			block = append(block, line)

		case strings.HasPrefix(line, "%"):
			if n, ok := parseNotification(line); ok {
				select {
				case c.notes <- n:
				case <-c.done:
					return
				}
			}
		}
	}
}

func (c *Conn) hasPending() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.pending) > 0
}

// deliver hands a reply to the oldest outstanding command. tmux answers in the
// order it was asked, and the serial in the header is the server's own counter
// rather than anything we chose, so position is the only thing we can match on.
func (c *Conn) deliver(r reply) {
	c.mu.Lock()
	if len(c.pending) == 0 {
		c.mu.Unlock()
		return
	}
	ch := c.pending[0]
	c.pending = c.pending[1:]
	c.mu.Unlock()
	ch <- r // buffered; never blocks even if the caller timed out
}

// clean strips the wrapping the pty and the protocol add around every line.
//
// The whole control-mode stream is one DCS sequence: the first line arrives
// prefixed with ESC P 1000 p, and the last is followed by ST. And a pty ends
// lines with CRLF, so the scanner's line still carries the CR.
func clean(s string) string {
	if rest, ok := strings.CutPrefix(s, "\x1bP"); ok {
		if i := strings.IndexByte(rest, 'p'); i >= 0 {
			s = rest[i+1:]
		}
	}
	s = strings.TrimSuffix(s, "\x1b\\")
	return strings.TrimRight(s, "\r")
}

// parseBlockHeader reads "%begin <timestamp> <serial> <flags>".
func parseBlockHeader(line string) (serial, flags int, ok bool) {
	f := strings.Fields(line)
	if len(f) < 4 {
		return 0, 0, false
	}
	serial, err1 := strconv.Atoi(f[2])
	flags, err2 := strconv.Atoi(f[3])
	return serial, flags, err1 == nil && err2 == nil
}

// parseNotification splits "%kind rest…", decoding %output specially.
func parseNotification(line string) (Notification, bool) {
	line = strings.TrimPrefix(line, "%")
	kind, rest, _ := strings.Cut(line, " ")
	if kind == "" {
		return Notification{}, false
	}
	n := Notification{Kind: kind, Args: rest}
	if kind == "output" {
		pane, data, ok := strings.Cut(rest, " ")
		if !ok {
			// A pane that emitted nothing but a newline still counts.
			pane, data = rest, ""
		}
		n.Pane, n.Data = pane, unescape(data)
	}
	return n, true
}

// unescape reverses tmux's octal escaping of non-printable bytes: a literal
// backslash followed by three octal digits. Anything else after a backslash is
// left as it stands rather than guessed at — a corrupted screen is easier to
// diagnose than silently dropped bytes.
func unescape(s string) []byte {
	if !strings.ContainsRune(s, '\\') {
		return []byte(s)
	}
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); {
		if s[i] != '\\' || i+3 >= len(s) {
			out = append(out, s[i])
			i++
			continue
		}
		v, err := strconv.ParseUint(s[i+1:i+4], 8, 8)
		if err != nil {
			out = append(out, s[i])
			i++
			continue
		}
		out = append(out, byte(v))
		i += 4
	}
	return out
}
