package cli

import (
	"bufio"
	"bytes"
	"github.com/fernandotonon/QtMesh-Agents/internal/config"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestSetupConfirmation(t *testing.T) {
	for _, tc := range []struct {
		answer string
		want   bool
	}{{"y\n", true}, {"YES\n", true}, {"\n", false}, {"no\n", false}, {"", false}} {
		cmd := &cobra.Command{}
		var out bytes.Buffer
		cmd.SetOut(&out)
		if got := confirmSetup(cmd, bufio.NewReader(strings.NewReader(tc.answer)), "Register? "); got != tc.want {
			t.Errorf("%q: got %v", tc.answer, got)
		}
		if out.String() != "Register? " {
			t.Fatalf("missing prompt: %q", out.String())
		}
	}
	if isTerminalInput(strings.NewReader("yes\n")) {
		t.Fatal("piped input must not trigger wizard")
	}
}

func TestInitNonInteractivePreservesConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("QTMESH_AGENTS_CONFIG_DIR", filepath.Join(dir, "config"))
	t.Setenv("QTMESH_AGENTS_DATA_DIR", filepath.Join(dir, "data"))
	t.Setenv("QTMESH_AGENTS_RUNTIME_DIR", filepath.Join(dir, "run"))
	path, err := config.Path()
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Execution.MaxConcurrentAgents = 7
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	cmd := newInitCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetIn(strings.NewReader("yes\n"))
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "Register current directory") {
		t.Fatal("non-TTY input prompted")
	}
	for _, want := range []string{"detected agents:", "Next steps:", "qma chat"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in %s", want, out.String())
		}
	}
	got, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Execution.MaxConcurrentAgents != 7 {
		t.Fatal("init overwrote existing config")
	}
}
