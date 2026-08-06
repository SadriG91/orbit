package ui

import (
	"image/color"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/sadrig91/orbit/internal/dispatch"
	"github.com/sadrig91/orbit/internal/format"
	"github.com/sadrig91/orbit/internal/session"

	"charm.land/lipgloss/v2"
)

// ANSI Shadow. Rendered with a vertical fade, so it reads as a light source
// rather than a wall of green.
var bannerFull = []string{
	` ██████╗ ██████╗ ██████╗ ██╗████████╗`,
	`██╔═══██╗██╔══██╗██╔══██╗██║╚══██╔══╝`,
	`██║   ██║██████╔╝██████╔╝██║   ██║   `,
	`██║   ██║██╔══██╗██╔══██╗██║   ██║   `,
	`╚██████╔╝██║  ██║██████╔╝██║   ██║   `,
	` ╚═════╝ ╚═╝  ╚═╝╚═════╝ ╚═╝   ╚═╝   `,
}

var bannerSmall = []string{
	`╔═╗╦═╗╔╗ ╦╔╦╗`,
	`║ ║╠╦╝╠╩╗║ ║ `,
	`╚═╝╩╚═╚═╝╩ ╩ `,
}

var bannerFade = []color.Color{
	lipgloss.Color("48"), lipgloss.Color("48"), lipgloss.Color("42"),
	lipgloss.Color("36"), lipgloss.Color("30"), lipgloss.Color("29"),
}

var (
	cBorder = lipgloss.Color("238")
	cSel    = lipgloss.Color("236")
	cInk    = lipgloss.Color("232")

	// Flat, deliberately. v2's BorderForegroundBlend distributes a gradient
	// around the whole ring, which on a three-sided box (the top edge is drawn
	// by hand for the label) lands grey on one side and green on the other.
	paneStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(cBorder).
			Padding(0, 1)

	paneLabel = lipgloss.NewStyle().Foreground(cGreen).Bold(true)
	sTok      = lipgloss.NewStyle().Foreground(lipgloss.Color("101"))
	sGroup    = lipgloss.NewStyle().Foreground(cGreen).Bold(true)
	sHit      = lipgloss.NewStyle().Foreground(cAmber)
	tagline   = lipgloss.NewStyle().Foreground(cDim).Italic(true)
	keyCap    = lipgloss.NewStyle().Foreground(cInk).Background(cGreen).Bold(true).Padding(0, 1)
	keyLabel  = lipgloss.NewStyle().Foreground(cMid)
)

// statusMetric is deliberately not a pill. These are live measurements, not
// buttons or filters, so a small semantic mark and a strong count give them
// enough hierarchy without adding another block of chrome to the header.
func statusMetric(icon, label string, count int, accent color.Color) string {
	iconStyle := lipgloss.NewStyle().Foreground(accent).Bold(true)
	countStyle := lipgloss.NewStyle().Foreground(accent).Bold(true)
	return iconStyle.Render(icon) + " " + sMid.Render(label) + " " + countStyle.Render(format.Itoa(count))
}

func stateMarker(icon, label string, accent color.Color) string {
	style := lipgloss.NewStyle().Foreground(accent).Bold(true)
	return style.Render(icon) + " " + style.Render(label)
}

func banner(width, height int) []string {
	var art []string
	switch {
	case height >= 32 && width >= 74:
		art = bannerFull
	case height >= 28 && width >= 46:
		art = bannerSmall
	default:
		return []string{sTitle.Render("orbit")}
	}
	out := make([]string, len(art))
	// session.Index the fade against the full ramp so the small banner still fades.
	for i, line := range art {
		c := bannerFade[i*len(bannerFade)/len(art)]
		out[i] = lipgloss.NewStyle().Foreground(c).Render(line)
	}
	return out
}

func (m *Model) render() string {
	if m.w == 0 {
		return "starting orbit…"
	}
	if m.terminalFocused && m.embedded != "" && m.terminalZoom {
		return m.renderEmbedded()
	}
	head := m.header()
	foot := m.footer()
	if m.showHelp {
		bodyH := max(5, m.h-lipgloss.Height(head)-lipgloss.Height(foot))
		body := titledPane("keyboard shortcuts · ? or esc to close", m.shortcutHelp(m.w-4, bodyH-3), m.w, bodyH)
		return head + "\n" + body + "\n" + foot
	}
	if m.diagnosticsOpen {
		bodyH := max(5, m.h-lipgloss.Height(head)-lipgloss.Height(foot))
		body := titledPane("diagnostics · D or esc to close", m.diagnosticLines(m.w-4, bodyH-3), m.w, bodyH)
		return head + "\n" + body + "\n" + foot
	}
	if m.killConfirm != nil {
		bodyH := max(5, m.h-lipgloss.Height(head)-lipgloss.Height(foot))
		body := titledPane("confirm kill · input locked", m.killConfirmationLines(m.w-4, bodyH-3), m.w, bodyH)
		return head + "\n" + body + "\n" + foot
	}
	listBoxW, detBoxW, bodyH := m.dashboardPaneSizes()
	const chrome = 4

	scope := "recent"
	if m.showAll {
		scope = "all"
	}
	label := "sessions · " + scope + " · by " + m.sort.String()
	if m.group != groupNone {
		label += " · grouped by " + m.group.String()
	}
	if m.query != "" {
		label = "search · " + m.query
		if m.group != groupNone {
			label += " · grouped by " + m.group.String()
		}
	}
	label = focusPaneLabel(label, !m.terminalFocused && !m.dispatching && !m.newing && m.preparing == nil)
	body := titledPane(label, m.list(listBoxW-chrome, bodyH-3), listBoxW, bodyH)

	if detBoxW > 0 {
		label := "detail"
		var right []string
		if m.dispatching {
			label = focusPaneLabel("dispatch task", true)
			right = m.dispatchLines(detBoxW-chrome, bodyH-3)
		} else if m.newing {
			label = focusPaneLabel("new session", true)
			right = m.newSessionLines(detBoxW-chrome, bodyH-3)
		} else if m.preparing != nil {
			label = "… preparing live pane · " + shortDir(m.preparing.cwd)
			right = m.preparingLines(detBoxW-chrome, bodyH-3)
		} else if m.embedded != "" {
			mode := "unfocused"
			if m.terminalFocused {
				mode = "focused"
			}
			if m.stream != nil && m.stream.Scrolled() {
				mode = "scrollback · " + format.Itoa(m.stream.ScrollOffset()) + " lines up"
			}
			label = focusPaneLabel("terminal · "+mode+" · "+shortDir(m.embeddedCwd), m.terminalFocused)
			right = m.terminalLines(detBoxW-chrome, bodyH-3)
			if !m.terminalFocused {
				right = mutedTerminalLines(right)
			}
		} else if s := m.sel(); s != nil && s.Tmux != nil {
			label = "preview · " + s.ShortCwd()
			if preview := m.selectedTerminalPreview(s, detBoxW-chrome, bodyH-3); preview != nil {
				right = mutedTerminalLines(preview)
			}
		}
		if right == nil {
			right = m.detail(detBoxW-chrome, bodyH-3)
		}
		rightPane := titledPane(label, right, detBoxW, bodyH)
		gap := strings.TrimSuffix(strings.Repeat(" \n", lipgloss.Height(body)), "\n")
		body = lipgloss.JoinHorizontal(lipgloss.Top, body, gap, rightPane)
	}

	return head + "\n" + body + "\n" + foot
}

func (m *Model) activityMark() string {
	if m.reducedMotion {
		return "•"
	}
	return m.spin.View()
}

func focusPaneLabel(label string, focused bool) string {
	if focused {
		return "▶ " + label
	}
	return "  " + label
}

func (m *Model) killConfirmationLines(w, h int) []string {
	prompt := m.killConfirm
	if prompt == nil {
		return nil
	}
	lines := []string{
		"",
		sErr.Bold(true).Render("KILL LIVE SESSION?"),
		"",
		paneLabel.Render("SESSION") + "  " + sNameOn.Render(format.Truncate(format.Clean(prompt.title), max(1, w-11))),
		sDim.Render(format.Truncate(prompt.cwd, w)),
		"",
		sMid.Render(format.Truncate("The tmux process will stop. Its transcript and summary will be kept.", w)),
		"",
		sDim.Render("Press enter or x to kill · esc to cancel"),
	}
	if len(lines) > h {
		lines = lines[:h]
	}
	return lines
}

func (m *Model) preparingLines(w, h int) []string {
	p := m.preparing
	if p == nil {
		return nil
	}
	var out []string
	addWrapped := func(prefix, value string, style lipgloss.Style) {
		for i, line := range wrap(value, max(8, w-lipgloss.Width(prefix))) {
			lead := prefix
			if i > 0 {
				lead = strings.Repeat(" ", lipgloss.Width(prefix))
			}
			out = append(out, lead+style.Render(line))
		}
	}
	phase := "PREPARING " + strings.ToUpper(p.agent.String()) + " SESSION"
	detail := "Orbit is starting the agent inside tmux. The live terminal will appear when the agent is ready."
	out = append(out, "", paneLabel.Render(m.activityMark()+" "+phase), "")
	addWrapped("  ", detail, sName)
	out = append(out, "")
	addWrapped("  transcript  ", p.title, sMid)
	addWrapped("  directory   ", p.cwd, sMid)
	out = append(out, "")
	addWrapped("  ", "The live pane will receive focus automatically when the session is ready.", sDim)
	if len(out) > h {
		out = out[:h]
	}
	return out
}

// dashboardPaneSizes is the single source of truth for both rendering and the
// tmux client size. If those disagree, the agent paints for one geometry and
// Orbit clips it into another.
func (m *Model) dashboardPaneSizes() (listW, detailW, bodyH int) {
	bodyH = m.h - lipgloss.Height(m.header()) - lipgloss.Height(m.footer())
	if bodyH < 5 {
		bodyH = 5
	}
	listW = min(66, max(38, m.w*46/100))
	if m.terminalWide && m.embedded != "" {
		listW = max(28, m.w*30/100)
	}
	detailW = m.w - listW - 1
	if detailW < 30 {
		return m.w, 0, bodyH
	}
	return listW, detailW, bodyH
}

func (m *Model) dispatchLines(w, h int) []string {
	contentW := max(12, w)
	m.filter.SetWidth(contentW)
	m.dispatchDir.SetWidth(contentW)

	ag, _, err := parseDispatchPrompt(m.filter.Value(), m.dispatchTo)
	agent := "@" + m.dispatchTo.String() + " (default)"
	if err == nil && strings.HasPrefix(strings.TrimSpace(m.filter.Value()), "@") {
		agent = "@" + ag.String() + " (from task)"
	}
	taskLabel, agentLabel, dirLabel := "TASK · FOCUSED", "AGENT", "DIRECTORY"
	if m.composerAgentFocused {
		taskLabel, agentLabel = "TASK", "AGENT · FOCUSED"
	}
	if m.dispatchDirFocused {
		taskLabel, agentLabel, dirLabel = "TASK", "AGENT", "DIRECTORY · FOCUSED"
	}

	lines := []string{
		paneLabel.Render(agentLabel) + "  " + agentStyle(ag).Render(agent),
		m.agentSelector(contentW),
		sDim.Render(format.Truncate("Use @claude, @codex, or @copilot at the start of the task to override it.", contentW)),
		"",
		paneLabel.Render(taskLabel),
		m.filter.View(),
		"",
		paneLabel.Render(dirLabel),
		m.dispatchDir.View(),
	}
	if m.dispatchDirFocused {
		choices := m.dispatchDirectories()
		room := max(0, min(5, h-len(lines)-3))
		if room > 0 && len(choices) > 0 {
			lines = append(lines, sDim.Render("recent project directories"))
			for i, dir := range choices[:min(room, len(choices))] {
				marker, style := "  ", sMid
				if i == m.dispatchDirCursor {
					marker, style = "▸ ", sNameOn
				}
				lines = append(lines, marker+style.Render(format.Truncate(dir, max(8, contentW-2))))
			}
		}
	}
	lines = append(lines, "")
	if m.status != "" && (m.statusSticky || time.Now().Before(m.statusUntil)) {
		lines = append(lines, sErr.Render("▸ "+format.Truncate(m.status, contentW)))
	} else {
		lines = append(lines, sDim.Render(format.Truncate("Relative paths use Orbit's working directory; ~ is supported.", contentW)))
	}
	if len(lines) > h {
		lines = lines[:h]
	}
	return lines
}

func (m *Model) agentSelector(w int) string {
	var choices []string
	for _, ag := range session.AllAgents {
		name := ag.String()
		if ag == m.dispatchTo {
			name = "[" + name + "]"
		}
		choices = append(choices, agentStyle(ag).Render(name))
	}
	return format.Truncate(strings.Join(choices, "  "), w)
}

func (m *Model) newSessionLines(w, h int) []string {
	contentW := max(12, w)
	m.dispatchDir.SetWidth(contentW)
	agentLabel, dirLabel := "AGENT · FOCUSED", "DIRECTORY"
	if m.dispatchDirFocused {
		agentLabel, dirLabel = "AGENT", "DIRECTORY · FOCUSED"
	}
	lines := []string{
		paneLabel.Render(agentLabel),
		m.agentSelector(contentW),
		sDim.Render(format.Truncate("Use ←/→ to choose the interactive agent.", contentW)),
		"",
		paneLabel.Render(dirLabel),
		m.dispatchDir.View(),
	}
	if m.dispatchDirFocused {
		choices := m.dispatchDirectories()
		room := max(0, min(5, h-len(lines)-3))
		if room > 0 && len(choices) > 0 {
			lines = append(lines, sDim.Render("recent project directories"))
			for i, dir := range choices[:min(room, len(choices))] {
				marker, style := "  ", sMid
				if i == m.dispatchDirCursor {
					marker, style = "▸ ", sNameOn
				}
				lines = append(lines, marker+style.Render(format.Truncate(dir, max(8, contentW-2))))
			}
		}
	}
	lines = append(lines, "")
	if m.status != "" && (m.statusSticky || time.Now().Before(m.statusUntil)) {
		lines = append(lines, sErr.Render("▸ "+format.Truncate(m.status, contentW)))
	} else {
		lines = append(lines, sDim.Render("Enter starts the agent inside Orbit."))
	}
	if len(lines) > h {
		lines = lines[:h]
	}
	return lines
}

// View wraps the rendered frame in the declarative surface Bubble Tea v2 wants:
// alt screen, window title, and a terminal-level progress indicator. Ghostty
// paints that indicator on the tab itself, so a session wanting attention is
// visible even when orbit's tab isn't the one you're looking at.
func (m *Model) View() tea.View {
	content := m.render()
	if m.noColor {
		content = ansi.Strip(content)
	}
	v := tea.NewView(content)
	v.AltScreen = true
	v.WindowTitle = "orbit"
	// Keep wheel events as mouse messages throughout the dashboard. Turning
	// mouse mode off on Tab makes many terminals translate the wheel to arrow
	// keys in the alternate screen, which unexpectedly moves the session list.
	v.MouseMode = tea.MouseModeCellMotion
	if m.terminalFocused && m.embedded != "" && m.preparing == nil {
		v.WindowTitle = "orbit · " + m.embeddedName
	}

	var needs, working int
	for _, s := range m.all {
		switch s.State {
		case session.NeedsApproval:
			needs++
		case session.Working:
			working++
		}
	}
	switch {
	case len(m.pending) > 0:
		v.ProgressBar = &tea.ProgressBar{State: tea.ProgressBarIndeterminate}
	case needs > 0:
		v.ProgressBar = &tea.ProgressBar{State: tea.ProgressBarWarning, Value: 100}
	case working > 0:
		v.ProgressBar = &tea.ProgressBar{State: tea.ProgressBarIndeterminate}
	}
	return v
}

func (m *Model) renderEmbedded() string {
	w, h := m.terminalSize()
	mode := "FOCUSED"
	if m.stream != nil && m.stream.Scrolled() {
		mode = "SCROLLBACK · " + format.Itoa(m.stream.ScrollOffset()) + " LINES UP"
	}
	title := "  ▶ TERMINAL " + mode + " · " + m.embeddedName
	if m.embeddedCwd != "" {
		title += " · " + m.embeddedCwd
	}
	title = lipgloss.NewStyle().Foreground(cInk).Background(cGreen).Bold(true).
		Width(w).Render(format.Truncate(title, w))

	screen := strings.Join(m.terminalLines(w, h), "\n")
	body := lipgloss.NewStyle().Width(w).Height(h).MaxWidth(w).MaxHeight(h).Render(screen)
	helpText := "  INPUT → TERMINAL   Tab/Ctrl+g sessions   Ctrl+f split   Ctrl+e width"
	if m.stream != nil && m.stream.Scrolled() {
		helpText = "  SCROLLBACK · wheel to browse · any key returns to live · Tab sessions"
	}
	help := lipgloss.NewStyle().Foreground(cMid).Width(w).Render(helpText)
	return title + "\n" + body + "\n" + help
}

func (m *Model) terminalLines(w, h int) []string {
	screen := "connecting to tmux…"
	if m.stream != nil && m.stream.Session() == m.embedded {
		screen = m.stream.Render()
	} else if m.previewName == m.embedded && m.preview != "" {
		screen = m.preview
	}
	return clippedTerminalLines(screen, w, h)
}

// selectedTerminalPreview renders a live row through the terminal emulator,
// rather than feeding its cursor-positioned screen through transcript cleanup.
// A preview for another session is never reused during an asynchronous switch.
func (m *Model) selectedTerminalPreview(s *session.Session, w, h int) []string {
	if s == nil || s.Tmux == nil {
		return nil
	}
	name := s.Tmux.Name
	if m.stream != nil && m.stream.Session() == name {
		return clippedTerminalLines(m.stream.Render(), w, h)
	}
	if m.previewName == name && m.preview != "" {
		return clippedTerminalLines(m.preview, w, h)
	}
	return nil
}

func clippedTerminalLines(screen string, w, h int) []string {
	lines := strings.Split(screen, "\n")
	if len(lines) > h {
		lines = lines[len(lines)-h:]
	}
	for i := range lines {
		lines[i] = ansi.Truncate(strings.TrimSuffix(lines[i], "\r"), w, "")
	}
	return lines
}

// mutedTerminalLines removes the application's own colours before applying a
// single subdued foreground. This makes input ownership obvious and avoids an
// unfocused agent prompt looking just as actionable as the selected list row.
func mutedTerminalLines(lines []string) []string {
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = sDim.Render(ansi.Strip(line))
	}
	return out
}

func (m *Model) terminalMousePosition(x, y int) (int, int, bool) {
	if !m.terminalFocused || m.embedded == "" {
		return 0, 0, false
	}
	if m.terminalZoom {
		px, py := x, y-1 // the focused-terminal title owns row zero
		w, h := m.terminalSize()
		return px, py, px >= 0 && px < w && py >= 0 && py < h
	}
	listW, detailW, bodyH := m.dashboardPaneSizes()
	if detailW == 0 {
		return 0, 0, false
	}
	bodyTop := lipgloss.Height(m.header())
	// The right pane begins after the list and one-column gap. Its border and
	// horizontal padding consume two more columns; its title consumes one row.
	px, py := x-(listW+3), y-(bodyTop+1)
	w, h := m.terminalSize()
	return px, py, px >= 0 && px < w && py >= 0 && py < h && y < bodyTop+bodyH
}

// titledPane draws the pane label into the top border rule, the way most
// modern TUIs do it — lipgloss v1 has no border titles, so the top edge is
// drawn by hand and the box below it renders without one.
func titledPane(title string, lines []string, w, h int) string {
	border := lipgloss.NewStyle().Foreground(cBorder)
	label := " " + strings.ToUpper(format.Truncate(title, max(4, w-8))) + " "
	dashes := max(0, w-3-lipgloss.Width(label))
	top := border.Render("╭─") + paneLabel.Render(label) + border.Render(strings.Repeat("─", dashes)+"╮")

	box := paneStyle.BorderTop(false).Width(w).Height(h - 1).Render(strings.Join(lines, "\n"))
	return top + "\n" + box
}

func (m *Model) header() string {
	var working, needs int
	for _, s := range m.all {
		switch s.State {
		case session.Working:
			working++
		case session.NeedsApproval:
			needs++
		}
	}

	var metrics []string
	if needs > 0 {
		metrics = append(metrics, statusMetric("▲", "needs attention", needs, cAmber))
	}
	if working > 0 {
		metrics = append(metrics, statusMetric("●", "working", working, cBright))
	}

	if len(metrics) == 0 {
		metrics = append(metrics, sDim.Render("no active work"))
	}
	if m.h <= 24 {
		left := " " + sTitle.Render("orbit")
		right := tagline.Render(format.Itoa(len(m.view))+" of "+format.Itoa(len(m.all))+" shown") +
			sDim.Render("  ·  ") + strings.Join(metrics, sDim.Render("  ·  "))
		if !m.dispatching && !m.newing && (m.filtering || m.filter.Value() != "") {
			right = m.filter.View()
		} else if notice, style := m.headerNotice(); notice != "" {
			right = style.Render("◆ " + notice)
		}
		available := max(1, m.w-lipgloss.Width(left)-1)
		right = ansi.Truncate(right, available, "")
		gap := max(1, m.w-lipgloss.Width(left)-lipgloss.Width(right))
		return left + strings.Repeat(" ", gap) + right
	}

	// Spell the mapping out: the two-letter tags in the list are only obvious
	// once you've been told what they stand for.
	perAgent := map[session.Agent]int{}
	for _, s := range m.all {
		perAgent[s.Agent]++
	}
	var legend []string
	for _, a := range session.AllAgents {
		if perAgent[a] == 0 {
			continue
		}
		mark := agentStyle(a).Bold(true).Render(a.Tag())
		if m.icons == IconLogo {
			mark = LogoCells(a, "")
		}
		legend = append(legend, mark+" "+tagline.Render(a.String()+" "+format.Itoa(perAgent[a])))
	}

	art := banner(m.w, m.h)
	stats := []string{
		strings.Join(metrics, sDim.Render("  ·  ")),
		tagline.Render(format.Itoa(len(m.view))+" of "+format.Itoa(len(m.all))+" shown") +
			tagline.Render("  ·  ") + strings.Join(legend, tagline.Render(" · ")),
	}
	if !m.dispatching && !m.newing && (m.filtering || m.filter.Value() != "") {
		stats = append(stats, m.filter.View())
	}

	left := lipgloss.NewStyle().PaddingLeft(1).Render(strings.Join(art, "\n"))
	rightW := max(1, m.w-lipgloss.Width(left))
	rightH := max(len(art), len(stats))
	rightLines := make([]string, rightH)

	// Metrics sit on the logo's baseline. A transient notice owns the first
	// row at the far right without adding a row or moving the dashboard.
	metricTop := max(0, len(art)-len(stats)-1)
	if notice, style := m.headerNotice(); notice != "" {
		if metricTop == 0 {
			metricTop = 1
		}
		text := style.Render("◆ " + format.Truncate(notice, max(1, rightW-2)))
		rightLines[0] = lipgloss.NewStyle().Width(rightW).Align(lipgloss.Right).Render(text)
	}
	for i, stat := range stats {
		row := metricTop + i
		if row >= len(rightLines) {
			break
		}
		rightLines[row] = "   " + ansi.Truncate(stat, max(1, rightW-3), "")
	}
	right := strings.Join(rightLines, "\n")

	head := lipgloss.JoinHorizontal(lipgloss.Top, left, right)
	if lipgloss.Width(head) > m.w {
		head = lipgloss.NewStyle().MaxWidth(m.w).Render(head)
	}
	return head
}

// headerNotice returns short-lived feedback for the header's notification
// slot. Modes with their own persistent feedback keep using their local pane
// or footer and do not duplicate the same message here.
func (m *Model) headerNotice() (string, lipgloss.Style) {
	if m.status == "" || (!m.statusSticky && time.Now().After(m.statusUntil)) || m.updating ||
		m.preparing != nil || m.dispatching || m.newing || m.filtering {
		return "", lipgloss.Style{}
	}
	style := sHead
	if m.statusSticky {
		style = sErr
	}
	return m.status, style
}

// coverageBar is the global summary progress: filled by sessions that have a
// summary, advancing only as one completes.
func (m *Model) coverageBar() string {
	done, total, inflight := m.summaryCoverage()
	if total == 0 || !m.cfg.Summary.Enabled {
		return ""
	}
	m.prog.SetWidth(12)
	label := format.Itoa(done) + "/" + format.Itoa(total) + " summarised"
	if inflight > 0 {
		label += sDim.Render("  ") + sTok.Render(m.activityMark()+" "+format.Itoa(inflight)+" queued")
	}
	return m.prog.ViewAs(float64(done)/float64(total)) + "  " + tagline.Render(label)
}

func (m *Model) list(w, h int) []string {
	showGroups := m.group != groupNone && h >= 3
	if m.cursor < m.top {
		m.top = m.cursor
	}

	if len(m.view) == 0 {
		return m.emptyList(w)
	}
	if m.top >= len(m.view) {
		m.top = len(m.view) - 1
	}
	// Headings consume a real line. Move the viewport by session until the
	// selected row and every heading before it fit, rather than estimating the
	// pane as h/2 rows and letting group labels overflow the bottom border.
	for m.top < m.cursor && m.listSpanHeight(m.top, m.cursor, showGroups) > h {
		m.top++
	}

	var out []string
	counts := make(map[string]int)
	if showGroups {
		for _, s := range m.view {
			counts[groupKey(s, m.group)]++
		}
	}
	lastGroup := ""
	haveGroup := false
	i := m.top
	for ; i < len(m.view); i++ {
		s := m.view[i]
		key := groupKey(s, m.group)
		needsHead := showGroups && (!haveGroup || key != lastGroup)
		need := 2
		if needsHead {
			need++
		}
		if len(out)+need > h {
			break
		}
		if needsHead {
			lastGroup, haveGroup = key, true
			label := m.groupLabel(s) + " · " + format.Itoa(counts[key])
			out = append(out, sGroup.Render("▸ "+format.Truncate(label, w-2)))
		}
		out = append(out, m.row(s, i == m.cursor, w)...)
	}
	// A scroll hint beats silently truncating the list.
	if more := len(m.view) - i; more > 0 && len(out) < h {
		out = append(out, sDim.Render("  + "+format.Itoa(more)+" more"))
	}
	return out
}

// emptyList explains why the list is empty and offers an action that can
// actually change that state. A single "show everything" message is wrong for
// a first run, a live filter, and a transcript search in three different ways.
func (m *Model) emptyList(w int) []string {
	title, action := "No sessions to show", "? shortcuts"
	switch {
	case m.scanning && len(m.all) == 0:
		title, action = m.activityMark()+" Scanning sessions", "Found sessions appear here"
	case m.filtering && m.searching:
		title, action = "Search inside transcripts", "enter search  ·  esc cancel"
	case m.query != "":
		title = "No transcript matches"
		action = "esc clear search"
	case !m.searching && strings.TrimSpace(m.filter.Value()) != "":
		title = "No title or path matches"
		action = "esc clear filter"
	case len(m.all) == 0:
		title, action = "No agent sessions found", "d dispatch a task  ·  ? shortcuts"
	case !m.showAll:
		title, action = "No recent titled sessions", "a show all sessions"
	}
	return []string{
		"",
		sName.Render("  " + ansi.Truncate(title, max(1, w-2), "")),
		sDim.Render("  " + format.Truncate(action, max(1, w-2))),
	}
}

func (m *Model) listSpanHeight(start, end int, showGroups bool) int {
	height := 0
	lastGroup := ""
	haveGroup := false
	for i := start; i <= end && i < len(m.view); i++ {
		if showGroups {
			if key := groupKey(m.view[i], m.group); !haveGroup || key != lastGroup {
				lastGroup, haveGroup = key, true
				height++
			}
		}
		height += 2
	}
	return height
}

func (m *Model) groupLabel(s *session.Session) string {
	if m.group == groupAgent {
		return s.Agent.String()
	}
	return s.ShortCwd()
}

// row renders one session as two lines. Selection is a filled background rather
// than a marker, so the eye lands on it without hunting for a caret.
func (m *Model) row(s *session.Session, sel bool, w int) []string {
	base := lipgloss.NewStyle()
	if sel {
		base = base.Background(cSel)
	}
	paint := func(st lipgloss.Style, text string) string {
		if sel {
			st = st.Background(cSel).Bold(true)
		}
		return st.Render(text)
	}

	bar := "  "
	if sel {
		bar = sBar.Background(cSel).Render("▌") + base.Render(" ")
	}

	when := format.RelTime(s.Modified)
	headW := w - 2 - len([]rune(when)) - 1
	cwd := format.Pad(format.Truncate(s.ShortCwd(), headW-5), headW-5)
	visualState := s.State
	preparing := m.preparing != nil && m.preparing.id == s.ID && m.preparing.agent == s.Agent
	icon := visualState.Icon()
	if visualState == session.Working || preparing {
		icon = m.activityMark()
		visualState = session.Working
	}
	// In logo mode the agent cell is raw escape codes: the foreground colour
	// encodes the Kitty image id, so lipgloss must not restyle it.
	tag := paint(agentStyle(s.Agent), s.Agent.Tag())
	if m.icons == IconLogo {
		selBG := ""
		if sel {
			selBG = "\x1b[48;5;236m"
		}
		tag = LogoCells(s.Agent, selBG)
	}
	line1 := bar +
		paint(stateStyle(visualState), icon) + base.Render(" ") +
		tag + base.Render(" ") +
		paint(sMid, cwd) + base.Render(" ") + paint(sDim, when)

	label := s.State.Label()
	if preparing {
		label = "preparing"
	}
	nameW := w - 4 - len([]rune(label))
	if label != "" {
		nameW--
	}
	nameStyle := sName
	if sel {
		nameStyle = sNameOn
	}
	line2 := bar + base.Render("  ") + paint(nameStyle, format.Pad(format.Truncate(format.Clean(s.Name()), nameW), nameW))
	if label != "" {
		line2 += base.Render(" ") + paint(stateStyle(visualState), label)
	}
	return []string{line1, line2}
}

func (m *Model) detail(w, h int) []string {
	s := m.sel()
	if s == nil {
		return nil
	}
	var out []string
	add := func(ss ...string) { out = append(out, ss...) }

	add(sNameOn.Render(format.Truncate(format.Clean(s.Name()), w)))
	add(sDim.Render(format.Truncate(s.Cwd, w)))

	agent := agentStyle(s.Agent).Render(s.Agent.String())
	if m.icons == IconLogo {
		agent = LogoCells(s.Agent, "") + " " + agent
	}
	meta := []string{agent}
	if s.Branch != "" {
		meta = append(meta, sMid.Render(format.Truncate(s.Branch, 24)))
	}
	if s.Msgs > 0 {
		meta = append(meta, sMid.Render(format.Itoa(s.Msgs)+" msgs"))
	}
	if t := format.HumanTokens(s.Tokens); t != "" {
		meta = append(meta, sTok.Render(t+" tokens"))
	}
	if s.Tmux != nil && s.Tmux.Attached {
		meta = append(meta, sMid.Render("on screen"))
	}
	meta = append(meta, sMid.Render(format.RelTime(s.Modified)+" ago"))
	line := strings.Join(meta, sDim.Render(" · "))
	if lbl := s.State.Label(); lbl != "" {
		line += "  " + stateMarker(s.State.Icon(), lbl, stateColor(s.State))
	}
	add(line, "")

	// Above the summary, because a dispatch is what is happening now and a
	// summary is what happened before it.
	if d := s.Dispatch; d != nil {
		label, detail := dispatchLine(d)
		head := paneLabel.Render("▸ " + label)
		if d.Status == dispatch.Running {
			head += sDim.Render("  ") + sMid.Render(m.activityMark())
		}
		add(head)
		if detail != "" {
			style := sName
			switch d.Status {
			case dispatch.NeedsYou:
				style = sHit
			case dispatch.Failed:
				style = sErr
			}
			for _, l := range wrap(format.Clean(detail), w-2) {
				add("  " + style.Render(l))
			}
		}
		for _, l := range wrap(format.Clean("⇢ "+d.Prompt), w-2) {
			add("  " + sDim.Render(l))
		}
		if d.Status == dispatch.NeedsYou {
			add("  " + sMid.Render("⏎ takes it over where it stopped"))
		}
		add("")
	}

	if rec, ok := m.summaries[s.ID]; ok {
		head := paneLabel.Render("▸ summary")
		// Say plainly when the summary predates the latest turns, rather than
		// presenting stale text as current.
		if behind := rec.Behind(s); behind > 0 {
			head += sDim.Render("  ") + sHit.Render(format.Itoa(behind)+" msgs newer — s to update")
		}
		add(head)
		for _, l := range wrap(format.Clean(rec.Text), w-2) {
			add("  " + sName.Render(l))
		}
		add("")
	} else if elapsed, running := m.summaryElapsed(s.ID); running {
		add(paneLabel.Render("▸ summary"))
		add("  " + sDim.Render(m.activityMark()+" "+s.Agent.String()+" · "+
			format.Itoa(int(elapsed.Seconds()))+"s elapsed"))
		add("")
	} else if m.cfg.Summary.Enabled {
		add(sDim.Render("press s to summarise this session"), "")
	}

	if hit, ok := m.matches[s.ID]; ok && hit.Snippet != "" {
		add(paneLabel.Render("▸ match") + sDim.Render("  "+format.Itoa(hit.Hits)+" hits"))
		for _, l := range wrap(hit.Snippet, w-2) {
			add("  " + sHit.Render(l))
		}
		add("")
	}

	if s.Last != "" {
		add(paneLabel.Render("▸ last prompt"))
		for _, l := range wrap(format.Clean(s.Last), w-2) {
			add("  " + sName.Render(l))
		}
		add("")
	}

	if s.Tmux != nil && m.previewName == s.Tmux.Name && m.preview != "" {
		add(paneLabel.Render("▸ live output"))
		lines := strings.Split(m.preview, "\n")
		for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
			lines = lines[:len(lines)-1] // trailing blanks are just unused pane
		}
		if room := h - len(out); room > 0 && len(lines) > room {
			lines = lines[len(lines)-room:]
		}
		for _, l := range lines {
			add("  " + sMid.Render(format.Truncate(format.Clean(l), w-2)))
		}
	} else if s.Tmux == nil {
		add(paneLabel.Render("▸ ⏎ resumes with"))
		add("  " + sMid.Render(format.Truncate(s.Agent.ResumeCmd(s.ID), w-2)))
		add("")
		add(sDim.Render(format.Truncate("transcript: "+s.Path, w)))
	}
	return out
}

func stateColor(s session.State) color.Color {
	switch s {
	case session.Working:
		return cBright
	case session.NeedsApproval:
		return cAmber
	case session.YourTurn:
		return cCyan
	}
	return cGreen
}

func (m *Model) footer() string {
	// An update outlives the usual status timeout: it is the one thing that
	// ends with orbit restarting itself, so it stays on screen throughout
	// rather than expiring mid-download and leaving that unexplained.
	if m.updating {
		return " " + sHead.Render(m.activityMark()+" "+m.status)
	}
	if m.killConfirm != nil {
		return m.footerLine([][2]string{{"⏎ / x", "kill session"}, {"esc", "cancel"}})
	}
	if m.diagnosticsOpen {
		return m.footerLine([][2]string{{"r", "refresh"}, {"c", "clear last error"}, {"D / esc", "close"}})
	}
	if m.preparing != nil {
		p := m.preparing
		elapsed := int(time.Since(p.started).Seconds())
		return " " + sHead.Render(m.activityMark()+" preparing "+p.agent.String()+" live pane") +
			sDim.Render("  ·  "+format.Itoa(elapsed)+"s  ·  it will open automatically")
	}
	if m.dispatching {
		return m.footerLine([][2]string{
			{"tab", "switch field"}, {"↑↓", "directory"}, {"⏎", "accept"}, {"esc", "cancel"},
		})
	}
	if m.newing {
		return m.footerLine([][2]string{
			{"←→", "agent"}, {"tab", "switch field"}, {"↑↓", "directory"}, {"⏎", "start"}, {"esc", "cancel"},
		})
	}
	if m.showHelp {
		return m.footerLine([][2]string{{"?", "close shortcuts"}, {"esc", "close"}})
	}
	if m.terminalFocused && m.embedded != "" {
		mode := "widen"
		if m.terminalWide {
			mode = "normal width"
		}
		zoom := "full screen"
		if m.terminalZoom {
			zoom = "split view"
		}
		keys := [][2]string{{"tab", "sessions"}, {"ctrl+f", zoom}, {"ctrl+e", mode}}
		return m.footerLine(keys)
	}
	keys := [][2]string{
		{"⏎", "attach"}, {"tab", "drive"}, {"?", "shortcuts"}, {"/", "filter"},
		{"n", "new"}, {"d", "dispatch"},
	}
	return m.footerLine(keys)
}

func (m *Model) diagnosticLines(w, h int) []string {
	value := func(label, text string) string {
		return paneLabel.Render(label) + "  " + format.Truncate(text, max(1, w-len(label)-2))
	}
	scan := "not completed yet"
	if !m.lastScan.IsZero() {
		scan = format.Itoa(m.lastScanCount) + " sessions · " + format.RelTime(m.lastScan)
	}
	if m.scanning {
		scan += " · scanning for " + format.Itoa(int(time.Since(m.scanStart).Seconds())) + "s"
	}
	preview := "idle"
	if m.streamOpening {
		preview = "connecting"
	} else if m.stream != nil {
		preview = "streaming " + m.stream.Session()
	} else if m.previewName != "" {
		preview = "polling " + m.previewName
	}
	tmuxStatus := m.diagnostics.tmux
	if tmuxStatus == "" {
		tmuxStatus = "checking…"
	}
	agents := strings.Join(m.diagnostics.agents, " · ")
	if agents == "" {
		agents = "checking…"
	}
	lastErr := "none recorded"
	if m.lastError != "" {
		lastErr = m.lastError
		if !m.lastErrorAt.IsZero() {
			lastErr += " · " + format.RelTime(m.lastErrorAt)
		}
	}
	dump := m.dumpPath
	if dump == "" {
		dump = "not configured"
	}
	lines := []string{
		value("ORBIT", m.version),
		value("TMUX", tmuxStatus),
		value("AGENTS", agents),
		"",
		value("SCAN", scan),
		value("PREVIEW", preview),
		value("STACK DUMP", dump),
		"",
		value("LAST ERROR", lastErr),
		"",
		sDim.Render(format.Truncate("Errors remain visible until Esc or c clears them.", w)),
	}
	if len(lines) > h {
		lines = lines[:h]
	}
	return lines
}

func (m *Model) footerLine(keys [][2]string) string {
	right := m.coverageBar()
	available := m.w
	if right != "" {
		available -= lipgloss.Width(right) + 2
	}
	available = max(1, available)

	var ps []string
	for _, k := range keys {
		ps = append(ps, keyCap.Render(k[0])+" "+keyLabel.Render(k[1]))
	}
	left := " " + strings.Join(ps, sDim.Render("  "))
	for len(ps) > 1 && lipgloss.Width(left) > available {
		ps = ps[:len(ps)-1]
		left = " " + strings.Join(ps, sDim.Render("  ")) + sDim.Render(" …")
	}
	if lipgloss.Width(left) > available {
		left = ansi.Truncate(left, available, "")
	}
	if right == "" {
		return left
	}
	if lipgloss.Width(right) >= m.w {
		return lipgloss.NewStyle().Width(m.w).Align(lipgloss.Right).
			Render(ansi.Truncate(right, m.w, ""))
	}
	gap := max(1, m.w-lipgloss.Width(left)-lipgloss.Width(right))
	line := left + strings.Repeat(" ", gap) + right
	if lipgloss.Width(line) > m.w {
		for len(ps) > 1 && lipgloss.Width(line) > m.w {
			ps = ps[:len(ps)-1]
			left = " " + strings.Join(ps, sDim.Render("  ")) + sDim.Render(" …")
			gap = max(1, m.w-lipgloss.Width(left)-lipgloss.Width(right))
			line = left + strings.Repeat(" ", gap) + right
		}
	}
	return line
}

func (m *Model) shortcutHelp(w, h int) []string {
	type binding struct{ key, label string }
	type pair [2]binding
	entry := func(b binding) string {
		return keyCap.Render(b.key) + " " + keyLabel.Render(b.label)
	}
	row := func(left, right binding) []string {
		if w < 64 {
			return []string{entry(left), entry(right)}
		}
		column := max(1, w/2)
		return []string{lipgloss.NewStyle().Width(column).MaxWidth(column).Render(entry(left)) + entry(right)}
	}
	var lines []string
	section := func(title string, pairs ...pair) {
		lines = append(lines, paneLabel.Render(title))
		for _, p := range pairs {
			lines = append(lines, row(p[0], p[1])...)
		}
	}
	section("NAVIGATE",
		pair{binding{"↑ / k", "previous session"}, binding{"↓ / j", "next session"}},
		pair{binding{"g", "first session"}, binding{"G", "last session"}},
		pair{binding{"[", "previous attention"}, binding{"]", "next attention"}},
	)
	section("SESSIONS",
		pair{binding{"⏎", "attach"}, binding{"tab", "focus live pane"}},
		pair{binding{"i", "attach here"}, binding{"t", "open in new tab"}},
		pair{binding{"w", "open in new window"}, binding{"n", "new session"}},
		pair{binding{"d", "dispatch task"}, binding{"x", "confirm kill session"}},
	)
	section("FIND & ORGANIZE",
		pair{binding{"/", "filter list"}, binding{"f", "search transcripts"}},
		pair{binding{"o / p", "sort / group"}, binding{"a", "all / recent"}},
		pair{binding{"s / S", "summary / visible"}, binding{"r", "refresh"}},
	)
	section("LIVE PANE & APP",
		pair{binding{"tab / ctrl+g", "sessions"}, binding{"ctrl+f", "full screen"}},
		pair{binding{"ctrl+e", "toggle width"}, binding{"D", "diagnostics"}},
		pair{binding{"esc", "dismiss error"}, binding{"q", "quit"}},
	)
	if len(lines) > h {
		lines = lines[:max(0, h)]
	}
	return lines
}

func wrap(s string, w int) []string {
	if w < 8 {
		return []string{format.Truncate(s, w)}
	}
	var out []string
	line := ""
	for _, word := range strings.Fields(s) {
		switch {
		case line == "":
			line = word
		case len([]rune(line))+1+len([]rune(word)) <= w:
			line += " " + word
		default:
			out = append(out, line)
			line = word
		}
		if len(out) == 4 {
			return append(out, format.Truncate(line, w))
		}
	}
	if line != "" {
		out = append(out, format.Truncate(line, w))
	}
	return out
}
