package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestModelQuitOnQ(t *testing.T) {
	m := New(nil)
	m.ready = true
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if cmd == nil {
		t.Fatal("expected quit cmd")
	}
}

func TestModelTabSwitchesMode(t *testing.T) {
	m := New(nil)
	m.ready = true
	m.mode = modeOrchestrator
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	got := next.(Model)
	if got.mode != modeAgent {
		t.Fatalf("mode=%v", got.mode)
	}
}
