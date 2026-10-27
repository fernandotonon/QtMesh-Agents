package tui

import (
	"regexp"
	"strings"

	"github.com/fernandotonon/QtMesh-Agents/internal/ipc"
)

type replyOption struct {
	Key    string // short key shown in the list (1, 2, y, …)
	Label  string // human-readable label
	Reply  string // text sent to the worker (empty for custom/dismiss)
	Action string // reply | custom | dismiss
	// Permission grants applied on resume.
	ElevateSandbox       bool
	InteractiveApprovals bool
}

// buildAttentionOptions builds selectable choices for a NEEDS YOU item.
// Agent-provided OPTIONS come first; then context-specific defaults.
func buildAttentionOptions(attn ipc.AttentionInfo, task *ipc.TaskInfo) []replyOption {
	if attn.Kind == "merge_approval" || attn.Kind == "review_block" {
		opts := mergeApprovalOptions()
		for i := range opts {
			opts[i].Key = string(rune('1' + i))
		}
		return opts
	}

	var parsed []replyOption
	question := ""
	if task != nil {
		question, parsed = parseAgentChoices(task.Output)
	}
	if question == "" {
		question = attn.Message
	}

	out := make([]replyOption, 0, len(parsed)+6)
	out = append(out, parsed...)

	defaults := contextualDefaults(attn.Message+"\n"+question, task)
	seen := map[string]bool{}
	for _, o := range out {
		seen[strings.ToLower(strings.TrimSpace(o.Label))] = true
	}
	for _, d := range defaults {
		key := strings.ToLower(strings.TrimSpace(d.Label))
		if seen[key] {
			continue
		}
		out = append(out, d)
		seen[key] = true
	}
	for i := range out {
		out[i].Key = string(rune('1' + i))
		if i >= 9 {
			out[i].Key = string(rune('a' + i - 9))
		}
	}
	return out
}

func contextualDefaults(contextBlob string, task *ipc.TaskInfo) []replyOption {
	// Use attention/question text + a short tail of output — not the whole dump
	// (which may include repo source the agent read).
	blob := strings.ToLower(contextBlob)
	if task != nil {
		blob += "\n" + strings.ToLower(task.Error)
		tail := task.Output
		if len(tail) > 2500 {
			tail = tail[len(tail)-2500:]
		}
		blob += "\n" + strings.ToLower(tail)
	}
	switch {
	case strings.Contains(blob, "apply to main working tree") || strings.Contains(blob, "approve apply") ||
		strings.Contains(blob, "reject leave worktree") || strings.Contains(blob, "ready to apply worktree"):
		return mergeApprovalOptions()
	case strings.Contains(blob, "authrequired(") || strings.Contains(blob, "oauth") ||
		strings.Contains(blob, "www-authenticate") || strings.Contains(blob, "please sign in") ||
		strings.Contains(blob, "please login"):
		return []replyOption{
			{Label: "I've finished signing in — retry now", Reply: "Human reply: Authentication is done. Retry the same work now.", Action: "reply"},
			{Label: "Skip this task", Reply: "Human reply: Skip this task. Do not make further changes; summarize and exit.", Action: "reply"},
			{Label: "Type a custom reply…", Action: "custom"},
			{Label: "Dismiss warning only (do not resume)", Action: "dismiss"},
		}
	case strings.Contains(blob, "index.lock") || (strings.Contains(blob, "commit") && strings.Contains(blob, "sandbox")) ||
		strings.Contains(blob, "git cannot") || strings.Contains(blob, "read-only"):
		return []replyOption{
			{Label: "Grant elevated access & retry commit (full sandbox)", Reply: "Human reply: Elevated access granted. Retry the git commit and finish the task.", Action: "reply", ElevateSandbox: true},
			{Label: "Prompt me in the ChatGPT/Codex app for approvals", Reply: "Human reply: Ask for approvals interactively; I will confirm in the ChatGPT/Codex app.", Action: "reply", InteractiveApprovals: true, ElevateSandbox: true},
			{Label: "Skip the commit — leave the patch / worktree as-is", Reply: "Human reply: Skip committing. Leave the patch/worktree as-is, summarize what remains, and finish the task.", Action: "reply"},
			{Label: "Type a custom reply…", Action: "custom"},
			{Label: "Dismiss warning only (do not resume)", Action: "dismiss"},
		}
	case strings.Contains(blob, "approval") || strings.Contains(blob, "permission") || strings.Contains(blob, "sandbox"):
		return []replyOption{
			{Label: "False alarm — continue (no extra access)", Reply: "Human reply: False alarm. Continue the coding work; do not wait for approvals or OAuth.", Action: "reply"},
			{Label: "Grant elevated access & retry (full sandbox)", Reply: "Human reply: Elevated sandbox access granted. Retry and finish the task.", Action: "reply", ElevateSandbox: true},
			{Label: "Prompt me in the ChatGPT/Codex app for each approval", Reply: "Human reply: Use interactive approvals; I will confirm prompts in the ChatGPT/Codex app.", Action: "reply", InteractiveApprovals: true, ElevateSandbox: true},
			{Label: "Denied — stop; do not escalate further", Reply: "Human reply: Denied. Do not escalate permissions; stop and summarize.", Action: "reply"},
			{Label: "Type a custom reply…", Action: "custom"},
			{Label: "Dismiss warning only (do not resume)", Action: "dismiss"},
		}
	default:
		return []replyOption{
			{Label: "Continue — proceed with your best judgment", Reply: "Human reply: Continue. Proceed with your best judgment and finish the task.", Action: "reply"},
			{Label: "Retry — run the task again with the same goal", Reply: "Human reply: Retry the task. Same goal; try a different approach if you were blocked.", Action: "reply"},
			{Label: "Skip — stop this task; do not change more files", Reply: "Human reply: Skip this task. Do not make further changes; summarize what is left and exit.", Action: "reply"},
			{Label: "Type a custom reply…", Action: "custom"},
			{Label: "Dismiss warning only (do not resume the worker)", Action: "dismiss"},
		}
	}
}

func mergeApprovalOptions() []replyOption {
	return []replyOption{
		{Label: "Approve apply to main working tree", Reply: "Human reply: Approve apply to main working tree.", Action: "reply"},
		{Label: "Reject leave worktree", Reply: "Human reply: Reject leave worktree.", Action: "reply"},
		{Label: "Show findings", Reply: "Human reply: Show findings.", Action: "reply"},
		{Label: "Dismiss warning only (do not resume)", Action: "dismiss"},
	}
}

var (
	reNeedsHuman  = regexp.MustCompile(`(?im)^\s*needs_human:\s*(\S.*)$`)
	reOptLine     = regexp.MustCompile(`(?i)^\s*(?:[-*]|\(?[a-d0-9]\)?[.\)\]])\s+(.+)$`)
	rePlaceholder = regexp.MustCompile(`^<[^>]+>$`)
)

// parseAgentChoices extracts the NEEDS_HUMAN question and any A/B/1/2 option lines
// from the assistant portion of agent output (ignores echoed prompt templates).
func parseAgentChoices(output string) (question string, opts []replyOption) {
	clean := cleanAgentFeedback(stripEchoedPromptForChoices(output))
	if clean == "" {
		clean = cleanAgentFeedback(output)
	}
	if clean == "" {
		return "", nil
	}
	lines := strings.Split(clean, "\n")

	needsIdx := -1
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if m := reNeedsHuman.FindStringSubmatch(line); len(m) == 2 {
			q := strings.TrimSpace(m[1])
			if isPlaceholderText(q) {
				continue
			}
			// Skip source-code false positives like {"needs_human:", "…"}.
			if strings.Contains(line, "{\"") || strings.Contains(line, "\",") {
				continue
			}
			question = q
			needsIdx = i
			break
		}
	}

	start := needsIdx
	if start < 0 {
		start = 0
	}
	inOptions := false
	for _, line := range lines[start:] {
		trim := strings.TrimSpace(line)
		if trim == "" {
			continue
		}
		if reNeedsHuman.MatchString(trim) {
			continue
		}
		if strings.EqualFold(trim, "options:") || strings.EqualFold(trim, "options") {
			inOptions = true
			continue
		}
		if !inOptions {
			continue // only parse explicit OPTIONS blocks from the agent
		}
		if m := reOptLine.FindStringSubmatch(trim); len(m) == 2 {
			label := strings.TrimSpace(m[1])
			if label == "" || len(label) > 200 || isPlaceholderText(label) {
				continue
			}
			opts = append(opts, replyOption{
				Label:  label,
				Reply:  "Human reply: " + label,
				Action: "reply",
			})
		}
	}
	if len(opts) > 8 {
		opts = opts[:8]
	}
	return question, opts
}

func isPlaceholderText(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return true
	}
	if rePlaceholder.MatchString(s) {
		return true
	}
	low := strings.ToLower(s)
	switch low {
	case "reason", "<reason>", "one-line question", "<one-line question>",
		"first choice", "<first choice>", "second choice", "<second choice>":
		return true
	}
	if strings.HasPrefix(s, "<") && strings.HasSuffix(s, ">") {
		return true
	}
	return false
}

// stripEchoedPromptForChoices drops the Codex-echoed user prompt so template
// text like NEEDS_HUMAN: <reason> / OPTIONS: 1) <first choice> is ignored.
func stripEchoedPromptForChoices(s string) string {
	lower := strings.ToLower(s)
	idx := strings.Index(lower, "\nuser\n")
	if idx < 0 {
		idx = strings.Index(lower, "\nuser\r\n")
	}
	if idx < 0 {
		return s
	}
	rest := s[idx+1:]
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
	return ""
}

func attentionQuestion(attn ipc.AttentionInfo, task *ipc.TaskInfo) string {
	if task != nil {
		if q, _ := parseAgentChoices(task.Output); q != "" {
			return q
		}
	}
	// Prefer a line-anchored NEEDS_HUMAN inside the attention message itself.
	if m := reNeedsHuman.FindStringSubmatch(attn.Message); len(m) == 2 && !isPlaceholderText(m[1]) {
		return strings.TrimSpace(m[1])
	}
	return attn.Message
}

// attentionAgentExcerpt returns the NEEDS_HUMAN ask (and a little context), not
// random source the agent happened to read earlier in the transcript.
func attentionAgentExcerpt(task *ipc.TaskInfo, attnMsg string, maxLines, width int) string {
	if maxLines < 2 {
		maxLines = 2
	}
	if task == nil {
		if attnMsg != "" {
			return wrapPlain(attnMsg, width)
		}
		return ""
	}
	raw := strings.TrimSpace(task.Output)
	if raw == "" {
		raw = task.Error
	}
	if raw == "" {
		return wrapPlain(attnMsg, width)
	}

	clean := cleanAgentFeedback(stripEchoedPromptForChoices(raw))
	if clean == "" {
		clean = cleanAgentFeedback(raw)
	}
	lines := strings.Split(clean, "\n")

	needsIdx := -1
	var needsLine string
	for i := len(lines) - 1; i >= 0; i-- {
		trim := strings.TrimSpace(lines[i])
		if m := reNeedsHuman.FindStringSubmatch(trim); len(m) == 2 {
			if isPlaceholderText(m[1]) || strings.Contains(trim, "{\"") {
				continue
			}
			needsIdx = i
			needsLine = trim
			break
		}
	}

	if needsIdx >= 0 {
		start := needsIdx - 4
		if start < 0 {
			start = 0
		}
		block := lines[start : needsIdx+1]
		// Ensure the NEEDS_HUMAN line is visible even if wrap eats space.
		snippet := strings.Join(block, "\n")
		if !strings.Contains(snippet, needsLine) {
			snippet = needsLine
		}
		return tailLines(snippet, width, maxLines)
	}

	// Fall back only to short prose that looks like a real ask — never dump
	// diffs/SQL/source the agent was editing (that made NEEDS YOU unreadable).
	var useful []string
	for i := len(lines) - 1; i >= 0 && len(useful) < maxLines; i-- {
		trim := strings.TrimSpace(lines[i])
		if trim == "" || looksLikeSourceLine(trim) || !looksLikeAgentQuestion(trim) {
			continue
		}
		useful = append([]string{trim}, useful...)
	}
	if len(useful) == 0 {
		msg := "No clear question from the agent.\n" +
			"This pause is probably a false alarm (keyword in docs/source/diff it read).\n" +
			"You are not being asked to sign in or grant access unless a real NEEDS_HUMAN line appears.\n" +
			"Prefer: dismiss, or continue/retry — do not grant elevated access for this."
		return wrapPlain(msg, width)
	}
	return tailLines(strings.Join(useful, "\n"), width, maxLines)
}

func looksLikeAgentQuestion(s string) bool {
	trim := strings.TrimSpace(s)
	low := strings.ToLower(trim)
	if len(trim) < 8 || len(trim) > 240 {
		return false
	}
	if strings.HasPrefix(low, "needs_human:") {
		return true
	}
	if strings.Contains(low, "?") {
		return true
	}
	prompts := []string{
		"waiting for approval", "please sign", "please login", "do you want",
		"permission denied", "sandbox", "grant", "confirm", "allow ",
	}
	for _, p := range prompts {
		if strings.Contains(low, p) {
			return true
		}
	}
	return false
}

func looksLikeSourceLine(s string) bool {
	trim := strings.TrimSpace(s)
	low := strings.ToLower(trim)
	if strings.HasPrefix(trim, "+") || strings.HasPrefix(trim, "-") || strings.HasPrefix(trim, "@@") {
		return true
	}
	if strings.HasPrefix(trim, "func ") || strings.HasPrefix(trim, "type ") ||
		strings.HasPrefix(trim, "package ") || strings.HasPrefix(trim, "import ") ||
		strings.HasPrefix(trim, "return ") || strings.HasPrefix(trim, "if ") ||
		strings.HasPrefix(trim, "for ") || strings.HasPrefix(trim, "switch ") ||
		strings.HasPrefix(trim, "case ") || strings.HasPrefix(trim, "defer ") ||
		strings.HasPrefix(trim, "var ") || strings.HasPrefix(trim, "const ") ||
		strings.HasPrefix(trim, "//") || strings.HasPrefix(trim, "/*") ||
		strings.Contains(trim, "{\"") || strings.HasPrefix(trim, "}") ||
		strings.HasPrefix(trim, "{") || strings.Contains(trim, ":= ") ||
		strings.Contains(trim, " = ") && (strings.Contains(trim, ".UTC()") ||
			strings.Contains(trim, "Upsert") || strings.Contains(trim, "store.") ||
			strings.Contains(trim, "time.Now") || strings.HasSuffix(trim, "{") ||
			strings.HasSuffix(trim, ")") || strings.Contains(trim, "Bench")) {
		return true
	}
	if strings.Contains(low, "create table") || strings.Contains(low, "alter table") ||
		strings.Contains(low, "primary key") || strings.HasSuffix(trim, ");") {
		return true
	}
	if strings.Contains(low, "select ") && strings.Contains(low, "from ") {
		return true
	}
	if strings.Contains(low, "querycontext") || strings.Contains(low, "execcontext") ||
		strings.Contains(low, "nullstr(") || strings.Contains(low, "sql.null") {
		return true
	}
	// Heavy punctuation typical of code, not prose.
	if strings.Count(trim, "(") > 2 && strings.Count(trim, ")") > 2 {
		return true
	}
	return false
}
