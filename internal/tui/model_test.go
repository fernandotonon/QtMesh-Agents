package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fernandotonon/QtMesh-Agents/internal/ipc"
)

func TestModelQuitOnQ(t *testing.T) {
	m := New(nil)
	m.ready = true
	m.focus = focusTree
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if cmd == nil {
		t.Fatal("expected quit cmd")
	}
}

func TestModelTabSwitchesFocus(t *testing.T) {
	m := New(nil)
	m.ready = true
	m.focus = focusInput
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	got := next.(Model)
	if got.focus != focusTree {
		t.Fatalf("focus=%v want tree", got.focus)
	}
}

func TestBuildTreeOrchestratorRoot(t *testing.T) {
	m := New(nil)
	m.runs = []ipc.RunInfo{{
		ID: "run-1", Status: "running", Objective: "add hair", UpdatedAt: "2026-01-02",
		Tasks: []ipc.TaskInfo{
			{ID: "run-1-discover", Status: "succeeded", AssignedAgent: "claude"},
			{ID: "run-1-impl-1", Status: "running", AssignedAgent: "codex"},
		},
	}}
	m.rebuildTree()
	if len(m.tree) == 0 || m.tree[0].Kind != nodeRoot {
		t.Fatalf("expected orchestrator root, got %+v", m.tree)
	}
	if !strings.Contains(m.tree[0].Label, "1 working") {
		t.Fatalf("root label should show working count: %q", m.tree[0].Label)
	}
	foundTask := false
	for _, n := range m.tree {
		if n.Kind == nodeTask && n.Status == "running" && n.Agent == "codex" {
			foundTask = true
		}
	}
	if !foundTask {
		t.Fatalf("expected running codex task in tree: %+v", m.tree)
	}
}

func TestTreeNavigationActivateRun(t *testing.T) {
	m := New(nil)
	m.ready = true
	m.focus = focusTree
	m.runs = []ipc.RunInfo{{
		ID: "run-abc", Status: "planning", Objective: "ship it",
		Tasks: []ipc.TaskInfo{{ID: "run-abc-discover", Status: "pending"}},
	}}
	m.rebuildTree()
	for i, n := range m.tree {
		if n.Kind == nodeRun {
			m.cursor = i
			break
		}
	}
	next, _ := m.handleTreeKey("enter")
	got := next.(Model)
	if got.mode != modeRunDetail || got.selectedRun != "run-abc" {
		t.Fatalf("mode=%v run=%s", got.mode, got.selectedRun)
	}
}

func TestSidebarDoesNotOverflowHeight(t *testing.T) {
	m := New(nil)
	m.ready = true
	m.width = 60
	m.height = 12
	m.layout()
	// Many runs that would explode if all auto-expanded.
	for i := 0; i < 10; i++ {
		id := "run-" + strings.Repeat("0", 10) + string(rune('a'+i))
		tasks := make([]ipc.TaskInfo, 6)
		for j := range tasks {
			tasks[j] = ipc.TaskInfo{ID: id + "-impl-" + string(rune('1'+j)), Status: "running", AssignedAgent: "codex"}
		}
		m.runs = append(m.runs, ipc.RunInfo{
			ID: id, Status: "running", Objective: "work", UpdatedAt: "2026-01-01",
			Tasks: tasks,
		})
	}
	m.rebuildTree()
	view := m.View()
	lines := strings.Split(view, "\n")
	if len(lines) > m.height {
		t.Fatalf("view height %d > terminal %d\n%s", len(lines), m.height, view)
	}
	// Only hottest run auto-expands → tree stays short enough to navigate.
	expandedRuns := 0
	for _, n := range m.tree {
		if n.Kind == nodeRun && n.Expanded {
			expandedRuns++
		}
	}
	if expandedRuns > 1 {
		t.Fatalf("expected at most 1 auto-expanded run, got %d", expandedRuns)
	}
}

func TestTaskDetailShowsAgentFeedback(t *testing.T) {
	m := New(nil)
	m.ready = true
	m.width = 80
	m.height = 24
	m.layout()
	m.mode = modeTaskDetail
	m.selectedRun = "run-1"
	m.selectedTask = "run-1-impl-1"
	m.runs = []ipc.RunInfo{{
		ID: "run-1", Status: "running", Objective: "ship it",
		Tasks: []ipc.TaskInfo{{
			ID: "run-1-impl-1", Status: "running", AssignedAgent: "codex",
			Description: "implement feature",
			Output:      "OpenAI Codex v0.163.0\napproval: never\n--------\nEdited router.go\nAdded usage counters\n",
		}},
	}}
	view := m.View()
	if !strings.Contains(view, "Agent feedback") {
		t.Fatalf("expected Agent feedback section:\n%s", view)
	}
	if !strings.Contains(view, "Edited router.go") {
		t.Fatalf("expected agent output in view:\n%s", view)
	}
	if strings.Contains(view, "approval: never") {
		t.Fatalf("banner should be stripped:\n%s", view)
	}
}

func TestAwaitingInputShowsWorkerReplyField(t *testing.T) {
	m := New(nil)
	m.ready = true
	m.width = 70
	m.height = 18
	m.mode = modeTaskDetail
	m.focus = focusInput
	m.selectedRun = "run-1"
	m.selectedTask = "run-1-impl-1"
	m.runs = []ipc.RunInfo{{
		ID: "run-1", Status: "needs_attention",
		Tasks: []ipc.TaskInfo{{
			ID: "run-1-impl-1", Status: "awaiting_input", AssignedAgent: "codex",
			Error:  "agent needs a human decision",
			Output: "NEEDS_HUMAN: which approach?\n",
		}},
	}}
	m.layout()
	view := m.View()
	if !strings.Contains(view, "WORKER ›") && !strings.Contains(stripForWrap(view), "WORKER") {
		t.Fatalf("expected WORKER reply field:\n%s", view)
	}
	if !strings.Contains(view, "waiting") && !strings.Contains(view, "reply") {
		t.Fatalf("expected waiting/reply hint:\n%s", view)
	}
	lines := strings.Split(view, "\n")
	if len(lines) > m.height {
		t.Fatalf("overflow %d > %d", len(lines), m.height)
	}
}

func TestTaskDetailKeepsPromptVisible(t *testing.T) {
	m := New(nil)
	m.ready = true
	m.width = 60
	m.height = 14
	m.layout()
	m.mode = modeTaskDetail
	m.focus = focusInput
	m.selectedRun = "run-1"
	m.selectedTask = "run-1-impl-1"
	var huge strings.Builder
	for i := 0; i < 200; i++ {
		huge.WriteString(fmt.Sprintf("ERROR: Reconnecting... waiting for network line-%d with lots of text to wrap\n", i))
	}
	huge.WriteString("FINAL_WORKER_LINE\n")
	m.runs = []ipc.RunInfo{{
		ID: "run-1", Status: "running",
		Tasks: []ipc.TaskInfo{{
			ID: "run-1-impl-1", Status: "running", AssignedAgent: "codex",
			Output: huge.String(),
		}},
	}}
	view := m.View()
	lines := strings.Split(view, "\n")
	if len(lines) > m.height {
		t.Fatalf("view height %d > terminal %d\n%s", len(lines), m.height, view)
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, ">") {
		t.Fatalf("expected input prompt visible:\n%s", view)
	}
	if !strings.Contains(joined, "FINAL_WORKER_LINE") {
		t.Fatalf("expected latest worker feedback:\n%s", view)
	}
	// Latest feedback must sit near the bottom (above the prompt), not cropped away.
	endIdx := -1
	promptIdx := -1
	for i, line := range lines {
		if strings.Contains(line, "FINAL_WORKER_LINE") {
			endIdx = i
		}
		if strings.HasPrefix(strings.TrimSpace(stripForWrap(line)), ">") {
			promptIdx = i
		}
	}
	if endIdx < 0 || promptIdx < 0 {
		t.Fatalf("missing end=%d prompt=%d in:\n%s", endIdx, promptIdx, view)
	}
	if endIdx > promptIdx {
		t.Fatalf("feedback end after prompt (end=%d prompt=%d)", endIdx, promptIdx)
	}
	if promptIdx-endIdx > 4 {
		t.Fatalf("feedback end too far from prompt (end=%d prompt=%d):\n%s", endIdx, promptIdx, view)
	}
}

func TestOrchChatShowsLatest(t *testing.T) {
	m := New(nil)
	m.ready = true
	m.width = 80
	m.height = 16
	m.layout()
	for i := 0; i < 40; i++ {
		m.orchLog = appendOrchTurn(m.orchLog, "orch", "line-"+strings.Repeat("x", 20)+"-"+string(rune('0'+i%10)))
	}
	m.orchLog = appendOrchTurn(m.orchLog, "you", "final question")
	m.orchLog = appendOrchTurn(m.orchLog, "orch", "FINAL_REPLY_VISIBLE")
	m.syncOrchViewOpts(true)
	view := m.View()
	if !strings.Contains(view, "FINAL_REPLY_VISIBLE") {
		t.Fatalf("expected latest orch reply in view:\n%s", view)
	}
	lines := strings.Split(view, "\n")
	if len(lines) > m.height {
		t.Fatalf("view height %d > terminal %d", len(lines), m.height)
	}
}

func TestOrchChatPreservesScrollWhenNotAtBottom(t *testing.T) {
	m := New(nil)
	m.ready = true
	m.width = 80
	m.height = 20
	m.layout()
	for i := 0; i < 50; i++ {
		m.orchLog = appendOrchTurn(m.orchLog, "orch", fmt.Sprintf("history-line-%02d", i))
	}
	m.syncOrchViewOpts(true)
	if !m.orchView.AtBottom() {
		t.Fatal("expected at bottom after pin")
	}
	m.orchView.GotoTop()
	if m.orchView.AtBottom() {
		t.Fatal("expected scrolled away from bottom")
	}
	y := m.orchView.YOffset
	m.syncOrchView() // layout-style refresh must not wipe scroll
	if m.orchView.YOffset != y {
		t.Fatalf("scroll reset: YOffset %d → %d", y, m.orchView.YOffset)
	}
	if m.orchView.AtBottom() {
		t.Fatal("syncOrchView without force should not jump to bottom")
	}
}

func TestOrchChatRendersTurnsNotPerLinePrefix(t *testing.T) {
	rendered := renderOrchChat([]string{
		"you: did it work?",
		"orch: ## Verdict\nAnswer: **no**\n\n- task failed\n- branch feature/x",
	}, 60)
	if strings.Count(rendered, "Orchestrator") < 1 {
		t.Fatalf("missing orch label:\n%s", rendered)
	}
	if strings.Count(rendered, "orch:") > 0 {
		t.Fatalf("should not repeat orch: on every line:\n%s", rendered)
	}
	if !strings.Contains(rendered, "You") || !strings.Contains(rendered, "did it work?") {
		t.Fatalf("missing you turn:\n%s", rendered)
	}
}

func TestTreeHidesDeadSessions(t *testing.T) {
	m := New(nil)
	m.sessions = []ipc.SessionInfo{
		{ID: "cursor-old", AgentID: "cursor", Status: "interrupted"},
		{ID: "claude-live", AgentID: "claude", Status: "running"},
	}
	m.rebuildTree()
	var labels []string
	for _, n := range m.tree {
		if n.Kind == nodeSession {
			labels = append(labels, n.SessionID)
		}
	}
	if len(labels) != 1 || labels[0] != "claude-live" {
		t.Fatalf("expected only running session, got %v", labels)
	}
}

func TestTreeScrollFollowsCursor(t *testing.T) {
	m := New(nil)
	m.sideInnerH = 5
	for i := 0; i < 20; i++ {
		m.runs = append(m.runs, ipc.RunInfo{
			ID: "run-" + string(rune('a'+i)), Status: "succeeded", Objective: "x",
		})
	}
	// Force many collapsed run nodes.
	m.rebuildTree()
	m.cursor = len(m.tree) - 1
	m.snapCursorToSelectable()
	m.ensureTreeVisible()
	if m.treeOffset+m.sideInnerH <= m.cursor && m.cursor >= m.sideInnerH {
		// cursor should be within [offset, offset+innerH)
		if m.cursor < m.treeOffset || m.cursor >= m.treeOffset+m.sideInnerH {
			t.Fatalf("cursor %d not visible in [%d,%d)", m.cursor, m.treeOffset, m.treeOffset+m.sideInnerH)
		}
	}
}
