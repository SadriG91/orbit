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
	_, err := io.WriteString(c.ptmx, cmd+"\n")
	c.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("write command: %w", err)
	}

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
