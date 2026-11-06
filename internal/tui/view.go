package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/fernandotonon/QtMesh-Agents/internal/ipc"
)

var (
	titleStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	borderStyle  = lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color("240"))
	activeBorder = lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color("39"))
	dimStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	okStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	warnStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	sectionStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("251"))
)

func (m Model) View() string {
	if !m.ready {
		return "loading..."
	}

	conn := warnStyle.Render("○ Disconnected")
	if m.connected {
		conn = okStyle.Render("● Connected")
	}
	header := lipgloss.JoinHorizontal(lipgloss.Top,
		titleStyle.Render("QtMesh Agents"),
		strings.Repeat(" ", max(1, m.width-len("QtMesh Agents")-len("● Connected")-4)),
		conn,
	)

	sideW := 30
	if m.chatWide {
		sideW = 0
	} else if m.width < 70 {
		sideW = max(18, m.width/2)
	}
	mainW := m.width - sideW
	if sideW > 0 {
		mainW--
	}
	if mainW < 16 {
		mainW = 16
	}
	// header(1) + hint(1) + prompt(1) + optional worker banner / err / flash
	footerLines := m.footerLineCount()
	bodyH := m.height - 1 - footerLines
	if bodyH < 4 {
		bodyH = 4
	}

	var body string
	if m.chatWide {
		body = m.renderMain(mainW, bodyH)
	} else {
		side := m.renderSidebar(sideW, bodyH)
		main := m.renderMain(mainW, bodyH)
		body = lipgloss.JoinHorizontal(lipgloss.Top, side, main)
	}

	footer := m.renderFooter()

	out := lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
	// Clip by newline rows only (do not re-wrap — that blows up bordered panes).
	return clipScreenLines(out, m.height)
}

func (m Model) footerLineCount() int {
	n := 2 // hint + prompt
	if m.waitingWorkerReply() {
		n++ // worker banner
	}
	if m.errMsg != "" {
		n++
	}
	if m.statusFlash != "" {
		n++
	}
	return n
}

func (m Model) renderFooter() string {
	var b strings.Builder
	if m.mode == modeAttentionDetail && m.focus == focusOptions {
		b.WriteString(okStyle.Render("OPTIONS") + " " + dimStyle.Render("j/k · enter choose · t custom · Tab/← tree · esc back"))
		b.WriteByte('\n')
		b.WriteString(dimStyle.Render("> ") + dimStyle.Render("(pick an option, or Tab to navigate the left tree)"))
	} else if m.mode == modeAttentionDetail && m.focus == focusTree {
		b.WriteString(okStyle.Render("TREE") + " " + dimStyle.Render("j/k navigate · Tab/→ options · enter open · esc leave"))
		b.WriteByte('\n')
		b.WriteString(dimStyle.Render("> ") + dimStyle.Render("(left tree focused)"))
	} else if m.waitingWorkerReply() {
		b.WriteString(warnStyle.Render("⚠ Worker is waiting — type your reply below, Enter continues the task"))
		b.WriteByte('\n')
		b.WriteString(warnStyle.Render("WORKER › ") + m.input.View())
	} else if m.talkingToLeader() {
		b.WriteString(okStyle.Render("Talking to project leader — Enter sends a lead consult (Esc/o = orch)"))
		b.WriteByte('\n')
		b.WriteString(okStyle.Render("LEADER › ") + m.input.View())
	} else {
		hint := dimStyle.Render("Tab · f chat-wide (copy) · y yank chat · Y last reply · j/k tree · o orch")
		if m.chatWide {
			hint = okStyle.Render("CHAT-WIDE") + " " + dimStyle.Render("mouse-drag copies chat only · f show tree · y yank · Y last")
		} else if m.focus == focusTree {
			hint = okStyle.Render("TREE") + " " + dimStyle.Render("j/k · enter · →/← · o orch · f chat-wide")
		} else if m.mode == modeTaskDetail {
			hint = dimStyle.Render("Task detail · type to orchestrator · o orch chat")
		}
		b.WriteString(hint)
		b.WriteByte('\n')
		b.WriteString("> " + m.input.View())
	}
	if m.statusFlash != "" {
		b.WriteByte('\n')
		b.WriteString(okStyle.Render(truncate(m.statusFlash, m.width-2)))
	}
	if m.errMsg != "" {
		b.WriteByte('\n')
		b.WriteString(warnStyle.Render(truncate(m.errMsg, m.width-2)))
	}
	return b.String()
}

func (m Model) renderSidebar(w, h int) string {
	// Border eats 2 rows and 2 cols.
	innerH := h - 2
	if innerH < 1 {
		innerH = 1
	}
	innerW := w - 2
	if innerW < 8 {
		innerW = 8
	}

	offset := m.treeOffset
	if offset < 0 {
		offset = 0
	}
	maxOff := len(m.tree) - innerH
	if maxOff < 0 {
		maxOff = 0
	}
	if offset > maxOff {
		offset = maxOff
	}

	end := offset + innerH
	if end > len(m.tree) {
		end = len(m.tree)
	}

	var lines []string
	if offset > 0 || end < len(m.tree) {
		scroll := ""
		if offset > 0 {
			scroll += "↑"
		}
		if end < len(m.tree) {
			scroll += "↓"
		}
		lines = append(lines, dimStyle.Render(truncate(
			fmt.Sprintf("TREE %s %d/%d", scroll, m.cursor+1, len(m.tree)), innerW)))
		// One row used for scroll header — shrink the node window.
		innerH--
		if innerH < 1 {
			innerH = 1
		}
		if m.cursor < offset {
			offset = m.cursor
		}
		if m.cursor >= offset+innerH {
			offset = m.cursor - innerH + 1
		}
		if offset < 0 {
			offset = 0
		}
		maxOff = len(m.tree) - innerH
		if maxOff < 0 {
			maxOff = 0
		}
		if offset > maxOff {
			offset = maxOff
		}
		end = offset + innerH
		if end > len(m.tree) {
			end = len(m.tree)
		}
	}

	for i := offset; i < end; i++ {
		lines = append(lines, m.tree[i].render(i == m.cursor && m.focus == focusTree, innerW))
	}
	// Pad / clip exactly to panel inner height (header + nodes already accounted).
	target := h - 2
	if target < 1 {
		target = 1
	}
	for len(lines) < target {
		lines = append(lines, "")
	}
	if len(lines) > target {
		lines = lines[:target]
	}

	content := strings.Join(lines, "\n")
	return renderPane(content, w, h, m.focus == focusTree, false)
}

func (m Model) renderMain(w, h int) string {
	innerH := h - 2
	if innerH < 1 {
		innerH = 1
	}
	innerW := w - 2
	if innerW < 10 {
		innerW = 10
	}

	var title, content string
	keepBottom := true
	trustViewport := false
	switch m.mode {
	case modeAgent:
		title = "AGENT"
		if m.attachedID != "" {
			title += "  " + dimStyle.Render(truncate(m.attachedID, innerW/2))
		}
		content = m.agentView.View()
		if strings.TrimSpace(content) == "" {
			content = dimStyle.Render("No agent output. Select a session and press enter.")
		}
		trustViewport = true
	case modeRunDetail:
		title = "RUN"
		content = m.renderRunDetail(innerW, innerH-1)
		keepBottom = false
	case modeTaskDetail:
		title = "TASK"
		content = m.renderTaskDetail(innerW, innerH-1)
		keepBottom = true // latest agent feedback
	case modeAttentionDetail:
		title = "NEEDS YOU"
		if m.focus == focusOptions {
			title += "  " + okStyle.Render("[options focused]")
		}
		content = m.renderAttentionDetail(innerW, innerH-1)
		keepBottom = false
	default:
		title = "ORCHESTRATOR"
		content = m.orchView.View()
		trustViewport = true
		keepBottom = false
	}

	inner := sectionStyle.Render(title) + "\n" + content
	active := m.focus == focusInput || m.focus == focusOptions
	if trustViewport {
		// Viewport already clipped/scrolled — pad by raw newlines only (no keepBottom rewrap).
		return renderViewportPane(inner, w, h, active)
	}
	inner = fitBlock(inner, innerW, innerH, keepBottom)
	return renderPane(inner, w, h, active, keepBottom)
}

// renderViewportPane borders content that a bubbles viewport already sized.
// It pads/clips by raw newlines without wrapBlock+keepBottom (which hid older chat).
func renderViewportPane(content string, outerW, outerH int, active bool) string {
	innerW := outerW - 2
	innerH := outerH - 2
	if innerW < 1 {
		innerW = 1
	}
	if innerH < 1 {
		innerH = 1
	}
	lines := strings.Split(content, "\n")
	if len(lines) > innerH {
		lines = lines[:innerH]
	}
	for len(lines) < innerH {
		lines = append(lines, "")
	}
	content = strings.Join(lines, "\n")
	style := borderStyle
	if active {
		style = activeBorder
	}
	style = style.Width(innerW).MaxWidth(innerW).Height(innerH).MaxHeight(innerH)
	out := style.Render(content)
	return clipScreenLines(out, outerH)
}

// renderPane draws a bordered box. lipgloss Width/Height are CONTENT sizes;
// borders add 2 cols/rows, so we size content to (w-2)×(h-2) for an outer w×h pane.
func renderPane(content string, outerW, outerH int, active, keepBottom bool) string {
	innerW := outerW - 2
	innerH := outerH - 2
	if innerW < 1 {
		innerW = 1
	}
	if innerH < 1 {
		innerH = 1
	}
	content = fitBlock(content, innerW, innerH, keepBottom)
	style := borderStyle
	if active {
		style = activeBorder
	}
	// Important: do NOT set Height to outerH — that makes content taller than
	// the slot, and lipgloss truncates from the bottom (hiding the latest chat).
	style = style.Width(innerW).MaxWidth(innerW).Height(innerH).MaxHeight(innerH)
	out := style.Render(content)
	return clipScreenLines(out, outerH)
}

func (m Model) renderAttentionDetail(w, maxH int) string {
	attn := m.selectedAttentionInfo()
	if attn == nil {
		return dimStyle.Render("Attention item gone — press esc.")
	}
	if maxH < 1 {
		maxH = 1
	}

	var head strings.Builder
	head.WriteString(warnStyle.Render("⚠ "+attn.Kind) + "\n")
	head.WriteString(wrapPlain(truncate(attn.Message, w*3), w) + "\n")
	if m.selectedTask != "" {
		agent := ""
		if t := m.selectedTaskInfo(); t != nil && t.AssignedAgent != "" {
			agent = " · " + t.AssignedAgent
		}
		head.WriteString(dimStyle.Render(truncate("task "+shortTaskID(m.selectedTask)+" · run "+shortRunID(m.selectedRun)+agent, w)) + "\n")
	}
	if m.attnQuestion != "" && m.attnQuestion != attn.Message {
		head.WriteString("\n" + sectionStyle.Render("Question") + "\n")
		head.WriteString(wrapPlain(m.attnQuestion, w) + "\n")
	}

	var opts strings.Builder
	opts.WriteString(sectionStyle.Render("Choose a reply") + "\n")
	if len(m.attnOptions) == 0 {
		opts.WriteString(dimStyle.Render("(no options)") + "\n")
	} else {
		for i, opt := range m.attnOptions {
			prefix := "  "
			line := fmt.Sprintf("%s. %s", opt.Key, opt.Label)
			if i == m.attnOptCur {
				prefix = "› "
				opts.WriteString(okStyle.Render(truncate(prefix+line, w)) + "\n")
			} else {
				opts.WriteString(truncate(prefix+line, w) + "\n")
			}
		}
	}
	opts.WriteString(dimStyle.Render("Enter sends that reply · Tab/← tree"))

	headN := countVisualLines(head.String(), w)
	optsN := countVisualLines(opts.String(), w)
	// Reserve room for options; give the rest to the agent prompt excerpt.
	excerptBudget := maxH - headN - optsN - 2
	if excerptBudget < 3 {
		excerptBudget = 3
	}
	if headN+optsN+excerptBudget+1 > maxH {
		excerptBudget = max(2, maxH-headN-optsN-1)
	}

	var mid strings.Builder
	mid.WriteString("\n" + sectionStyle.Render("Agent said") + "\n")
	excerpt := attentionAgentExcerpt(m.selectedTaskInfo(), attn.Message, excerptBudget, w)
	if excerpt == "" {
		mid.WriteString(dimStyle.Render("(no agent output captured yet)") + "\n")
	} else {
		mid.WriteString(excerpt)
		if !strings.HasSuffix(excerpt, "\n") {
			mid.WriteByte('\n')
		}
	}

	return fitBlock(head.String()+mid.String()+"\n"+opts.String(), w, maxH, false)
}

func (m Model) renderRunDetail(w, maxH int) string {
	var r *ipc.RunInfo
	for i := range m.runs {
		if m.runs[i].ID == m.selectedRun {
			r = &m.runs[i]
			break
		}
	}
	if r == nil {
		return dimStyle.Render("Run not found. Refreshing…")
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("ID:      %s\n", truncate(r.ID, w-10)))
	b.WriteString(fmt.Sprintf("Status:  %s %s\n", statusGlyph(r.Status), r.Status))
	b.WriteString(fmt.Sprintf("Project: %s\n", emptyDash(r.ProjectID)))
	b.WriteString(fmt.Sprintf("Goal:    %s\n", truncate(r.Objective, w-10)))
	b.WriteString("\n" + sectionStyle.Render("Tasks") + "\n")
	if len(r.Tasks) == 0 {
		b.WriteString(dimStyle.Render("  (none)") + "\n")
	}
	for _, t := range r.Tasks {
		agent := t.AssignedAgent
		if agent == "" {
			agent = "-"
		}
		line := fmt.Sprintf("  %s %-10s %-8s %s", statusGlyph(t.Status), shortTaskID(t.ID), agent, t.Status)
		b.WriteString(truncate(line, w-2) + "\n")
		if t.Error != "" {
			b.WriteString(warnStyle.Render("    ! "+truncate(t.Error, w-6)) + "\n")
		} else if preview := feedbackPreview(t.Output, w-6); preview != "" {
			b.WriteString(dimStyle.Render("    » "+preview) + "\n")
		}
	}
	b.WriteString("\n" + dimStyle.Render("Enter a task for details / full agent feedback."))
	return fitBlock(b.String(), w, max(1, maxH), false)
}

func (m Model) renderTaskDetail(w, maxH int) string {
	var task *ipc.TaskInfo
	var run *ipc.RunInfo
	for i := range m.runs {
		if m.runs[i].ID != m.selectedRun {
			continue
		}
		run = &m.runs[i]
		for j := range m.runs[i].Tasks {
			if m.runs[i].Tasks[j].ID == m.selectedTask {
				task = &m.runs[i].Tasks[j]
				break
			}
		}
		break
	}
	if task == nil {
		return dimStyle.Render("Task not found.")
	}
	if maxH < 1 {
		maxH = 1
	}

	// Compact header so most of the pane is agent feedback.
	var header strings.Builder
	header.WriteString(fmt.Sprintf("%s %s  ·  %s\n", statusGlyph(task.Status), task.Status, emptyDash(task.AssignedAgent)))
	header.WriteString(truncate(shortTaskID(task.ID)+": "+task.Description, w) + "\n")
	if run != nil {
		header.WriteString(dimStyle.Render(truncate("run "+shortRunID(run.ID), w)) + "\n")
	}
	if task.Error != "" {
		header.WriteString(warnStyle.Render(truncate("! "+task.Error, w)) + "\n")
	}
	if task.Status == "awaiting_input" || task.Status == "needs_attention" {
		header.WriteString(warnStyle.Render("⚠ Type your reply in WORKER › below, then Enter") + "\n")
	}
	header.WriteString(sectionStyle.Render("Agent feedback") + "\n")
	headerLines := countVisualLines(header.String(), w)
	budget := maxH - headerLines
	if budget < 1 {
		budget = 1
	}

	feedback := cleanAgentFeedback(task.Output)
	var body string
	if feedback == "" {
		if task.Status == "running" {
			body = dimStyle.Render("(waiting for first agent output…)")
		} else {
			body = dimStyle.Render("(no agent output captured)")
		}
	} else {
		body = tailLines(feedback, w, budget)
	}
	return fitBlock(header.String()+body, w, maxH, true)
}

// cleanAgentFeedback strips noisy vendor banners for display.
func cleanAgentFeedback(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	var keep []string
	for _, line := range strings.Split(s, "\n") {
		trim := strings.TrimSpace(line)
		lower := strings.ToLower(trim)
		if trim == "" {
			if len(keep) > 0 && keep[len(keep)-1] != "" {
				keep = append(keep, "")
			}
			continue
		}
		if strings.HasPrefix(lower, "openai codex") ||
			strings.HasPrefix(lower, "--------") ||
			strings.HasPrefix(lower, "workdir:") ||
			strings.HasPrefix(lower, "model:") ||
			strings.HasPrefix(lower, "provider:") ||
			strings.HasPrefix(lower, "approval:") ||
			strings.HasPrefix(lower, "sandbox:") ||
			strings.HasPrefix(lower, "reasoning ") ||
			strings.HasPrefix(lower, "session id:") ||
			strings.HasPrefix(lower, "reading additional input") {
			continue
		}
		keep = append(keep, line)
	}
	return strings.TrimSpace(strings.Join(keep, "\n"))
}

func feedbackPreview(s string, width int) string {
	s = cleanAgentFeedback(s)
	if s == "" {
		return ""
	}
	// Prefer the last non-empty line.
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line != "" {
			return truncate(line, width)
		}
	}
	return ""
}

func tailLines(s string, width, maxLines int) string {
	if maxLines < 1 {
		maxLines = 1
	}
	wrapped := wrapBlock(s, width)
	if len(wrapped) > maxLines {
		omitted := len(wrapped) - maxLines + 1
		if maxLines == 1 {
			wrapped = wrapped[len(wrapped)-1:]
		} else {
			wrapped = append([]string{dimStyle.Render(fmt.Sprintf("… %d earlier lines …", omitted))}, wrapped[len(wrapped)-(maxLines-1):]...)
		}
	}
	return strings.Join(wrapped, "\n")
}

// wrapBlock hard-wraps each logical line to width (ANSI codes ignored for length).
func wrapBlock(s string, width int) []string {
	if width < 1 {
		width = 1
	}
	var out []string
	for _, line := range strings.Split(s, "\n") {
		plain := line
		// Measure visible width roughly by stripping common lipgloss-unrelated length;
		// truncatePlain wraps by rune count which is good enough for our ASCII-heavy UI.
		if visibleLen(plain) <= width {
			out = append(out, line)
			continue
		}
		for _, part := range strings.Split(wrapPlain(stripForWrap(plain), width), "\n") {
			out = append(out, part)
		}
	}
	return out
}

func stripForWrap(s string) string {
	// Drop ANSI CSI sequences so wrap width matches the terminal.
	var b strings.Builder
	inEsc := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == 0x1b {
			inEsc = true
			continue
		}
		if inEsc {
			if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
				inEsc = false
			}
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

func visibleLen(s string) int {
	return len([]rune(stripForWrap(s)))
}

func countVisualLines(s string, width int) int {
	return len(wrapBlock(s, width))
}

// fitBlock wraps to width and pads/clips to exactly height rows.
func fitBlock(s string, width, height int, keepBottom bool) string {
	if height < 1 {
		height = 1
	}
	lines := wrapBlock(s, width)
	if len(lines) > height {
		if keepBottom {
			if height > 1 {
				omitted := len(lines) - (height - 1)
				lines = append([]string{dimStyle.Render(fmt.Sprintf("… %d lines above …", omitted))}, lines[len(lines)-(height-1):]...)
			} else {
				lines = lines[len(lines)-height:]
			}
		} else {
			lines = lines[:height]
		}
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

// clipScreenLines keeps the last `height` newline rows (prompt/footer) without
// re-wrapping, which would shatter bordered panes into dozens of extra lines.
func clipScreenLines(s string, height int) string {
	if height < 1 {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) > height {
		lines = lines[len(lines)-height:]
	}
	return strings.Join(lines, "\n")
}

func emptyDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func wrap(s string, width int) string {
	if width < 10 {
		width = 10
	}
	s = strings.TrimSpace(s)
	if len(s) <= width {
		return s
	}
	var lines []string
	for len(s) > width {
		lines = append(lines, s[:width])
		s = s[width:]
	}
	if s != "" {
		lines = append(lines, s)
	}
	return strings.Join(lines, "\n         ")
}

// wrapPlain wraps text to width on word boundaries when possible (for chat).
func wrapPlain(s string, width int) string {
	if width < 10 {
		width = 10
	}
	if s == "" {
		return s
	}
	var out []string
	for _, para := range strings.Split(s, "\n") {
		if para == "" {
			out = append(out, "")
			continue
		}
		words := strings.Fields(para)
		if len(words) == 0 {
			out = append(out, "")
			continue
		}
		// Preserve leading indent for nested bullets.
		lead := ""
		for _, r := range para {
			if r == ' ' || r == '\t' {
				lead += string(r)
				continue
			}
			break
		}
		if len(lead) > width/3 {
			lead = lead[:width/3]
		}
		line := lead
		for _, w := range words {
			if line == lead {
				line += w
				continue
			}
			if len(line)+1+len(w) <= width {
				line += " " + w
				continue
			}
			out = append(out, line)
			line = lead + w
			// Hard-break overlong tokens.
			for len(line) > width {
				out = append(out, line[:width])
				line = lead + line[width:]
			}
		}
		if line != "" {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}
