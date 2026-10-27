package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestChatWorkingFeedback(t *testing.T) {
	for _, leader := range []bool{false, true} {
		name, working, reply, placeholder := "orchestrator", "Orchestrator is working…", "orch: finished", "Talk to orchestrator"
		if leader {
			name, working, reply, placeholder = "leader", "Leader is thinking…", "leader: finished", "ask the project leader"
		}
		for _, failed := range []bool{false, true} {
			outcome := "reply"
			status := statusLineMsg("finished")
			if leader {
				status = "leader: finished"
			}
			if failed {
				outcome = "error"
				status = statusLineMsg(name + " error: failed")
				reply = "orch: " + string(status)
			}
			t.Run(name+"/"+outcome, func(t *testing.T) {
				m := New(nil)
				m.ready, m.width, m.height = true, 100, 24
				if leader {
					m.tree = []treeNode{{Kind: nodeBench, Agent: "claude", ProjectID: "project-1"}}
				}
				m.layout()
				m.input.SetValue("please investigate")
				next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
				busy := next.(Model)
				if cmd == nil {
					t.Fatal("missing chat command")
				}
				if busy.orchWorking != working || busy.input.Placeholder != "Working…" {
					t.Fatalf("missing busy state: %q, %q", busy.orchWorking, busy.input.Placeholder)
				}
				if !strings.Contains(busy.View(), working) {
					t.Fatalf("working feedback not visible:\n%s", busy.View())
				}
				if !strings.Contains(strings.Join(busy.orchLog, "\n"), "please investigate") {
					t.Fatal("user turn not echoed")
				}
				// Refresh/layout must retain the feedback while the command is blocked.
				busy.layout()
				if busy.input.Placeholder != "Working…" {
					t.Fatal("layout reset busy placeholder")
				}
				busy.input.SetValue("next question")
				again, extra := busy.Update(tea.KeyMsg{Type: tea.KeyEnter})
				if extra != nil || again.(Model).input.Value() != "next question" {
					t.Fatal("busy submit should preserve draft without starting another request")
				}
				// Unrelated statuses must not clear the pending chat indicator.
				unrelated, _ := busy.Update(statusLineMsg("background status"))
				if unrelated.(Model).orchWorking != working {
					t.Fatal("unrelated status cleared feedback")
				}
				next, _ = busy.Update(chatReplyMsg{status})
				done := next.(Model)
				if done.orchWorking != "" || strings.Contains(done.View(), working) {
					t.Fatal("working feedback remains after completion")
				}
				if !strings.HasPrefix(done.input.Placeholder, placeholder) {
					t.Fatalf("placeholder not restored: %q", done.input.Placeholder)
				}
				if got := done.orchLog[len(done.orchLog)-1]; got != reply {
					t.Fatalf("reply=%q want %q", got, reply)
				}
			})
		}
	}
}

func TestMetaCommandDoesNotShowChatWorking(t *testing.T) {
	m := New(nil)
	m.ready = true
	m.input.SetValue("help")
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil || next.(Model).orchWorking != "" {
		t.Fatal("meta command must not start chat feedback")
	}
}
