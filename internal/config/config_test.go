package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultValidate(t *testing.T) {
	cfg := Default()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default config invalid: %v", err)
	}
}

func TestValidateRejectsBadConcurrency(t *testing.T) {
	cfg := Default()
	cfg.Execution.MaxConcurrentAgents = 0
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	cfg := Default()
	cfg.UI.Theme = "light"
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.UI.Theme != "light" {
		t.Fatalf("theme=%q", got.UI.Theme)
	}
}

func TestLoadMissingReturnsDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.yaml")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agents.Claude.Command != "claude" {
		t.Fatalf("unexpected default: %+v", cfg.Agents.Claude)
	}
}

func TestAgentByID(t *testing.T) {
	cfg := Default()
	e, ok := cfg.AgentByID("claude")
	if !ok || !e.Enabled {
		t.Fatal("claude missing")
	}
	if _, ok := cfg.AgentByID("nope"); ok {
		t.Fatal("expected miss")
	}
}

func TestEnsureDirs(t *testing.T) {
	base := t.TempDir()
	t.Setenv("QTMESH_AGENTS_CONFIG_DIR", filepath.Join(base, "cfg"))
	t.Setenv("QTMESH_AGENTS_DATA_DIR", filepath.Join(base, "data"))
	t.Setenv("QTMESH_AGENTS_RUNTIME_DIR", filepath.Join(base, "run"))
	if err := EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		filepath.Join(base, "cfg"),
		filepath.Join(base, "data"),
		filepath.Join(base, "run"),
		filepath.Join(base, "data", "logs"),
	} {
		if st, err := os.Stat(p); err != nil || !st.IsDir() {
			t.Fatalf("missing dir %s: %v", p, err)
		}
	}
}
