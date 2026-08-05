package ui

import (
	_ "embed"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/sadrig91/orbit/internal/session"
)

// Official brand marks, from Simple Icons (CC0), rasterised to 64x64 RGBA and
// recoloured for a dark terminal. Trademarks remain with their owners; these
// identify the agent a session belongs to and nothing more.
//
//go:embed assets/claude.png
var claudePNG []byte

//go:embed assets/codex.png
var codexPNG []byte

//go:embed assets/copilot.png
var copilotPNG []byte

func logoPNG(a session.Agent) []byte {
	switch a {
	case session.Codex:
		return codexPNG
	case session.Copilot:
		return copilotPNG
	}
	return claudePNG
}

// Stable per-agent image ids. The id is carried in the placeholder cell's
// foreground colour, so it has to fit in 24 bits and not be zero.
func imageID(a session.Agent) int { return 0x0B17 + int(a) + 1 }

// The image occupies exactly the two columns the "cl"/"cx"/"cp" tag used to.
const logoCols, logoRows = 2, 1

// Kitty's rowcolumn diacritics. Only the first few are needed for a 2x1
// placement, but the order is fixed by the spec and must not be reordered.
var rowColumnDiacritics = []rune{
	0x0305, 0x030D, 0x030E, 0x0310, 0x0312, 0x033D, 0x033E, 0x033F,
}

// placeholder is the private-use character Kitty reserves for virtual image
// placements; the terminal swaps these cells for the image.
const placeholder = '\U0010EEEE'

// IconMode picks how an agent is identified in the list.
type IconMode int

const (
	IconText IconMode = iota // cl / cx / cp — works in any terminal
	IconLogo                 // the real brand marks, via Kitty graphics
)

// ResolveIconMode turns the configured string into a mode. Everything except
// an explicit "logo"/"auto" on a terminal that composites Kitty graphics ends
// up as text: a wrong guess renders mojibake rather than degrading quietly,
// so the capability is detected rather than assumed — which is what makes
// "auto" safe to ship as the default.
func ResolveIconMode(mode string) IconMode {
	if (mode == "logo" || mode == "auto") && LogosSupported() {
		return IconLogo
	}
	return IconText
}

// LogosSupported reports whether the terminal can composite Kitty graphics.
// tmux is excluded: it does not pass unicode placeholders through, so a session
// pane would show mojibake. orbit's dashboard runs outside tmux, so this only
// disables logos in the unusual case of running orbit itself inside one.
func LogosSupported() bool {
	if os.Getenv("TMUX") != "" {
		return false
	}
	if os.Getenv("KITTY_WINDOW_ID") != "" || strings.Contains(os.Getenv("TERM"), "kitty") {
		return true
	}
	return isGhostty()
}

// TransmitLogos uploads each mark once and creates a virtual placement for it.
// Virtual placements aren't drawn anywhere themselves — they only become
// visible where placeholder cells reference them, which is what lets the images
// flow with the text instead of being pinned to screen coordinates.
func TransmitLogos(w io.Writer) error {
	for _, a := range session.AllAgents {
		id := imageID(a)
		data := base64.StdEncoding.EncodeToString(logoPNG(a))

		// Payloads must be sent in chunks of at most 4096 base64 bytes.
		const chunk = 4096
		first := true
		for len(data) > 0 {
			n := min(chunk, len(data))
			more := 0
			if n < len(data) {
				more = 1
			}
			var err error
			if first {
				// f=100: PNG. q=2: stay quiet, we have no reader for replies.
				_, err = fmt.Fprintf(w, "\x1b_Ga=t,f=100,i=%d,q=2,m=%d;%s\x1b\\", id, more, data[:n])
				first = false
			} else {
				_, err = fmt.Fprintf(w, "\x1b_Gm=%d;%s\x1b\\", more, data[:n])
			}
			if err != nil {
				return err
			}
			data = data[n:]
		}
		if _, err := fmt.Fprintf(w, "\x1b_Ga=p,i=%d,U=1,c=%d,r=%d,q=2\x1b\\",
			id, logoCols, logoRows); err != nil {
			return err
		}
	}
	return nil
}

// LogoCells renders the placeholder cells for an agent's mark. The foreground
// colour is not decorative: its RGB channels carry the image id.
func LogoCells(a session.Agent, bg string) string {
	id := imageID(a)
	r, g, b := (id>>16)&0xff, (id>>8)&0xff, id&0xff

	var sb strings.Builder
	sb.WriteString(bg)
	fmt.Fprintf(&sb, "\x1b[38;2;%d;%d;%dm", r, g, b)
	for col := 0; col < logoCols; col++ {
		sb.WriteRune(placeholder)
		sb.WriteRune(rowColumnDiacritics[0])   // row 0
		sb.WriteRune(rowColumnDiacritics[col]) // column
	}
	sb.WriteString("\x1b[39m")
	if bg != "" {
		sb.WriteString("\x1b[49m")
	}
	return sb.String()
}

// ProbeLogos prints the marks next to their text fallback so the rendering can
// be eyeballed in a real terminal — there is no in-band way to ask whether the
// image actually appeared.
func ProbeLogos(w io.Writer) error {
	fmt.Fprintf(w, "terminal: TERM_PROGRAM=%q TERM=%q  tmux=%v\n",
		os.Getenv("TERM_PROGRAM"), os.Getenv("TERM"), os.Getenv("TMUX") != "")
	fmt.Fprintf(w, "kitty graphics detected: %v\n\n", LogosSupported())
	if err := TransmitLogos(w); err != nil {
		return err
	}
	for _, a := range session.AllAgents {
		fmt.Fprintf(w, "  %s  %-8s  (text fallback: %s)\n", LogoCells(a, ""), a, a.Tag())
	}
	fmt.Fprint(w, "\nIf you see three logos above, run: ORBIT_ICONS=logo orbit\n")
	return nil
}
