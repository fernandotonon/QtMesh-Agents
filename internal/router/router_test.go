package router

import (
	"testing"

	"github.com/fernandotonon/QtMesh-Agents/internal/config"
)

func TestSelectPreferred(t *testing.T) {
	cfg := config.Default().Routing
	r := New(cfg, map[string]bool{"claude": true, "codex": true, "cursor": true})
	for _, tc := range []struct {
		category string
		want     string
	}{
		{"architecture", "claude"},
		{"implementation", "cursor"},
		{"frontend", "codex"},
		{"testing", "codex"},
		{"review", "claude"},
	} {
		t.Run(tc.category, func(t *testing.T) {
			sel, err := r.Select(tc.category)
			if err != nil {
				t.Fatal(err)
			}
			if sel.AgentID != tc.want {
				t.Fatalf("got %s, want %s", sel.AgentID, tc.want)
			}
		})
	}
}

func TestSelectFallback(t *testing.T) {
	cfg := config.Default().Routing
	r := New(cfg, map[string]bool{"claude": false, "codex": true, "cursor": false})
	sel, err := r.Select("architecture")
	if err != nil {
		t.Fatal(err)
	}
	if sel.AgentID != "codex" {
		t.Fatalf("got %s", sel.AgentID)
	}
}

func TestSelectNone(t *testing.T) {
	cfg := config.Default().Routing
	r := New(cfg, map[string]bool{})
	if _, err := r.Select("testing"); err == nil {
		t.Fatal("expected error")
	}
}
