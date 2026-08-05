package ui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/sadrig91/orbit/internal/config"
	"github.com/sadrig91/orbit/internal/format"
	"github.com/sadrig91/orbit/internal/session"
	"github.com/sadrig91/orbit/internal/tmux"
)

// Renders the full UI against synthetic sessions in every state, so layout
// regressions show up without needing a terminal.
func TestViewRenders(t *testing.T) {
	now := time.Now()
	m := newTestModel(testConfig(), attachInline)
	m.w, m.h = 120, 30
	m.all = []*session.Session{
		{Agent: session.Claude, ID: "aaaaaaaa-1", Cwd: format.Home("work", "api-gateway"), Branch: "batch-runner",
			Title: "Refactor batch runner", Last: "now run the tests", Msgs: 46, Modified: now.Add(-2 * time.Minute),
			State: session.NeedsApproval, Tmux: &tmux.Session{Name: "cl-work-api-gateway-aaaaaaaa", AgentRunning: true}},
		{Agent: session.Codex, ID: "bbbbbbbb-2", Cwd: format.Home("src", "widgets"), Branch: "main",
			Title: "what other improvements can we bring to this project?", Msgs: 12, Modified: now.Add(-40 * time.Minute),
			State: session.YourTurn, Tmux: &tmux.Session{Name: "cx-src-widgets-bbbbbbbb", AgentRunning: true}},
		{Agent: session.Copilot, ID: "cccccccc-3", Cwd: format.Home("work", "docs-site"),
			Title: "Integrate GitHub session.Copilot in Actions", Msgs: 8, Modified: now.Add(-72 * time.Hour)},
		{Agent: session.Claude, ID: "dddddddd-4", Cwd: format.Home(), Title: "Investigate slow terminal startup time",
			Last: "no its fine we leave that as is", Msgs: 90, Modified: now.Add(-3 * time.Hour)},
	}
	session.SortSessions(m.all)
	m.rebuild()

	if len(m.view) != 4 {
		t.Fatalf("expected 4 visible sessions, got %d", len(m.view))
	}
	// Highest-priority state sorts first.
	if got := m.view[0].State; got != session.NeedsApproval {
		t.Errorf("expected session.NeedsApproval first, got %v", got)
	}

	out := m.render()
	for _, want := range []string{"╔═╗╦═╗╔╗", "needs attention", "finished", "Refactor batch runner", "work/api-gateway", "attach"} {
		if !strings.Contains(out, want) {
			t.Errorf("view missing %q", want)
		}
	}
	if head := stripANSI(m.header()); strings.Contains(head, "finished") {
		t.Errorf("completed turns should stay on their rows, not become a header metric: %q", head)
	}
	assertFrame(t, m, out)
	t.Log("\n" + out)

	// The full-size banner only appears on a tall, wide terminal.
	m.w, m.h = 150, 44
	big := m.render()
	if !strings.Contains(big, "██████╗") {
		t.Error("full banner missing at 150x44")
	}
	assertFrame(t, m, big)
	t.Log("\n" + big)

	// Group headings must fit the keyboard-first 80x24 fallback as real rows,
	// without pushing the selected session through the bottom border.
	m.w, m.h = 80, 24
	m.group = groupProject
	m.rebuild()
	compact := m.render()
	for _, want := range []string{"SESSIONS · RECENT · BY AGE ·…", "▸ work/api-gateway · 1"} {
		if !strings.Contains(compact, want) {
			t.Errorf("80x24 grouped view missing %q", want)
		}
	}
	assertFrameSize(t, m, compact)
}

func TestShortcutHelpKeepsTheFooterFocused(t *testing.T) {
	m := newTestModel(testConfig(), attachInline)
	m.w, m.h = 80, 24
	m.all = []*session.Session{{
		Agent: session.Claude, ID: "a", Cwd: format.Home("work", "orbit"),
		Title: "Improve keyboard shortcuts", Modified: time.Now(),
	}}
	m.rebuild()

	footer := stripANSI(m.footer())
	for _, want := range []string{"attach", "drive", "shortcuts", "summarised"} {
		if !strings.Contains(footer, want) {
			t.Errorf("focused footer missing %q: %q", want, footer)
		}
	}
	for _, hidden := range []string{"sort", "group", "kill", "quit"} {
		if strings.Contains(footer, hidden) {
			t.Errorf("secondary action %q still crowds the footer: %q", hidden, footer)
		}
	}
	if !strings.HasSuffix(footer, "0/1 summarised") {
		t.Errorf("summary progress is not anchored to the footer's right edge: %q", footer)
	}

	m.key(tea.KeyPressMsg{Code: '?', Text: "?"})
	if !m.showHelp {
		t.Fatal("? did not open shortcut help")
	}
	help := stripANSI(m.render())
	for _, want := range []string{"KEYBOARD SHORTCUTS", "previous session", "focus live pane", "search transcripts", "sort / group", "ctrl+g", "full screen", "quit"} {
		if !strings.Contains(help, want) {
			t.Errorf("shortcut help missing %q:\n%s", want, help)
		}
	}
	for i, line := range strings.Split(help, "\n") {
		if width := lipgloss.Width(line); width > m.w {
			t.Errorf("shortcut help line %d is %d cols, wider than %d: %q", i, width, m.w, line)
		}
	}
	if rows := len(strings.Split(help, "\n")); rows != m.h {
		t.Errorf("shortcut help is %d rows, want %d", rows, m.h)
	}

	// The helper is modal: command keys should not act on the dashboard under it.
	m.key(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if m.showAll {
		t.Error("dashboard command ran while shortcut help was open")
	}
	m.key(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.showHelp {
		t.Error("escape did not close shortcut help")
	}
}

func TestStatusMetricsDoNotRenderAsFilledPills(t *testing.T) {
	for name, rendered := range map[string]string{
		"header metric": statusMetric("▲", "needs attention", 2, cAmber),
		"detail state":  stateMarker("◆", "finished", cCyan),
	} {
		if strings.Contains(rendered, "\x1b[48;") {
			t.Errorf("%s still has a filled background: %q", name, rendered)
		}
	}
}

func TestTransientStatusUsesTheTopRightHeaderSlot(t *testing.T) {
	m := newTestModel(testConfig(), attachInline)
	m.w, m.h = 100, 30
	m.say("Sessions focused · terminal remains live")

	header := stripANSI(m.header())
	lines := strings.Split(header, "\n")
	if len(lines) == 0 || !strings.Contains(lines[0], "Sessions focused · terminal remains live") {
		t.Errorf("transient status is not in the header's first row:\n%s", header)
	}
	if footer := stripANSI(m.footer()); strings.Contains(footer, "Sessions focused") {
		t.Errorf("transient status still replaced the footer controls: %q", footer)
	}
	if footer := stripANSI(m.footer()); !strings.Contains(footer, "attach") {
		t.Errorf("footer key map disappeared while status was visible: %q", footer)
	}
}

func TestFilterAndDetail(t *testing.T) {
	now := time.Now()
	m := newTestModel(testConfig(), attachInline)
	m.w, m.h = 120, 30
	m.all = []*session.Session{
		{Agent: session.Claude, ID: "a", Cwd: format.Home("work", "api-gateway"), Title: "Refactor batch runner", Modified: now},
		{Agent: session.Codex, ID: "b", Cwd: format.Home("src", "widgets"), Title: "Tune widget layout", Modified: now},
	}
	m.rebuild()
	m.filter.SetValue("widgets")
	m.rebuild()
	if len(m.view) != 1 || m.view[0].ID != "b" {
		t.Fatalf("filter by cwd failed: %+v", m.view)
	}
	if d := strings.Join(m.detail(50, 20), "\n"); !strings.Contains(d, "Tune widget layout") {
		t.Errorf("detail pane missing title:\n%s", d)
	}
}

func TestDetailUsesAgentLogoWithoutDroppingItsName(t *testing.T) {
	m := newTestModel(testConfig(), attachInline)
	m.icons = IconLogo
	m.all = []*session.Session{{
		Agent: session.Claude, ID: "a", Cwd: format.Home("work", "orbit"),
		Title: "Improve detail metadata", Modified: time.Now(),
	}}
	m.rebuild()

	detail := strings.Join(m.detail(60, 20), "\n")
	if !strings.Contains(detail, LogoCells(session.Claude, "")) {
		t.Error("detail metadata did not use the configured agent logo")
	}
	if !strings.Contains(stripANSI(detail), "claude") {
		t.Error("detail metadata dropped the accessible agent name")
	}
}

func TestOldUntitledHiddenUntilShowAll(t *testing.T) {
	m := newTestModel(testConfig(), attachInline)
	m.w, m.h = 120, 30
	m.all = []*session.Session{
		{Agent: session.Claude, ID: "old", Cwd: format.Home(), Title: "", Modified: time.Now().AddDate(0, 0, -60)},
	}
	m.rebuild()
	if len(m.view) != 0 {
		t.Errorf("stale untitled session should be hidden by default")
	}
	m.showAll = true
	m.rebuild()
	if len(m.view) != 1 {
		t.Errorf("showAll should reveal it")
	}
}

func TestGroupingKeepsSectionsContiguousWithoutChangingTheSort(t *testing.T) {
	m := newTestModel(testConfig(), attachInline)
	m.w, m.h = 100, 24
	now := time.Now()
	m.all = []*session.Session{
		{Agent: session.Codex, ID: "b-new", Cwd: "/work/b", Title: "new b", Modified: now},
		{Agent: session.Claude, ID: "a-new", Cwd: "/work/a", Title: "new a", Modified: now.Add(-time.Minute)},
		{Agent: session.Copilot, ID: "b-old", Cwd: "/work/b", Title: "old b", Modified: now.Add(-2 * time.Minute)},
		{Agent: session.Codex, ID: "a-old", Cwd: "/work/a", Title: "old a", Modified: now.Add(-3 * time.Minute)},
	}
	session.SortSessionsBy(m.all, session.SortAge)
	m.sort = session.SortAge
	m.group = groupProject
	m.rebuild()

	got := []string{m.view[0].ID, m.view[1].ID, m.view[2].ID, m.view[3].ID}
	want := []string{"b-new", "b-old", "a-new", "a-old"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("project groups = %v, want %v", got, want)
	}
	if m.sort != session.SortAge {
		t.Errorf("grouping changed sort to %v", m.sort)
	}

	list := strings.Join(m.list(50, 14), "\n")
	for _, heading := range []string{"▸ work/b · 2", "▸ work/a · 2"} {
		if strings.Count(list, heading) != 1 {
			t.Errorf("heading %q was not rendered exactly once:\n%s", heading, list)
		}
	}
}

func TestGroupKeyCyclesThroughProjectAgentAndOff(t *testing.T) {
	m := newTestModel(testConfig(), attachInline)
	m.all = []*session.Session{
		{Agent: session.Codex, ID: "cx-1", Cwd: "/work/a", Title: "one", Modified: time.Now()},
		{Agent: session.Claude, ID: "cl-1", Cwd: "/work/b", Title: "two", Modified: time.Now().Add(-time.Minute)},
		{Agent: session.Codex, ID: "cx-2", Cwd: "/work/c", Title: "three", Modified: time.Now().Add(-2 * time.Minute)},
	}
	m.rebuild()

	m.key(tea.KeyPressMsg{Code: 'p', Text: "p"})
	if m.group != groupProject {
		t.Fatalf("first p selected %v, want project", m.group)
	}
	m.key(tea.KeyPressMsg{Code: 'p', Text: "p"})
	if m.group != groupAgent {
		t.Fatalf("second p selected %v, want agent", m.group)
	}
	if got := []string{m.view[0].ID, m.view[1].ID, m.view[2].ID}; strings.Join(got, ",") != "cx-1,cx-2,cl-1" {
		t.Errorf("agent grouping did not make sections contiguous: %v", got)
	}
	list := strings.Join(m.list(50, 12), "\n")
	if !strings.Contains(list, "▸ codex · 2") || !strings.Contains(list, "▸ claude · 1") {
		t.Errorf("agent headings missing:\n%s", list)
	}

	m.key(tea.KeyPressMsg{Code: 'p', Text: "p"})
	if m.group != groupNone {
		t.Fatalf("third p selected %v, want off", m.group)
	}
}

func TestGroupedListAccountsForHeadingHeightWhileScrolling(t *testing.T) {
	m := newTestModel(testConfig(), attachInline)
	m.group = groupProject
	m.all = []*session.Session{
		{Agent: session.Claude, ID: "a", Cwd: "/work/a", Title: "first", Modified: time.Now()},
		{Agent: session.Claude, ID: "b", Cwd: "/work/b", Title: "second", Modified: time.Now().Add(-time.Minute)},
		{Agent: session.Claude, ID: "c", Cwd: "/work/c", Title: "selected", Modified: time.Now().Add(-2 * time.Minute)},
	}
	m.rebuild()
	m.cursor = 2
	lines := m.list(40, 5)
	if len(lines) > 5 {
		t.Fatalf("grouped list used %d lines in a five-line pane", len(lines))
	}
	if !strings.Contains(strings.Join(lines, "\n"), "selected") {
		t.Errorf("selected row scrolled out behind headings:\n%s", strings.Join(lines, "\n"))
	}
}

// assertFrame checks the frame fills its terminal exactly and that no row got
// wrapped. Width alone isn't enough: wrapping makes lines shorter, not wider,
// so it slips past a max-width check — pin the row content to one line instead.
func assertFrame(t *testing.T, m *Model, frame string) {
	t.Helper()
	assertFrameSize(t, m, frame)
	lines := strings.Split(frame, "\n")
	var joined bool
	for _, line := range lines {
		plain := stripANSI(line)
		if strings.Contains(plain, "Refactor batch runner") && strings.Contains(plain, "needs attention") {
			joined = true
		}
	}
	if !joined {
		t.Error("session row wrapped: title and state label landed on different lines")
	}
}

func assertFrameSize(t *testing.T, m *Model, frame string) {
	t.Helper()
	lines := strings.Split(frame, "\n")
	for i, line := range lines {
		// Display width, not a rune count. A logo cell is a placeholder rune
		// plus the row/column diacritics that tell the terminal which part of
		// the image to draw — six runes for the two columns it actually
		// occupies. Counting runes measures the encoding rather than the
		// screen, and reports every row with a logo in it as overflowing.
		if n := lipgloss.Width(stripANSI(line)); n > m.w {
			t.Errorf("line %d is %d cols, wider than %d: %q", i, n, m.w, stripANSI(line))
		}
	}
	if len(lines) != m.h {
		t.Errorf("frame is %d rows, want exactly %d", len(lines), m.h)
	}
}

// testConfig is the shipped default, so the tests exercise what users get.
func testConfig() config.Config {
	cfg, err := config.LoadDefaults()
	if err != nil {
		panic(err)
	}
	return cfg
}

func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			for i < len(s) && s[i] != 'm' {
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

// newTestModel builds a Model straight from a config, bypassing flag handling.
func newTestModel(cfg config.Config, _ attachMode) *Model { return New(cfg, "inline", "dev") }
