package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fernandotonon/QtMesh-Agents/internal/ipc"
)

func TestParseAgentChoicesOptions(t *testing.T) {
	out := `
Some work done.
NEEDS_HUMAN: Which approach should we take?
OPTIONS:
1) Commit only the scoped patch
2) Leave uncommitted and continue tomorrow
3) Abort and revert
`
	q, opts := parseAgentChoices(out)
	if !strings.Contains(q, "Which approach") {
		t.Fatalf("question=%q", q)
	}
	if len(opts) < 3 {
		t.Fatalf("expected 3 options, got %#v", opts)
	}
	if !strings.Contains(opts[0].Label, "scoped patch") {
		t.Fatalf("opt0=%q", opts[0].Label)
	}
}

func TestBuildAttentionOptionsIncludesDefaults(t *testing.T) {
	opts := buildAttentionOptions(ipc.AttentionInfo{
		ID: "a1", Kind: "permission", Message: "agent needs a human decision",
	}, &ipc.TaskInfo{
		ID: "t1", Status: "awaiting_input",
		Output: "NEEDS_HUMAN: Sandbox blocked git write.\n",
	})
	if len(opts) < 4 {
		t.Fatalf("expected defaults, got %d", len(opts))
	}
	foundCustom := false
	for _, o := range opts {
		if o.Action == "custom" {
			foundCustom = true
		}
	}
	if !foundCustom {
		t.Fatal("missing custom option")
	}
}

func TestIgnoresPromptPlaceholders(t *testing.T) {
	out := "user\nNEEDS_HUMAN: <one-line question>\nOPTIONS:\n1) <first choice>\n2) <second choice>\nassistant\nAll done.\n"
	q, opts := parseAgentChoices(out)
	if q != "" {
		t.Fatalf("placeholder question leaked: %q", q)
	}
	if len(opts) != 0 {
		t.Fatalf("placeholder options leaked: %#v", opts)
	}
}

func TestAuthDefaults(t *testing.T) {
	opts := buildAttentionOptions(ipc.AttentionInfo{
		Message: "agent needs authentication (OAuth / login)",
	}, nil)
	if len(opts) < 2 || !strings.Contains(strings.ToLower(opts[0].Label), "sign") {
		t.Fatalf("expected auth-first option, got %#v", opts)
	}
}

func TestCommitSandboxDefaults(t *testing.T) {
	opts := buildAttentionOptions(ipc.AttentionInfo{
		Message: "Git cannot create index.lock outside the writable sandbox",
	}, &ipc.TaskInfo{
		Output: "assistant\nNEEDS_HUMAN: Git cannot create `index.lock` outside the writable sandbox, so committing is blocked.\n",
	})
	if len(opts) < 1 || !opts[0].ElevateSandbox {
		t.Fatalf("expected elevated-access option first, got %#v", opts)
	}
}

func TestAttentionDetailShowsChoices(t *testing.T) {
	m := New(nil)
	m.ready = true
	m.width = 80
	m.height = 22
	m.attention = []ipc.AttentionInfo{{
		ID: "attn-1", Kind: "permission", Message: "agent needs a human decision",
		RunID: "run-1", TaskID: "run-1-impl-1",
	}}
	m.runs = []ipc.RunInfo{{
		ID: "run-1", Status: "needs_attention",
		Tasks: []ipc.TaskInfo{{
			ID: "run-1-impl-1", Status: "awaiting_input", AssignedAgent: "codex",
			Output: "assistant\nNEEDS_HUMAN: Commit the patch?\nOPTIONS:\n1) Yes, commit\n2) No, leave dirty\n",
		}},
	}}
	m.openAttentionDetail("attn-1")
	view := m.View()
	if !strings.Contains(view, "Choose a reply") {
		t.Fatalf("missing choices header:\n%s", view)
	}
	if !strings.Contains(view, "Yes, commit") {
		t.Fatalf("missing parsed option:\n%s", view)
	}
	if m.focus != focusOptions {
		t.Fatalf("focus=%v want options", m.focus)
	}
}

func TestAttentionDetailShowsAgentSaid(t *testing.T) {
	m := New(nil)
	m.ready = true
	m.width = 90
	m.height = 24
	m.attention = []ipc.AttentionInfo{{
		ID: "attn-1", Kind: "permission",
		Message: "agent needs a human decision",
		RunID:   "run-1", TaskID: "run-1-impl-1",
	}}
	m.runs = []ipc.RunInfo{{
		ID: "run-1", Status: "needs_attention",
		Tasks: []ipc.TaskInfo{{
			ID: "run-1-impl-1", Status: "awaiting_input", AssignedAgent: "codex",
			Output: "assistant\nfunc (n treeNode) render() {}\nImplemented the feature.\nNEEDS_HUMAN: Git cannot create `index.lock` outside the writable sandbox, so committing is blocked.\n",
		}},
	}}
	m.openAttentionDetail("attn-1")
	view := m.View()
	if !strings.Contains(view, "Agent said") {
		t.Fatalf("missing Agent said:\n%s", view)
	}
	if !strings.Contains(view, "index.lock") {
		t.Fatalf("expected NEEDS_HUMAN reason in excerpt:\n%s", view)
	}
	if strings.Contains(view, "treeNode") {
		t.Fatalf("should not show source noise:\n%s", view)
	}
	if !strings.Contains(view, "Choose a reply") {
		t.Fatalf("options should remain visible:\n%s", view)
	}
}

func TestAttentionTabReturnsToTree(t *testing.T) {
	m := New(nil)
	m.ready = true
	m.width = 80
	m.height = 20
	m.attention = []ipc.AttentionInfo{{
		ID: "attn-1", Kind: "permission", Message: "agent needs authentication (OAuth / login)",
		RunID: "run-1", TaskID: "t1",
	}}
	m.runs = []ipc.RunInfo{{ID: "run-1", Tasks: []ipc.TaskInfo{{ID: "t1", Status: "awaiting_input"}}}}
	m.openAttentionDetail("attn-1")
	if m.focus != focusOptions {
		t.Fatal("expected options focus")
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	got := next.(Model)
	if got.focus != focusTree {
		t.Fatalf("tab should focus tree, got %v", got.focus)
	}
	next, _ = got.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	got = next.(Model)
	if got.focus != focusTree {
		t.Fatal("j on tree should stay on tree")
	}
}
