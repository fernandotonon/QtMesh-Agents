package orchestrator

import "testing"

func TestLooksLikeNeedsAttention(t *testing.T) {
	ok, msg := looksLikeNeedsAttention("Waiting for approval to continue", 1)
	if !ok || msg == "" {
		t.Fatalf("expected attention, ok=%v msg=%q", ok, msg)
	}
	ok, _ = looksLikeNeedsAttention("all good, files written", 0)
	if ok {
		t.Fatal("false positive")
	}
	banner := "OpenAI Codex v0.163.0\napproval: never\nsandbox: workspace-write\nImplemented the feature.\n"
	ok, _ = looksLikeNeedsAttention(banner, 1)
	if ok {
		t.Fatal("banner false positive")
	}
}

func TestLooksLikeNeedsAttentionIgnoresEchoedPrompt(t *testing.T) {
	echo := "OpenAI Codex v0.163.0\n--------\napproval: never\n--------\nuser\n" +
		"You are a worker. If stuck say NEEDS_HUMAN: reason\n" +
		"APPROVAL REQUIRED would be bad to match here\n"
	ok, _ := looksLikeNeedsAttention(echo, 0)
	if ok {
		t.Fatal("echoed prompt should not trip attention")
	}
}

func TestLooksLikeNeedsAttentionAfterAssistant(t *testing.T) {
	out := "OpenAI Codex v0.163.0\napproval: never\nuser\nprompt here\nassistant\nWaiting for approval to continue\n"
	ok, _ := looksLikeNeedsAttention(out, 0)
	if !ok {
		t.Fatal("expected attention after assistant section")
	}
}

func TestLooksLikeNeedsAttentionNeedsHumanLine(t *testing.T) {
	out := "assistant\nDid some work.\nNEEDS_HUMAN: Git cannot create index.lock outside the writable sandbox\n"
	ok, msg := looksLikeNeedsAttention(out, 0)
	if !ok {
		t.Fatal("expected NEEDS_HUMAN")
	}
	if msg == "agent needs a human decision" || msg == "" {
		t.Fatalf("expected concrete reason, got %q", msg)
	}
	if !contains(msg, "index.lock") {
		t.Fatalf("reason should include index.lock, got %q", msg)
	}
}

func TestLooksLikeNeedsAttentionIgnoresIntegrationApprovalSpam(t *testing.T) {
	out := "assistant\nDone.\nNEEDS_HUMAN: Approve the implementation for integration?\nOPTIONS:\n1) Approve\n2) Reject\n"
	ok, _ := looksLikeNeedsAttention(out, 0)
	if ok {
		t.Fatal("integration approval ask must not pause mid-impl")
	}
	out2 := "assistant\nNEEDS_HUMAN: Changes are ready for developer review\n"
	ok, _ = looksLikeNeedsAttention(out2, 0)
	if ok {
		t.Fatal("ready for developer review must not pause")
	}
}

func TestLooksLikeNeedsAttentionIgnoresSourceNeedle(t *testing.T) {
	src := "\t{\"needs_human:\", \"agent needs a human decision\"},\n\t{\"authrequired\", \"msg\"},\n"
	ok, _ := looksLikeNeedsAttention(src, 0)
	if ok {
		t.Fatal("source code needles must not trip")
	}
}

func TestLooksLikeNeedsAttentionIgnoresPolicyDocLine(t *testing.T) {
	doc := "codex\n- Approval required for merge / destructive git ops\n+ created_at TEXT NOT NULL,\n"
	ok, _ := looksLikeNeedsAttention(doc, 0)
	if ok {
		t.Fatal("policy/docs/diff must not trip attention")
	}
}

func TestLooksLikeNeedsAttentionIgnoresOwnPatternTable(t *testing.T) {
	// Codex often cats attention_detect.go; those string literals must not pause the run.
	src := "\t{\"approval required\", \"agent is waiting for approval\"},\n" +
		"\t{\"waiting for approval\", \"agent is waiting for approval\"},\n" +
		"\t{\"permission denied\", \"permission denied — needs elevated access or approval\"},\n" +
		"\treAuthRequired   = regexp.MustCompile(`(?i)authrequired\\s*\\(|authrequirederror|www-authenticate\\s*:|www_authenticate`)\n" +
		"\tif reAuthRequired.MatchString(filtered) {\n" +
		"\t\treturn true, \"agent needs authentication (OAuth / login)\"\n" +
		"\t}\n"
	ok, _ := looksLikeNeedsAttention(src, 0)
	if ok {
		t.Fatal("attention_detect.go source must not trip attention")
	}
	// MCP noise + source dump together (common in live transcripts).
	mixed := `2026-10-26T15:49:03Z ERROR rmcp::transport::worker: worker quit with fatal: Transport channel closed, when AuthRequired(AuthRequiredError { www_authenticate_header: "Bearer realm=\"OAuth\", resource_metadata=\"https://mcp.cloudflare.com/.well-known/oauth-protected-resource/mcp\"" })
` + src + "codex\nI'll keep editing the files.\n"
	ok, _ = looksLikeNeedsAttention(mixed, 0)
	if ok {
		t.Fatal("MCP noise + source dump must not trip attention")
	}
	real := "codex\nWaiting for approval to continue\n"
	ok, msg := looksLikeNeedsAttention(real, 0)
	if !ok || msg == "" {
		t.Fatalf("expected real approval wait, ok=%v msg=%q", ok, msg)
	}
}

func TestLooksLikeNeedsAttentionAuthSpecific(t *testing.T) {
	src := "patterns := []struct{needle string}{{\"authrequired\", \"msg\"}}\n"
	ok, _ := looksLikeNeedsAttention(src, 0)
	if ok {
		t.Fatal("source mentioning authrequired should not trip")
	}
	// Optional Cloudflare MCP plugin auth must NOT pause coding work.
	mcpNoise := `2026-10-26T15:27:36Z ERROR rmcp::transport::worker: worker quit with fatal: Transport channel closed, when AuthRequired(AuthRequiredError { www_authenticate_header: "Bearer realm=\"OAuth\", resource_metadata=\"https://mcp.cloudflare.com/.well-known/oauth-protected-resource/mcp\"" })
codex
I'll keep working on the repo.
exec
/bin/zsh -lc "git status" in /tmp
 succeeded in 0ms:`
	ok, _ = looksLikeNeedsAttention(mcpNoise, 0)
	if ok {
		t.Fatal("optional MCP AuthRequired logs must not trip attention")
	}
	blocking := "assistant\nPlease sign in to continue with the provider.\n"
	ok, msg := looksLikeNeedsAttention(blocking, 1)
	if !ok || msg == "" {
		t.Fatalf("expected blocking sign-in prompt, ok=%v msg=%q", ok, msg)
	}
}

func TestLooksLikeIdleApprovalStall(t *testing.T) {
	ok, msg := looksLikeIdleApprovalStall("approval: on-request\nsandbox: workspace-write\n")
	if !ok || msg == "" {
		t.Fatalf("expected idle stall, ok=%v msg=%q", ok, msg)
	}
	ok, _ = looksLikeIdleApprovalStall("approval: never\nworking…\n")
	if ok {
		t.Fatal("never policy should not idle-stall")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		(len(s) > 0 && (func() bool {
			for i := 0; i+len(sub) <= len(s); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		})()))
}
