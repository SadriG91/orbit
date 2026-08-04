package main

import (
	"fmt"
	"os"
	"strings"
)

// Notifier fires a desktop notification when a session starts wanting your
// attention. It uses OSC 9, which Ghostty implements natively — writing the
// sequence to the controlling terminal is cheaper and quieter than shelling
// out to osascript, and it inherits Ghostty's own notification settings.
type Notifier struct {
	enabled bool
	tty     *os.File
	seen    map[string]State
	primed  bool // don't fire for whatever was already running at startup
}

func NewNotifier(enabled bool) *Notifier {
	n := &Notifier{enabled: enabled, seen: map[string]State{}}
	if enabled {
		// Not fatal: orbit still works headless or under a pipe, just quietly.
		n.tty, _ = os.OpenFile("/dev/tty", os.O_WRONLY, 0)
	}
	return n
}

func (n *Notifier) Update(sessions []*Session) {
	if n == nil || !n.enabled {
		return
	}
	next := map[string]State{}
	for _, s := range sessions {
		next[s.ID] = s.State
		if !n.primed {
			continue
		}
		prev, known := n.seen[s.ID]
		if !known || prev == s.State {
			continue
		}
		switch s.State {
		case NeedsApproval:
			n.post(s.Agent.String() + " needs approval — " + s.ShortCwd() + ": " + s.Name())
		case YourTurn:
			n.post(s.Agent.String() + " finished — " + s.ShortCwd() + ": " + s.Name())
		}
	}
	n.seen, n.primed = next, true
}

func (n *Notifier) post(body string) {
	if n.tty == nil {
		return
	}
	// OSC 9 ; text ST — no embedded ESC or BEL, or we'd terminate it early.
	body = strings.Map(func(r rune) rune {
		if r < 0x20 {
			return -1
		}
		return r
	}, body)
	fmt.Fprintf(n.tty, "\033]9;%s\033\\", truncate(body, 120))
}

func (n *Notifier) Close() {
	if n != nil && n.tty != nil {
		n.tty.Close()
	}
}
