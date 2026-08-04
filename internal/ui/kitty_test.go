package ui

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
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

func TestIconModeDefaultsToText(t *testing.T) {
	t.Setenv("ORBIT_ICONS", "")
	if ResolveIconMode(os.Getenv("ORBIT_ICONS")) != IconText {
		t.Error("icons should default to text: logos need a Kitty-graphics terminal")
	}
	t.Setenv("ORBIT_ICONS", "logo")
	t.Setenv("TMUX", "/tmp/fake,1,0")
	if ResolveIconMode(os.Getenv("ORBIT_ICONS")) != IconText {
		t.Error("logos must stay off inside tmux — placeholders aren't passed through")
	}
}
