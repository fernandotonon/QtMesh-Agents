package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/fernandotonon/QtMesh-Agents/internal/storage"
)

var (
	reResourceMetadata = regexp.MustCompile(`(?i)resource_metadata\s*=\s*\\*["'](https?://[^"'\\]+)`)
	reHTTPURL          = regexp.MustCompile(`https?://[^\s"'<>\\)\]]+`)
)

// BlockedAgent is a live task that needs the human (auth, permission, decision).
type BlockedAgent struct {
	TaskID   string
	RunID    string
	AgentID  string
	Error    string
	Question string
	AuthURLs []string
	AuthHost string
	Excerpt  string
}

// gatherBlockedAgents reads open attention + awaiting tasks and extracts
// concrete auth targets / NEEDS_HUMAN text from agent output.
func (c *Conductor) gatherBlockedAgents(ctx context.Context) []BlockedAgent {
	if c.engine == nil || c.engine.store == nil {
		return nil
	}
	seen := map[string]struct{}{}
	var out []BlockedAgent

	if open, err := c.engine.ListOpenAttention(ctx); err == nil {
		for _, a := range open {
			if a.TaskID == "" {
				continue
			}
			if _, ok := seen[a.TaskID]; ok {
				continue
			}
			if ba, ok := c.blockedFromTask(ctx, a.TaskID, a.Message); ok {
				seen[a.TaskID] = struct{}{}
				out = append(out, ba)
			}
		}
	}

	runs, err := c.engine.store.ListRuns(ctx, 30)
	if err != nil {
		return out
	}
	for _, r := range runs {
		if r.Status != RunNeedsAttention && r.Status != RunRunning && r.Status != RunPlanning {
			continue
		}
		tasks, err := c.engine.store.ListTasksByRun(ctx, r.ID)
		if err != nil {
			continue
		}
		for _, t := range tasks {
			if t.Status != "awaiting_input" {
				continue
			}
			if _, ok := seen[t.ID]; ok {
				continue
			}
			if ba, ok := blockedFromRecord(t, t.Error); ok {
				seen[t.ID] = struct{}{}
				out = append(out, ba)
			}
		}
	}
	return out
}

func (c *Conductor) blockedFromTask(ctx context.Context, taskID, fallbackErr string) (BlockedAgent, bool) {
	t, err := c.engine.store.GetTask(ctx, taskID)
	if err != nil {
		return BlockedAgent{}, false
	}
	return blockedFromRecord(t, fallbackErr)
}

func blockedFromRecord(t storage.TaskRecord, fallbackErr string) (BlockedAgent, bool) {
	output := outputFromResultJSON(t.ResultJSON)
	// Extract auth/questions from raw output — stripEchoedUserPrompt can drop the
	// AuthRequired line when it cuts at a later "codex" banner.
	ba := BlockedAgent{
		TaskID:  t.ID,
		RunID:   t.RunID,
		AgentID: t.AssignedAgent,
		Error:   strings.TrimSpace(t.Error),
	}
	if ba.Error == "" {
		ba.Error = strings.TrimSpace(fallbackErr)
	}
	if q := extractNeedsHumanQuestion(output); q != "" {
		ba.Question = q
	}
	ba.AuthURLs, ba.AuthHost = extractAuthTargets(output)
	ba.Excerpt = usefulExcerpt(output, ba)
	if ba.Error == "" && ba.Question == "" && len(ba.AuthURLs) == 0 {
		if needs, msg := looksLikeNeedsAttention(output, 0); needs {
			ba.Error = msg
		} else {
			return BlockedAgent{}, false
		}
	}
	// Prefer a concrete auth reason when we found OAuth URLs.
	if len(ba.AuthURLs) > 0 && (ba.Error == "" || strings.Contains(strings.ToLower(ba.Error), "approval") ||
		strings.Contains(strings.ToLower(ba.Error), "resumed")) {
		ba.Error = "agent needs authentication (OAuth / login)"
	}
	// Skip transient resume markers with nothing actionable yet.
	if strings.Contains(strings.ToLower(ba.Error), "resumed after daemon") &&
		ba.Question == "" && len(ba.AuthURLs) == 0 {
		return BlockedAgent{}, false
	}
	return ba, true
}

func outputFromResultJSON(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return raw
	}
	if s, ok := m["output"].(string); ok {
		return s
	}
	return ""
}

func extractNeedsHumanQuestion(output string) string {
	for _, line := range strings.Split(output, "\n") {
		trim := strings.TrimSpace(line)
		if looksLikeSourceNoiseLine(trim) {
			continue
		}
		low := strings.ToLower(trim)
		// Only real agent utterances: line begins with needs_human:
		if !strings.HasPrefix(low, "needs_human:") {
			continue
		}
		q := strings.TrimSpace(trim[len("needs_human:"):])
		// Handle "NEEDS_HUMAN:" with same length as needs_human:
		if len(trim) >= 12 && strings.EqualFold(trim[:12], "needs_human:") {
			q = strings.TrimSpace(trim[12:])
		}
		q = strings.Trim(q, "`\"")
		if isTemplatePlaceholder(q) || q == "" || looksLikeRegexFragment(q) {
			continue
		}
		return q
	}
	return ""
}

func looksLikeSourceNoiseLine(line string) bool {
	low := strings.ToLower(strings.TrimSpace(line))
	return strings.HasPrefix(low, "//") ||
		strings.HasPrefix(low, "#") ||
		strings.HasPrefix(low, "/*") ||
		strings.HasPrefix(low, "*") ||
		strings.HasPrefix(low, "{\"") || // Go composite / JSON test tables in source
		strings.Contains(low, "mustcompile") ||
		strings.Contains(low, "regexp.") ||
		strings.Contains(line, ":= ") ||
		strings.Contains(low, "sandbox :=") ||
		strings.Contains(low, "cfg.agents") ||
		strings.Contains(low, "--sandbox <") ||
		strings.Contains(low, "false-trigger") ||
		strings.Contains(low, "needs elevated access or approval") && strings.Contains(line, "{") ||
		(strings.Contains(line, "`") && (strings.Contains(low, "needs_human") || strings.Contains(low, "authrequired")))
}

func looksLikeRegexFragment(q string) bool {
	return strings.Contains(q, `\s*`) ||
		strings.Contains(q, `\S`) ||
		strings.Contains(q, "(?i") ||
		strings.Contains(q, "(?m") ||
		strings.HasPrefix(q, "^") ||
		strings.HasSuffix(q, "$") ||
		strings.HasSuffix(q, "$`)") ||
		strings.HasSuffix(q, "`")
}

func extractAuthTargets(output string) (urls []string, host string) {
	seen := map[string]struct{}{}
	add := func(u string) {
		u = strings.TrimRight(u, ".,);]")
		u = strings.TrimSuffix(u, `\`)
		if u == "" || !strings.HasPrefix(u, "http") {
			return
		}
		if looksLikeSourceNoiseURL(u) {
			return
		}
		if _, ok := seen[u]; ok {
			return
		}
		seen[u] = struct{}{}
		urls = append(urls, u)
		if host == "" {
			if parsed, err := url.Parse(u); err == nil && parsed.Host != "" {
				host = parsed.Host
			}
		}
	}

	for _, m := range reResourceMetadata.FindAllStringSubmatch(output, 8) {
		add(m[1])
	}
	// Lines that mention AuthRequired / www_authenticate / MCP OAuth hosts.
	for _, line := range strings.Split(output, "\n") {
		low := strings.ToLower(line)
		authLine := strings.Contains(low, "authrequired") ||
			strings.Contains(low, "www_authenticate") ||
			strings.Contains(low, "resource_metadata") ||
			strings.Contains(low, "oauth") ||
			(strings.Contains(low, "mcp.") && (strings.Contains(low, "auth") || strings.Contains(low, "error sending request")))
		if !authLine {
			continue
		}
		if looksLikeSourceNoiseLine(line) {
			continue
		}
		for _, u := range reHTTPURL.FindAllString(line, 8) {
			add(u)
		}
	}
	// Prefer OAuth metadata URLs first in the list.
	if len(urls) > 1 {
		var meta, rest []string
		for _, u := range urls {
			if strings.Contains(u, ".well-known/oauth") {
				meta = append(meta, u)
			} else {
				rest = append(rest, u)
			}
		}
		urls = append(meta, rest...)
		if len(urls) > 0 {
			if parsed, err := url.Parse(urls[0]); err == nil && parsed.Host != "" {
				host = parsed.Host
			}
		}
	}
	return urls, host
}

func looksLikeSourceNoiseURL(u string) bool {
	low := strings.ToLower(u)
	noise := []string{
		"github.com/", "/blob/", "/tree/", "golang.org/", "pkg.go.dev/",
		"raw.githubusercontent.com/", "example.com", "localhost",
	}
	for _, n := range noise {
		if strings.Contains(low, n) {
			return true
		}
	}
	return false
}

func usefulExcerpt(output string, ba BlockedAgent) string {
	if ba.Question != "" {
		return truncateObj(ba.Question, 240)
	}
	if len(ba.AuthURLs) > 0 {
		return ""
	}
	lines := strings.Split(output, "\n")
	var pick []string
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if trim == "" || looksLikeSourceNoiseLine(trim) {
			continue
		}
		low := strings.ToLower(trim)
		if strings.HasPrefix(low, "needs_human:") {
			q := strings.TrimSpace(trim[len("needs_human:"):])
			if isTemplatePlaceholder(q) {
				continue
			}
		}
		if strings.Contains(low, "authrequired(") ||
			strings.Contains(low, "www_authenticate") ||
			strings.Contains(low, "resource_metadata") ||
			strings.Contains(low, "permission denied") ||
			strings.Contains(low, "sandbox violation") ||
			strings.Contains(low, "index.lock") ||
			strings.HasPrefix(low, "needs_human:") {
			if strings.Contains(trim, "\"authrequired\"") || strings.Contains(trim, "`AuthRequired") {
				continue
			}
			pick = append(pick, trim)
			if len(pick) >= 2 {
				break
			}
		}
	}
	if len(pick) == 0 {
		return ""
	}
	return truncateObj(strings.Join(pick, " | "), 320)
}

func authProviderLabel(host string) string {
	h := strings.ToLower(host)
	switch {
	case strings.Contains(h, "cloudflare"):
		return "Cloudflare MCP (OAuth)"
	case strings.Contains(h, "openai") || strings.Contains(h, "chatgpt"):
		return "OpenAI / ChatGPT"
	case strings.Contains(h, "anthropic") || strings.Contains(h, "claude"):
		return "Anthropic / Claude"
	case strings.Contains(h, "github"):
		return "GitHub"
	case host != "":
		return host
	default:
		return ""
	}
}

func formatLiveSituation(blocked []BlockedAgent) string {
	if len(blocked) == 0 {
		return "No agents currently waiting on you."
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%d agent task(s) need you right now:\n", len(blocked)))
	for i, ba := range blocked {
		if i >= 6 {
			b.WriteString(fmt.Sprintf("  …and %d more\n", len(blocked)-i))
			break
		}
		b.WriteString(fmt.Sprintf("\n• Task `%s` (agent %s)\n", ba.TaskID, orDash(ba.AgentID)))
		if ba.Error != "" {
			b.WriteString(fmt.Sprintf("  Why: %s\n", ba.Error))
		}
		if ba.Question != "" {
			b.WriteString(fmt.Sprintf("  Agent asked: %s\n", ba.Question))
		}
		if label := authProviderLabel(ba.AuthHost); label != "" {
			b.WriteString(fmt.Sprintf("  Authenticating to: %s\n", label))
		}
		for _, u := range ba.AuthURLs {
			b.WriteString(fmt.Sprintf("  Auth URL: %s\n", u))
		}
		if ba.Excerpt != "" && ba.Question == "" {
			b.WriteString(fmt.Sprintf("  From agent output: %s\n", ba.Excerpt))
		}
	}
	b.WriteString("\nHow to unblock:\n")
	b.WriteString("• qma cannot collect a password in chat. OAuth/MCP login happens in the browser or the agent’s own app (ChatGPT/Codex, Claude, etc.).\n")
	b.WriteString("• Open the Auth URL above (or complete login in that app), then in the TUI select the ⚠ mark and choose “I’ve finished signing in — retry now”.\n")
	b.WriteString("• If the block is sandbox/git (index.lock / permission denied), pick “Grant elevated access (full disk)” instead.\n")
	return b.String()
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "?"
	}
	return s
}

func isAuthOrPermissionQuery(lower string) bool {
	if isContinueIntent(lower) {
		return false
	}
	if containsAny(lower, []string{"implement auth", "add auth", "build auth", "fix authentication"}) {
		return false
	}
	keys := []string{
		"authenticat", "sign in", "sign-in", "oauth", "password",
		"permission", "needs you", "need me", "waiting for me", "www-authenticate",
		"auth url", "auth link", "login link", "how do i auth",
		"how to auth", "grant access", "elevate",
	}
	for _, k := range keys {
		if strings.Contains(lower, k) {
			return true
		}
	}
	if strings.Contains(lower, "where") && (strings.Contains(lower, "auth") || strings.Contains(lower, "login") || strings.Contains(lower, "sign")) {
		return true
	}
	if strings.Contains(lower, "how") && (strings.Contains(lower, "auth") || strings.Contains(lower, "login") || strings.Contains(lower, "permission")) {
		return true
	}
	return false
}
