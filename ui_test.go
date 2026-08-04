package main

import (
	"strings"
	"testing"
	"time"
)

// Renders the full UI against synthetic sessions in every state, so layout
// regressions show up without needing a terminal.
func TestViewRenders(t *testing.T) {
	now := time.Now()
	m := newModel(attachInline)
	m.w, m.h = 120, 30
	m.all = []*Session{
		{Agent: Claude, ID: "aaaaaaaa-1", Cwd: home("work", "api-gateway"), Branch: "batch-runner",
			Title: "Refactor batch runner", Last: "now run the tests", Msgs: 46, Modified: now.Add(-2 * time.Minute),
			hint: HintMaybeApproval, Tmux: &Tmux{Name: "cl-work-api-gateway-aaaaaaaa", AgentRunning: true}},
		{Agent: Codex, ID: "bbbbbbbb-2", Cwd: home("src", "widgets"), Branch: "main",
			Title: "what other improvements can we bring to this project?", Msgs: 12, Modified: now.Add(-40 * time.Minute),
			hint: HintDone, Tmux: &Tmux{Name: "cx-src-widgets-bbbbbbbb", AgentRunning: true}},
		{Agent: Copilot, ID: "cccccccc-3", Cwd: home("work", "docs-site"),
			Title: "Integrate GitHub Copilot in Actions", Msgs: 8, Modified: now.Add(-72 * time.Hour)},
		{Agent: Claude, ID: "dddddddd-4", Cwd: home(), Title: "Investigate slow terminal startup time",
			Last: "no its fine we leave that as is", Msgs: 90, Modified: now.Add(-3 * time.Hour)},
	}
	for _, s := range m.all {
		s.resolve(now)
	}
	sortSessions(m.all)
	m.rebuild()

	if len(m.view) != 4 {
		t.Fatalf("expected 4 visible sessions, got %d", len(m.view))
	}
	// Highest-priority state sorts first.
	if got := m.view[0].State; got != NeedsApproval {
		t.Errorf("expected NeedsApproval first, got %v", got)
	}

	out := m.View()
	for _, want := range []string{"╔═╗╦═╗╔╗", "needs you", "your turn", "Refactor batch runner", "work/api-gateway", "attach"} {
		if !strings.Contains(out, want) {
			t.Errorf("view missing %q", want)
		}
	}
	for i, line := range strings.Split(out, "\n") {
		if n := len([]rune(stripANSI(line))); n > m.w {
			t.Errorf("line %d is %d cols, wider than %d: %q", i, n, m.w, stripANSI(line))
		}
	}
	t.Log("\n" + out)

	// The full-size banner only appears on a tall, wide terminal.
	m.w, m.h = 150, 44
	big := m.View()
	if !strings.Contains(big, "██████╗") {
		t.Error("full banner missing at 150x44")
	}
	for i, line := range strings.Split(big, "\n") {
		if n := len([]rune(stripANSI(line))); n > m.w {
			t.Errorf("big layout line %d is %d cols, wider than %d", i, n, m.w)
		}
	}
	t.Log("\n" + big)
}

func TestFilterAndDetail(t *testing.T) {
	now := time.Now()
	m := newModel(attachInline)
	m.w, m.h = 120, 30
	m.all = []*Session{
		{Agent: Claude, ID: "a", Cwd: home("work", "api-gateway"), Title: "Refactor batch runner", Modified: now},
		{Agent: Codex, ID: "b", Cwd: home("src", "widgets"), Title: "Tune widget layout", Modified: now},
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

func TestOldUntitledHiddenUntilShowAll(t *testing.T) {
	m := newModel(attachInline)
	m.w, m.h = 120, 30
	m.all = []*Session{
		{Agent: Claude, ID: "old", Cwd: home(), Title: "", Modified: time.Now().AddDate(0, 0, -60)},
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
