package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	borderStyle = lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color("240"))
	dimStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	okStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	warnStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
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

	sideW := 22
	mainW := m.width - sideW - 1
	if mainW < 20 {
		mainW = 20
	}
	bodyH := m.height - 5
	if bodyH < 6 {
		bodyH = 6
	}

	side := m.renderSidebar(sideW, bodyH)
	main := m.renderMain(mainW, bodyH)
	body := lipgloss.JoinHorizontal(lipgloss.Top, side, main)

	footer := "> " + m.input.View()
	if m.errMsg != "" {
		footer += "\n" + warnStyle.Render(m.errMsg)
	}

	return lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
}

func (m Model) renderSidebar(w, h int) string {
	var b strings.Builder
	b.WriteString(sectionStyle.Render("PROJECTS") + "\n")
	if len(m.projects) == 0 {
		b.WriteString(dimStyle.Render("  (none)") + "\n")
	}
	for _, p := range m.projects {
		mark := " "
		if p.Active {
			mark = "●"
		}
		b.WriteString(fmt.Sprintf("%s %s\n", mark, truncate(p.Name, w-3)))
	}
	b.WriteString("\n" + sectionStyle.Render("AGENTS") + "\n")
	for _, a := range m.agents {
		if a.ID == "mock" {
			continue
		}
		mark := "○"
		if a.Detected {
			mark = "●"
		}
		b.WriteString(fmt.Sprintf("%s %s\n", mark, a.ID))
	}
	b.WriteString("\n" + sectionStyle.Render("SESSIONS") + "\n")
	if len(m.sessions) == 0 {
		b.WriteString(dimStyle.Render("  (none)") + "\n")
	}
	for i, s := range m.sessions {
		if i > 8 {
			break
		}
		mark := "○"
		if s.Status == "running" {
			mark = "●"
		}
		if s.ID == m.attachedID {
			mark = "▶"
		}
		b.WriteString(fmt.Sprintf("%s %s %s\n", mark, s.AgentID, truncate(s.ID, w-10)))
	}

	content := b.String()
	style := borderStyle.Width(w).Height(h)
	return style.Render(content)
}

func (m Model) renderMain(w, h int) string {
	title := "ORCHESTRATOR"
	var content string
	if m.mode == modeAgent {
		title = "AGENT"
		if m.attachedID != "" {
			title += "  " + dimStyle.Render(m.attachedID)
		}
		content = m.agentView.View()
		if content == "" {
			content = dimStyle.Render("No agent output. Spawn or attach a session.")
		}
	} else {
		var lines []string
		lines = append(lines, "Goal: manage multi-vendor coding agents")
		lines = append(lines, "")
		start := 0
		if len(m.orchLog) > h-4 {
			start = len(m.orchLog) - (h - 4)
		}
		lines = append(lines, m.orchLog[start:]...)
		content = strings.Join(lines, "\n")
	}
	inner := sectionStyle.Render(title) + "\n" + content
	return borderStyle.Width(w).Height(h).Render(inner)
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
