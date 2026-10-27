package orchestrator

import (
	"regexp"
	"strings"
)

var (
	// Line-anchored so source/docs containing the needle don't trip attention.
	reNeedsHumanLine = regexp.MustCompile(`(?im)^\s*needs_human:\s*(\S.*)$`)
	reAuthRequired   = regexp.MustCompile(`(?i)authrequired\s*\(|authrequirederror|www-authenticate\s*:|www_authenticate`)
)

// looksLikeNeedsAttention detects agent output that usually means the human
// must approve a permission / answer a prompt / complete auth.
func looksLikeNeedsAttention(output string, exitCode int) (bool, string) {
	lower := strings.ToLower(stripEchoedUserPrompt(output))
	filtered := stripAgentBanners(lower)
	filtered = stripOptionalMcpAuthNoise(filtered)

	// Prefer a real agent utterance — return the reason text for the UI.
	if m := reNeedsHumanLine.FindStringSubmatch(filtered); len(m) == 2 {
		reason := strings.TrimSpace(m[1])
		if !isTemplatePlaceholder(reason) && !isIntegrationApprovalNoise(reason) {
			return true, reason
		}
	}

	patterns := []struct {
		needle string
		msg    string
	}{
		// Avoid bare "approval required" — it matches policy docs
		// ("Approval required for merge / destructive git ops").
		{"waiting for approval", "agent is waiting for approval"},
		{"ask for approval", "agent wants approval to continue"},
		{"do you want to allow", "agent is prompting for confirmation"},
		{"permission denied", "permission denied — needs elevated access or approval"},
		{"requires permission", "agent requires permission"},
		{"press enter to continue", "agent paused for interactive continue"},
		{"waiting for user", "agent waiting for user input"},
		{"sandbox violation", "sandbox blocked an action — needs attention"},
		{"exceeded permissions", "agent exceeded sandbox permissions"},
		{"authorization required", "agent needs authentication"},
		{"please login", "agent needs you to log in"},
		{"please sign in", "agent needs you to sign in"},
		{"confirm to continue", "agent is waiting for confirmation"},
	}
	for _, p := range patterns {
		if containsAttentionNeedle(filtered, p.needle) {
			// Ignore soft "waiting for approval" when it's really integration ask spam.
			if isIntegrationApprovalNoise(filtered) {
				continue
			}
			return true, p.msg
		}
	}
	// AuthRequired only on non-source lines. Agents editing this repo dump
	// attention_detect.go itself (reAuthRequired := MustCompile(...)), which
	// must not pause the run after optional MCP noise is stripped.
	if authRequiredBlockingLine(filtered) {
		return true, "agent needs authentication (OAuth / login)"
	}
	// y/n only as its own prompt line, not buried in code/docs.
	for _, line := range strings.Split(filtered, "\n") {
		trim := strings.TrimSpace(line)
		if trim == "[y/n]" || trim == "(y/n)" || trim == "y/n" || trim == "yes/no" {
			return true, "agent is waiting for a yes/no confirmation"
		}
	}
	_ = exitCode
	return false, ""
}

// stripOptionalMcpAuthNoise drops background MCP transport OAuth failures.
// Codex/Claude often log AuthRequired for optional plugins (e.g. Cloudflare MCP)
// while continuing the coding task — that must not pause the run.
func stripOptionalMcpAuthNoise(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		low := strings.ToLower(line)
		if isOptionalMcpAuthLogLine(low) {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// containsAttentionNeedle matches a stall phrase only on lines that look like
// real agent/runtime output — not Go source the agent read (e.g. our own
// {"waiting for approval", "…"} table in attention_detect.go).
func containsAttentionNeedle(filtered, needle string) bool {
	for _, line := range strings.Split(filtered, "\n") {
		if !strings.Contains(line, needle) {
			continue
		}
		if lineIsSourceEchoOfNeedle(line, needle) {
			continue
		}
		return true
	}
	return false
}

func authRequiredBlockingLine(filtered string) bool {
	for _, line := range strings.Split(filtered, "\n") {
		if !reAuthRequired.MatchString(line) {
			continue
		}
		if lineIsSourceEchoOfNeedle(line, "authrequired") {
			continue
		}
		return true
	}
	return false
}

func lineIsSourceEchoOfNeedle(line, needle string) bool {
	trim := strings.TrimSpace(line)
	low := strings.ToLower(trim)
	// Unified diffs / markdown policy docs the agent is reading or writing.
	if strings.HasPrefix(trim, "+") || strings.HasPrefix(trim, "-") || strings.HasPrefix(trim, "@@") {
		return true
	}
	if strings.HasPrefix(low, "//") || strings.HasPrefix(low, "#") || strings.HasPrefix(low, "*") {
		return true
	}
	if strings.Contains(low, "mustcompile") || strings.Contains(low, "regexp.") {
		return true
	}
	// Docs: "Approval required for merge / destructive git ops"
	if strings.Contains(low, "for merge") || strings.Contains(low, "destructive git") ||
		strings.Contains(low, "git ops") {
		return true
	}
	// Composite-literal / test-table rows: {"waiting for approval", "agent is…"}
	if strings.Contains(trim, "{\"") || strings.Contains(trim, "\", \"") || strings.Contains(trim, "\",\"") {
		return true
	}
	// Quoted-only occurrence (needle appears only inside "…").
	if strings.Count(trim, "\"") >= 2 {
		unquoted := trim
		for {
			i := strings.Index(unquoted, "\"")
			if i < 0 {
				break
			}
			j := strings.Index(unquoted[i+1:], "\"")
			if j < 0 {
				break
			}
			unquoted = unquoted[:i] + unquoted[i+1+j+1:]
		}
		if !strings.Contains(strings.ToLower(unquoted), needle) {
			return true
		}
	}
	if strings.Contains(trim, ":= ") || strings.HasPrefix(trim, "func ") ||
		strings.HasPrefix(trim, "var ") || strings.HasPrefix(low, "\t{\"") {
		return true
	}
	_ = needle
	return false
}

func isOptionalMcpAuthLogLine(low string) bool {
	if !(strings.Contains(low, "authrequired") ||
		strings.Contains(low, "www-authenticate") ||
		strings.Contains(low, "www_authenticate") ||
		strings.Contains(low, "resource_metadata")) {
		return false
	}
	// MCP Rust transport / HTTP client noise.
	if strings.Contains(low, "rmcp::") ||
		strings.Contains(low, "rmcp:") ||
		strings.Contains(low, "transport channel closed") ||
		strings.Contains(low, "streamable_http") ||
		strings.Contains(low, "error sending request for url") {
		return true
	}
	// Known optional remote MCP hosts that auto-connect in Codex plugins.
	optionalHosts := []string{
		"mcp.cloudflare.com",
		"mcp.openai.com",
	}
	for _, h := range optionalHosts {
		if strings.Contains(low, h) {
			return true
		}
	}
	return false
}

// isIntegrationApprovalNoise ignores workers asking "approve for integration?"
// — that decision belongs to the integrate/merge_approval step, not mid-impl.
func isIntegrationApprovalNoise(s string) bool {
	low := strings.ToLower(strings.TrimSpace(s))
	if low == "" {
		return false
	}
	noise := []string{
		"approve for integration",
		"approve the implementation for integration",
		"approve these scoped changes for integration",
		"approve these changes for integration",
		"approve merging",
		"ready for developer review",
		"changes are ready for developer review",
		"approve the implementation",
		"should i also update",
		"will you merge the branch yourself",
		"apply the four routing files",
		"onto the `main` working tree",
		"onto the main working tree",
	}
	for _, n := range noise {
		if strings.Contains(low, n) {
			return true
		}
	}
	// Generic "approve … ?" that is clearly about landing/integration, not sandbox.
	if strings.Contains(low, "approve") && (strings.Contains(low, "integration") ||
		strings.Contains(low, "for review") || strings.Contains(low, "merge")) {
		return true
	}
	return false
}

func isTemplatePlaceholder(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return true
	}
	if strings.HasPrefix(s, "<") && strings.HasSuffix(s, ">") {
		return true
	}
	low := strings.ToLower(s)
	return low == "reason" || low == "one-line question" || strings.HasPrefix(low, "<reason")
}

// looksLikeIdleApprovalStall fires after the agent has gone quiet while still in
// interactive approval mode (Codex banner: approval: on-request).
func looksLikeIdleApprovalStall(output string) (bool, string) {
	lower := strings.ToLower(output)
	if strings.Contains(lower, "approval: on-request") {
		return true, "Codex is paused on approval: on-request — confirm in the ChatGPT/Codex app (or resolve the ⚠ mark)"
	}
	if needs, msg := looksLikeNeedsAttention(output, 0); needs {
		return true, msg
	}
	return false, ""
}

// stripEchoedUserPrompt drops the Codex-echoed user prompt so our own
// instruction text ("say NEEDS_HUMAN: …") does not false-trigger attention.
func stripEchoedUserPrompt(s string) string {
	lower := strings.ToLower(s)
	idx := strings.Index(lower, "\nuser\n")
	if idx < 0 {
		idx = strings.Index(lower, "\nuser\r\n")
	}
	if idx < 0 {
		return s
	}
	rest := s[idx+1:] // keep from "user\n…"
	restLower := strings.ToLower(rest)
	for _, marker := range []string{
		"\nassistant\n",
		"\nmcp ",
		"\nexec ",
		"\ntool:",
		"\ncodex\n",
		"\nthinking\n",
	} {
		if j := strings.Index(restLower, marker); j >= 0 {
			return rest[j:]
		}
	}
	// Prompt still being echoed — ignore for attention matching.
	return ""
}

func stripAgentBanners(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "approval:") ||
			strings.HasPrefix(trim, "sandbox:") ||
			strings.HasPrefix(trim, "workdir:") ||
			strings.HasPrefix(trim, "model:") ||
			strings.HasPrefix(trim, "provider:") ||
			strings.HasPrefix(trim, "reasoning ") ||
			strings.HasPrefix(trim, "session id:") ||
			strings.HasPrefix(trim, "--------") {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}
