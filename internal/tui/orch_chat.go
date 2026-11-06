package tui

import (
	"strings"
	"unicode"

	"github.com/charmbracelet/lipgloss"
)

var (
	youLabelStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("81"))
	orchLabelStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	leaderLabelStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("141"))
	sysLabelStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	youBodyStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("255"))
	orchBodyStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	codeStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("213"))
	boldStyle        = lipgloss.NewStyle().Bold(true)
	headerMDStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("159"))
	bulletStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("117"))
)

// orchLogPlain returns the chat history as plain text (for clipboard yank).
func orchLogPlain(log []string) string {
	var b strings.Builder
	for _, entry := range log {
		role, body := parseOrchTurn(entry)
		if role == "blank" {
			continue
		}
		label := role
		switch role {
		case "you":
			label = "You"
		case "you→leader":
			label = "You → Leader"
		case "leader":
			label = "Leader"
		case "orch":
			label = "Orchestrator"
		case "note":
			label = "Note"
		case "system":
			label = "System"
		}
		b.WriteString(label)
		b.WriteString("\n")
		b.WriteString(body)
		if !strings.HasSuffix(body, "\n") {
			b.WriteByte('\n')
		}
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// lastOrchReplyPlain returns the most recent orchestrator/leader reply body.
func lastOrchReplyPlain(log []string) string {
	for i := len(log) - 1; i >= 0; i-- {
		role, body := parseOrchTurn(log[i])
		if role == "orch" || role == "leader" {
			return strings.TrimSpace(body) + "\n"
		}
	}
	return orchLogPlain(log)
}

// appendOrchTurn stores one conversational turn as a single log entry (multi-line OK).
func appendOrchTurn(log []string, role, text string) []string {
	text = strings.TrimRight(text, "\r\n")
	if text == "" {
		return log
	}
	switch role {
	case "you", "you→leader", "leader", "system", "note":
		return append(log, role+": "+text)
	default:
		return append(log, "orch: "+text)
	}
}

func parseOrchTurn(entry string) (role, body string) {
	entry = strings.TrimRight(entry, "\r")
	prefixes := []string{"you→leader: ", "you: ", "leader: ", "orch: ", "system: ", "note: ", "→ "}
	for _, p := range prefixes {
		if strings.HasPrefix(entry, p) {
			role = strings.TrimSuffix(p, ": ")
			if role == "→" {
				role = "note"
			}
			return role, strings.TrimPrefix(entry, p)
		}
	}
	if entry == "" {
		return "blank", ""
	}
	return "system", entry
}

func renderOrchChat(log []string, width int) string {
	if width < 20 {
		width = 20
	}
	var b strings.Builder
	b.WriteString(headerMDStyle.Render("Orchestrator chat") + "\n")
	b.WriteString(dimStyle.Render("You talk here · select leader:… for a lead consult") + "\n")

	for _, entry := range log {
		role, body := parseOrchTurn(entry)
		if role == "blank" {
			continue
		}
		b.WriteByte('\n')
		label, bodySt := roleStyle(role)
		b.WriteString(label + "\n")
		rendered := renderChatMarkdown(body, width, bodySt)
		b.WriteString(rendered)
		if !strings.HasSuffix(rendered, "\n") {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func roleStyle(role string) (label string, body lipgloss.Style) {
	switch role {
	case "you":
		return youLabelStyle.Render("You"), youBodyStyle
	case "you→leader":
		return youLabelStyle.Render("You → Leader"), youBodyStyle
	case "leader":
		return leaderLabelStyle.Render("Leader"), orchBodyStyle
	case "orch":
		return orchLabelStyle.Render("Orchestrator"), orchBodyStyle
	case "note":
		return warnStyle.Render("Note"), dimStyle
	default:
		return sysLabelStyle.Render("System"), dimStyle
	}
}

// renderChatMarkdown does lightweight markdown for terminal chat readability.
func renderChatMarkdown(s string, width int, body lipgloss.Style) string {
	var out []string
	for _, para := range strings.Split(s, "\n") {
		trim := strings.TrimSpace(para)
		switch {
		case trim == "":
			out = append(out, "")
		case strings.HasPrefix(trim, "### "):
			out = append(out, wrapStyled(headerMDStyle.Render(strings.TrimPrefix(trim, "### ")), width)...)
		case strings.HasPrefix(trim, "## "):
			out = append(out, wrapStyled(headerMDStyle.Render(strings.TrimPrefix(trim, "## ")), width)...)
		case strings.HasPrefix(trim, "# "):
			out = append(out, wrapStyled(headerMDStyle.Render(strings.TrimPrefix(trim, "# ")), width)...)
		case strings.HasPrefix(trim, "- ") || strings.HasPrefix(trim, "* ") || strings.HasPrefix(trim, "• "):
			text := trim
			for _, p := range []string{"- ", "* ", "• "} {
				if strings.HasPrefix(text, p) {
					text = strings.TrimPrefix(text, p)
					break
				}
			}
			bullet := bulletStyle.Render("• ")
			wrapped := wrapPlain(styleInlineMD(text), width-2)
			for i, line := range strings.Split(wrapped, "\n") {
				if i == 0 {
					out = append(out, bullet+body.Render(line))
				} else {
					out = append(out, "  "+body.Render(line))
				}
			}
		case looksLikeFence(trim):
			out = append(out, codeStyle.Render(wrapPlain(trim, width)))
		default:
			styled := styleInlineMD(para)
			for _, line := range strings.Split(wrapPlain(stripANSIKeepLen(styled), width), "\n") {
				// Re-apply body color on plain wrap of already-styled is lossy;
				// wrap the raw text then apply inline styles per line instead.
				_ = line
			}
			for _, line := range strings.Split(wrapPlain(para, width), "\n") {
				out = append(out, body.Render(styleInlineMD(line)))
			}
		}
	}
	return strings.Join(out, "\n")
}

func looksLikeFence(s string) bool {
	return strings.HasPrefix(s, "```") || strings.HasPrefix(s, "    ")
}

func wrapStyled(s string, width int) []string {
	// lipgloss width counting is hard; wrap on plain then keep as single styled block lines.
	plain := stripANSIKeepLen(s)
	var out []string
	for _, line := range strings.Split(wrapPlain(plain, width), "\n") {
		out = append(out, headerMDStyle.Render(line))
	}
	return out
}

func styleInlineMD(s string) string {
	// `code`
	s = replaceDelim(s, "`", func(inner string) string {
		return codeStyle.Render(inner)
	})
	// **bold**
	s = replaceDelim(s, "**", func(inner string) string {
		return boldStyle.Render(inner)
	})
	return s
}

func replaceDelim(s, delim string, style func(string) string) string {
	if delim == "" || !strings.Contains(s, delim) {
		return s
	}
	parts := strings.Split(s, delim)
	if len(parts) < 3 {
		return s
	}
	var b strings.Builder
	for i, p := range parts {
		if i%2 == 1 && i+1 < len(parts) {
			b.WriteString(style(p))
		} else {
			b.WriteString(p)
		}
	}
	// Odd trailing delim left unclosed — fine.
	return b.String()
}

func stripANSIKeepLen(s string) string {
	// Best-effort strip for wrap width estimation.
	var b strings.Builder
	inEsc := false
	for _, r := range s {
		if r == 0x1b {
			inEsc = true
			continue
		}
		if inEsc {
			if unicode.IsLetter(r) {
				inEsc = false
			}
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
