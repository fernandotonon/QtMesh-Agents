package orchestrator

import (
	"strings"
)

// looksLikeQuotaExhausted detects vendor CLI output that means the account is
// out of tokens/credits/usage — not a transient stall the human can click through.
// Callers should fail over to the next agent rather than raising ⚠ attention.
func looksLikeQuotaExhausted(output string) (bool, string) {
	filtered := strings.ToLower(stripAgentBanners(stripEchoedUserPrompt(output)))
	filtered = stripOptionalMcpAuthNoise(filtered)
	if filtered == "" {
		return false, ""
	}

	patterns := []struct {
		needle string
		msg    string
	}{
		// Claude Code / Anthropic
		{"credit balance is too low", "claude credit/token balance exhausted"},
		{"credit balance too low", "claude credit/token balance exhausted"},
		{"you've hit your session limit", "claude session usage limit reached"},
		{"you've hit your weekly limit", "claude weekly usage limit reached"},
		{"you've hit your opus limit", "claude opus usage limit reached"},
		{"hit your usage limit", "vendor usage limit reached"},
		{"usage limit reached", "vendor usage limit reached"},
		{"out of credits", "vendor credits exhausted"},
		{"insufficient credits", "vendor credits exhausted"},
		{"insufficient quota", "vendor quota exhausted"},
		{"quota exceeded", "vendor quota exceeded"},
		{"quota_exceeded", "vendor quota exceeded"},
		{"rate_limit_error", "vendor rate/quota limit"},
		{"rate limit", "vendor rate/quota limit"},
		{"too many requests", "vendor rate/quota limit"},
		{"usage credits are required", "vendor usage credits required"},
		{"usage credits required", "vendor usage credits required"},
		{"reached your specified api usage limits", "api usage limit reached"},
		{"your credit balance is too low to access", "claude credit balance exhausted"},
		// Cursor
		{"you've used all your", "cursor usage exhausted"},
		{"used all of your fast requests", "cursor fast-request quota exhausted"},
		{"request would exceed your", "cursor usage would exceed quota"},
		{"spend limit reached", "vendor spend limit reached"},
		{"billing hard limit", "vendor billing hard limit"},
		// Codex / OpenAI
		{"insufficient_quota", "openai/codex quota exhausted"},
		{"exceeded your current quota", "openai/codex quota exhausted"},
		{"you exceeded your current quota", "openai/codex quota exhausted"},
		{"billing_not_active", "openai/codex billing inactive"},
	}
	for _, p := range patterns {
		if containsAttentionNeedle(filtered, p.needle) {
			return true, p.msg
		}
	}
	return false, ""
}

// isQuotaRetryable reports whether an executor error should trigger agent failover.
func isQuotaRetryable(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "quota exhausted") ||
		strings.Contains(msg, "usage limit") ||
		(strings.Contains(msg, "credit") && strings.Contains(msg, "exhaust")) ||
		strings.Contains(msg, "rate/quota")
}
