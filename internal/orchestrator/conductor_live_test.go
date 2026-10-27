package orchestrator

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/fernandotonon/QtMesh-Agents/internal/config"
	"github.com/fernandotonon/QtMesh-Agents/internal/storage"
)

func TestExtractAuthTargetsCloudflareMCP(t *testing.T) {
	cases := []string{
		`AuthRequired(AuthRequiredError { www_authenticate_header: "Bearer realm=\"OAuth\", resource_metadata=\"https://mcp.cloudflare.com/.well-known/oauth-protected-resource/mcp\", scope=\"user:read account:read\"" })`,
		`worker quit with fatal: Transport channel closed, when AuthRequired(AuthRequiredError { www_authenticate_header: "Bearer realm=\"OAuth\", resource_metadata=\"https://mcp.cloudflare.com/.well-known/oauth-protected-resource/mcp\", scope=\"user:read account:read\"" })`,
	}
	for _, out := range cases {
		urls, host := extractAuthTargets(out)
		if host != "mcp.cloudflare.com" {
			t.Fatalf("host=%q urls=%v\nout=%s", host, urls, out[:80])
		}
		if len(urls) == 0 || !strings.Contains(urls[0], "mcp.cloudflare.com") {
			t.Fatalf("urls=%v", urls)
		}
		if authProviderLabel(host) != "Cloudflare MCP (OAuth)" {
			t.Fatalf("label=%q", authProviderLabel(host))
		}
	}
}

func TestExtractNeedsHumanSkipsSourceRegex(t *testing.T) {
	out := "\treNeedsHumanLine = regexp.MustCompile(`(?im)^\\s*needs_human:\\s*(\\S.*)$`)\n" +
		"// instruction text (\"say NEEDS_HUMAN: …\") does not false-trigger attention.\n" +
		"NEEDS_HUMAN: <one-line question>\n" +
		"NEEDS_HUMAN: grant sandbox write for git index.lock\n"
	q := extractNeedsHumanQuestion(out)
	if q != "grant sandbox write for git index.lock" {
		t.Fatalf("q=%q", q)
	}
}

func TestExtractAuthTargetsIgnoresSourceNoise(t *testing.T) {
	out := `AuthRequired   = regexp.MustCompile(` + "`" + `(?i)authrequired` + "`" + `)
https://github.com/foo/bar/blob/main/x.go
https://mcp.cloudflare.com/.well-known/oauth-protected-resource/mcp`
	// Without AuthRequired( span, resource_metadata still picks cloudflare if present;
	// github blob should be filtered.
	urls, _ := extractAuthTargets(out)
	for _, u := range urls {
		if strings.Contains(u, "github.com") {
			t.Fatalf("should skip github noise, got %v", urls)
		}
	}
}

func TestChatAnswersAuthFromLiveTaskOutput(t *testing.T) {
	base := t.TempDir()
	store, err := storage.Open(base + "/db.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	eng := New(Deps{Config: config.Default(), Store: store, Planner: HeuristicPlanner{}})
	runID := "run-live-auth"
	taskID := runID + "-impl-1"
	now := time.Now().UTC()
	if err := store.UpsertRun(context.Background(), storage.Run{
		ID: runID, ProjectID: "demo", Objective: "test", Status: RunNeedsAttention,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{
		"output": `tool error
AuthRequired(AuthRequiredError { www_authenticate_header: "Bearer realm=\"OAuth\", resource_metadata=\"https://mcp.cloudflare.com/.well-known/oauth-protected-resource/mcp\", scope=\"user:read account:read\"" })
`,
	})
	if err := store.UpsertTask(context.Background(), storage.TaskRecord{
		ID: taskID, RunID: runID, ProjectID: "demo", Description: "impl",
		Status: "awaiting_input", AssignedAgent: "codex",
		ResultJSON: string(payload), Error: "agent needs authentication (OAuth / login)",
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	_ = store.UpsertAttention(context.Background(), storage.Attention{
		ID: "attn-1", Kind: "permission", Message: "agent needs authentication (OAuth / login)",
		Status: "open", RunID: runID, TaskID: taskID, CreatedAt: now,
	})

	c := NewConductor(eng, memProjects{list: []storage.Project{
		{ID: "demo", Name: "Demo", Path: base},
	}}, nil)
	res, err := c.Chat(context.Background(), "where are the agents trying to authenticate to?")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Actions) != 0 {
		t.Fatalf("should not start runs: %+v", res.Actions)
	}
	low := strings.ToLower(res.Reply)
	if !strings.Contains(low, "cloudflare") {
		t.Fatalf("expected cloudflare in reply, got:\n%s", res.Reply)
	}
	if !strings.Contains(res.Reply, "mcp.cloudflare.com") {
		t.Fatalf("expected auth URL in reply, got:\n%s", res.Reply)
	}
	if strings.Contains(low, "doesn't set any authentication") || strings.Contains(low, "docs/security") {
		t.Fatalf("should not narrate codebase, got:\n%s", res.Reply)
	}
}

func TestExtractAuthFromMCPRequestError(t *testing.T) {
	out := `error sending request for url (https://mcp.cloudflare.com/mcp)`
	urls, host := extractAuthTargets(out)
	if host != "mcp.cloudflare.com" || len(urls) == 0 {
		t.Fatalf("host=%q urls=%v", host, urls)
	}
}

func TestExtractAuthPrefersWellKnown(t *testing.T) {
	out := `error sending request for url (https://mcp.cloudflare.com/mcp)
AuthRequired(AuthRequiredError { resource_metadata=\"https://mcp.cloudflare.com/.well-known/oauth-protected-resource/mcp\" })`
	urls, host := extractAuthTargets(out)
	if host != "mcp.cloudflare.com" {
		t.Fatalf("host=%q", host)
	}
	if len(urls) == 0 || !strings.Contains(urls[0], ".well-known/oauth") {
		t.Fatalf("expected well-known first, urls=%v", urls)
	}
}
