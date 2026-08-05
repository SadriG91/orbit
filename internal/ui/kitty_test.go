package ui

import (
	"bytes"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/sadrig91/orbit/internal/config"
	"github.com/sadrig91/orbit/internal/format"
	"github.com/sadrig91/orbit/internal/session"
)

// The logo has to occupy exactly the two columns the text tag did, or every
// row in the list shifts. lipgloss measures the placeholder cells, so this is
// the check that catches a misaligned layout without a terminal.
func TestLogoCellsAreTwoColumns(t *testing.T) {
	for _, a := range session.AllAgents {
		if got := lipgloss.Width(LogoCells(a, "")); got != logoCols {
			t.Errorf("%s: logo measures %d columns, want %d", a, got, logoCols)
		}
	}
	if got := lipgloss.Width("cl"); got != logoCols {
		t.Errorf("text fallback measures %d columns, want %d", got, logoCols)
	}
}

// The image id travels in the placeholder's foreground colour; if that encoding
// drifts the terminal renders the wrong logo, or none.
func TestLogoCellsEncodeImageID(t *testing.T) {
	for _, a := range session.AllAgents {
		id := imageID(a)
		want := "38;2;" +
			format.Itoa((id>>16)&0xff) + ";" + format.Itoa((id>>8)&0xff) + ";" + format.Itoa(id&0xff) + "m"
		if !strings.Contains(LogoCells(a, ""), want) {
			t.Errorf("%s: placeholder missing id encoding %q", a, want)
		}
	}
	// Ids must be distinct, or agents would share a mark.
	seen := map[int]bool{}
	for _, a := range session.AllAgents {
		if seen[imageID(a)] {
			t.Errorf("duplicate image id for %s", a)
		}
		seen[imageID(a)] = true
	}
}

func TestTransmitLogosChunking(t *testing.T) {
	var buf bytes.Buffer
	if err := TransmitLogos(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, a := range session.AllAgents {
		id := format.Itoa(imageID(a))
		if !strings.Contains(out, "\x1b_Ga=t,f=100,i="+id+",q=2,m=") {
			t.Errorf("%s: no transmit command", a)
		}
		// Without a virtual placement the placeholder cells reference nothing.
		if !strings.Contains(out, "\x1b_Ga=p,i="+id+",U=1,c=2,r=1,q=2\x1b\\") {
			t.Errorf("%s: no virtual placement", a)
		}
	}
	// Kitty caps payload chunks at 4096 base64 bytes.
	for _, part := range strings.Split(out, "\x1b_G")[1:] {
		body := part
		if i := strings.Index(body, ";"); i >= 0 {
			body = body[i+1:]
		} else {
			continue
		}
		if i := strings.Index(body, "\x1b\\"); i >= 0 {
			body = body[:i]
		}
		if len(body) > 4096 {
			t.Errorf("payload chunk is %d bytes, over the 4096 limit", len(body))
		}
	}
}

// The shipped default is auto, which only holds up because the capability is
// detected rather than assumed: a terminal that can't composite Kitty
// graphics must get the text tags, not mojibake where the marks would be.
func TestIconModeDefaultIsAutoAndDegrades(t *testing.T) {
	cfg, err := config.LoadDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Icons != "auto" {
		t.Errorf("shipped default is icons = %q, want auto", cfg.Icons)
	}

	clear := func() {
		t.Setenv("TMUX", "")
		t.Setenv("KITTY_WINDOW_ID", "")
		t.Setenv("TERM", "xterm-256color")
		t.Setenv("TERM_PROGRAM", "")
		t.Setenv("GHOSTTY_RESOURCES_DIR", "")
	}

	clear()
	t.Setenv("TERM_PROGRAM", "ghostty")
	if got := ResolveIconMode(cfg.Icons); got != IconLogo {
		t.Errorf("auto in Ghostty resolved to %v, want the logos", got)
	}
	clear()
	t.Setenv("KITTY_WINDOW_ID", "1")
	if got := ResolveIconMode(cfg.Icons); got != IconLogo {
		t.Errorf("auto in Kitty resolved to %v, want the logos", got)
	}

	// Everywhere else, the default must come out as text.
	clear()
	t.Setenv("TERM_PROGRAM", "Apple_Terminal")
	if got := ResolveIconMode(cfg.Icons); got != IconText {
		t.Errorf("auto in a plain terminal resolved to %v, want text", got)
	}
	clear()
	t.Setenv("TERM_PROGRAM", "ghostty")
	t.Setenv("TMUX", "/tmp/fake,1,0")
	if ResolveIconMode(cfg.Icons) != IconText {
		t.Error("logos must stay off inside tmux — placeholders aren't passed through")
	}
	// And an explicit choice still wins over the detection either way.
	clear()
	t.Setenv("TERM_PROGRAM", "ghostty")
	if ResolveIconMode("text") != IconText {
		t.Error("icons = text was overridden by the terminal's capability")
	}
}
