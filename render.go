package main

import (
	"image/color"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

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

// pill is a filled badge — the counts need to read at a glance from across the
// room, which plain coloured text doesn't do.
func pill(icon, text string, bg color.Color) string {
	return lipgloss.NewStyle().Foreground(cInk).Background(bg).Bold(true).
		Padding(0, 1).Render(icon + " " + text)
}

func banner(width, height int) []string {
	var art []string
	switch {
	case height >= 32 && width >= 74:
		art = bannerFull
	case height >= 22 && width >= 46:
		art = bannerSmall
	default:
		return []string{sTitle.Render("orbit")}
	}
	out := make([]string, len(art))
	// Index the fade against the full ramp so the small banner still fades.
	for i, line := range art {
		c := bannerFade[i*len(bannerFade)/len(art)]
		out[i] = lipgloss.NewStyle().Foreground(c).Render(line)
	}
	return out
}

func (m *model) render() string {
	if m.w == 0 {
		return "starting orbit…"
	}

	head := m.header()
	foot := m.footer()
	bodyH := m.h - lipgloss.Height(head) - lipgloss.Height(foot)
	if bodyH < 5 {
		bodyH = 5
	}

	// In lipgloss v2 Width() is the *total* rendered width, border included
	// (v1 counted content+padding only). A box of width W therefore takes
	// Width(W) and holds W-4 of content: 2 border columns, 2 of padding.
	const chrome = 4
	listBoxW := min(66, max(38, m.w*46/100))
	detBoxW := m.w - listBoxW - 1 // one column of air between the panes
	if detBoxW < 30 {
		listBoxW, detBoxW = m.w, 0
	}

	scope := "recent"
	if m.showAll {
		scope = "all"
	}
	label := "sessions · " + scope + " · by " + m.sort.String()
	if m.query != "" {
		label = "search · " + m.query
	}
	body := titledPane(label, m.list(listBoxW-chrome, bodyH-3), listBoxW, bodyH)

	if detBoxW > 0 {
		label := "detail"
		if s := m.sel(); s != nil && s.Tmux != nil {
			label = "live · " + s.ShortCwd()
		}
		right := titledPane(label, m.detail(detBoxW-chrome, bodyH-3), detBoxW, bodyH)
		gap := strings.TrimSuffix(strings.Repeat(" \n", lipgloss.Height(body)), "\n")
		body = lipgloss.JoinHorizontal(lipgloss.Top, body, gap, right)
	}

	return head + "\n" + body + "\n" + foot
}

// View wraps the rendered frame in the declarative surface Bubble Tea v2 wants:
// alt screen, window title, and a terminal-level progress indicator. Ghostty
// paints that indicator on the tab itself, so a session wanting attention is
// visible even when orbit's tab isn't the one you're looking at.
func (m *model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.WindowTitle = "orbit"

	var needs, working int
	for _, s := range m.all {
		switch s.State {
		case NeedsApproval, YourTurn:
			needs++
		case Working:
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

// titledPane draws the pane label into the top border rule, the way most
// modern TUIs do it — lipgloss v1 has no border titles, so the top edge is
// drawn by hand and the box below it renders without one.
func titledPane(title string, lines []string, w, h int) string {
	border := lipgloss.NewStyle().Foreground(cBorder)
	label := " " + strings.ToUpper(truncate(title, max(4, w-8))) + " "
	dashes := max(0, w-3-lipgloss.Width(label))
	top := border.Render("╭─") + paneLabel.Render(label) + border.Render(strings.Repeat("─", dashes)+"╮")

	box := paneStyle.BorderTop(false).Width(w).Height(h - 1).Render(strings.Join(lines, "\n"))
	return top + "\n" + box
}

func (m *model) header() string {
	var working, needs, turn, shell int
	for _, s := range m.all {
		switch s.State {
		case Working:
			working++
		case NeedsApproval:
			needs++
		case YourTurn:
			turn++
		case ShellOnly:
			shell++
		}
	}

	var pills []string
	if needs > 0 {
		pills = append(pills, pill("▲", itoa(needs)+" needs you", cAmber))
	}
	if turn > 0 {
		pills = append(pills, pill("◆", itoa(turn)+" your turn", cCyan))
	}
	if working > 0 {
		pills = append(pills, pill("●", itoa(working)+" working", cBright))
	}
	if shell > 0 {
		pills = append(pills, pill("○", itoa(shell)+" idle", cGreen))
	}

	if len(pills) == 0 {
		pills = append(pills, lipgloss.NewStyle().Foreground(cDim).
			Padding(0, 1).Render("nothing running"))
	}

	// Spell the mapping out: the two-letter tags in the list are only obvious
	// once you've been told what they stand for.
	perAgent := map[Agent]int{}
	for _, s := range m.all {
		perAgent[s.Agent]++
	}
	var legend []string
	for _, a := range AllAgents {
		if perAgent[a] == 0 {
			continue
		}
		mark := agentStyle(a).Bold(true).Render(a.Tag())
		if m.icons == IconLogo {
			mark = LogoCells(a, "")
		}
		legend = append(legend, mark+" "+tagline.Render(a.String()+" "+itoa(perAgent[a])))
	}

	art := banner(m.w, m.h)
	stats := []string{
		strings.Join(pills, " "),
		m.coverageBar(),
		tagline.Render(itoa(len(m.view))+" of "+itoa(len(m.all))+" shown") +
			tagline.Render("  ·  ") + strings.Join(legend, tagline.Render(" · ")),
	}
	if m.filtering || m.filter.Value() != "" {
		stats = append(stats, m.filter.View())
	}

	left := lipgloss.NewStyle().PaddingLeft(1).Render(strings.Join(art, "\n"))
	// Sit the stats on the baseline of the logo rather than floating mid-air.
	right := lipgloss.NewStyle().PaddingLeft(3).PaddingTop(max(0, len(art)-len(stats)-1)).
		Render(strings.Join(stats, "\n"))

	head := lipgloss.JoinHorizontal(lipgloss.Top, left, right)
	if lipgloss.Width(head) > m.w {
		head = lipgloss.NewStyle().MaxWidth(m.w).Render(head)
	}
	return head
}

// coverageBar is the global summary progress: filled by sessions that have a
// summary, advancing only as one completes.
func (m *model) coverageBar() string {
	done, total, inflight := m.summaryCoverage()
	if total == 0 || !m.cfg.Summary.Enabled {
		return ""
	}
	m.prog.SetWidth(28)
	label := itoa(done) + "/" + itoa(total) + " summarised"
	if inflight > 0 {
		label += sDim.Render("  ") + sTok.Render(m.spin.View()+" "+itoa(inflight)+" queued")
	}
	return m.prog.ViewAs(float64(done)/float64(total)) + "  " + tagline.Render(label)
}

func (m *model) list(w, h int) []string {
	rows := h / 2
	if rows < 1 {
		rows = 1
	}
	if m.cursor < m.top {
		m.top = m.cursor
	}
	if m.cursor >= m.top+rows {
		m.top = m.cursor - rows + 1
	}
	if m.top > max(0, len(m.view)-rows) {
		m.top = max(0, len(m.view)-rows)
	}

	if len(m.view) == 0 {
		return []string{"", sDim.Render("  nothing matches — press a to show everything")}
	}

	var out []string
	lastGroup := ""
	for i := m.top; i < len(m.view) && i < m.top+rows; i++ {
		s := m.view[i]
		if m.group {
			if g := s.ShortCwd(); g != lastGroup {
				lastGroup = g
				out = append(out, sGroup.Render("▸ "+truncate(g, w-2)))
			}
		}
		out = append(out, m.row(s, i == m.cursor, w)...)
	}
	// A scroll hint beats silently truncating the list.
	if more := len(m.view) - (m.top + rows); more > 0 && len(out) < h {
		out = append(out, sDim.Render("  + "+itoa(more)+" more"))
	}
	return out
}

// row renders one session as two lines. Selection is a filled background rather
// than a marker, so the eye lands on it without hunting for a caret.
func (m *model) row(s *Session, sel bool, w int) []string {
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

	when := relTime(s.Modified)
	headW := w - 2 - len([]rune(when)) - 1
	cwd := pad(truncate(s.ShortCwd(), headW-5), headW-5)
	icon := s.State.Icon()
	if s.State == Working {
		icon = m.spin.View() // a still dot reads as stalled; motion reads as busy
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
		paint(stateStyle(s.State), icon) + base.Render(" ") +
		tag + base.Render(" ") +
		paint(sMid, cwd) + base.Render(" ") + paint(sDim, when)

	label := s.State.Label()
	nameW := w - 4 - len([]rune(label))
	if label != "" {
		nameW--
	}
	nameStyle := sName
	if sel {
		nameStyle = sNameOn
	}
	line2 := bar + base.Render("  ") + paint(nameStyle, pad(truncate(clean(s.Name()), nameW), nameW))
	if label != "" {
		line2 += base.Render(" ") + paint(stateStyle(s.State), label)
	}
	return []string{line1, line2}
}

func (m *model) detail(w, h int) []string {
	s := m.sel()
	if s == nil {
		return nil
	}
	var out []string
	add := func(ss ...string) { out = append(out, ss...) }

	add(sNameOn.Render(truncate(clean(s.Name()), w)))
	add(sDim.Render(truncate(s.Cwd, w)))

	meta := []string{agentStyle(s.Agent).Render(s.Agent.String())}
	if s.Branch != "" {
		meta = append(meta, sMid.Render(truncate(s.Branch, 24)))
	}
	if s.Msgs > 0 {
		meta = append(meta, sMid.Render(itoa(s.Msgs)+" msgs"))
	}
	if t := humanTokens(s.Tokens); t != "" {
		meta = append(meta, sTok.Render(t+" tokens"))
	}
	meta = append(meta, sMid.Render(relTime(s.Modified)+" ago"))
	line := strings.Join(meta, sDim.Render(" · "))
	if lbl := s.State.Label(); lbl != "" {
		line += "  " + pill(s.State.Icon(), lbl, stateColor(s.State))
	}
	add(line, "")

	if rec, ok := m.summaries[s.ID]; ok {
		head := paneLabel.Render("▸ summary")
		// Say plainly when the summary predates the latest turns, rather than
		// presenting stale text as current.
		if behind := rec.Behind(s); behind > 0 {
			head += sDim.Render("  ") + sHit.Render(itoa(behind)+" msgs newer — s to update")
		}
		add(head)
		for _, l := range wrap(clean(rec.Summary), w-2) {
			add("  " + sName.Render(l))
		}
		add("")
	} else if elapsed, running := m.summaryElapsed(s.ID); running {
		add(paneLabel.Render("▸ summary"))
		add("  " + sDim.Render(m.spin.View()+" "+s.Agent.String()+" · "+
			itoa(int(elapsed.Seconds()))+"s elapsed"))
		add("")
	} else if m.cfg.Summary.Enabled {
		add(sDim.Render("press s to summarise this session"), "")
	}

	if hit, ok := m.matches[s.ID]; ok && hit.Snippet != "" {
		add(paneLabel.Render("▸ match") + sDim.Render("  "+itoa(hit.Hits)+" hits"))
		for _, l := range wrap(hit.Snippet, w-2) {
			add("  " + sHit.Render(l))
		}
		add("")
	}

	if s.Last != "" {
		add(paneLabel.Render("▸ last prompt"))
		for _, l := range wrap(clean(s.Last), w-2) {
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
			add("  " + sMid.Render(truncate(clean(l), w-2)))
		}
	} else if s.Tmux == nil {
		add(paneLabel.Render("▸ ⏎ resumes with"))
		add("  " + sMid.Render(truncate(s.Agent.ResumeCmd(s.ID), w-2)))
		add("")
		add(sDim.Render(truncate("transcript: "+s.Path, w)))
	}
	return out
}

func stateColor(s State) color.Color {
	switch s {
	case Working:
		return cBright
	case NeedsApproval:
		return cAmber
	case YourTurn:
		return cCyan
	}
	return cGreen
}

func (m *model) footer() string {
	if m.status != "" && time.Now().Before(m.statusUntil) {
		st := sHead
		if strings.Contains(m.status, "failed") || strings.Contains(m.status, "not installed") {
			st = sErr
		}
		return " " + st.Render("▸ "+m.status)
	}
	keys := [][2]string{
		{"⏎", "attach"}, {"i", "here"}, {"n", "new"}, {"s/S", "sum/all"},
		{"f", "search"}, {"/", "filter"}, {"o", "sort"}, {"p", "group"},
		{"x", "kill"}, {"a", "all"}, {"q", "quit"},
	}
	if canSpawnTab() {
		keys = append(keys[:2], append([][2]string{{"w", "window"}}, keys[2:]...)...)
	}
	var ps []string
	for _, k := range keys {
		ps = append(ps, keyCap.Render(k[0])+" "+keyLabel.Render(k[1]))
	}
	foot := " " + strings.Join(ps, sDim.Render("  "))
	if lipgloss.Width(foot) > m.w {
		// Drop trailing keys rather than wrapping into a second row.
		for len(ps) > 3 && lipgloss.Width(foot) > m.w {
			ps = ps[:len(ps)-1]
			foot = " " + strings.Join(ps, sDim.Render("  ")) + sDim.Render(" …")
		}
	}
	return foot
}

func wrap(s string, w int) []string {
	if w < 8 {
		return []string{truncate(s, w)}
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
			return append(out, truncate(line, w))
		}
	}
	if line != "" {
		out = append(out, truncate(line, w))
	}
	return out
}
