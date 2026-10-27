package mock

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/fernandotonon/QtMesh-Agents/internal/agents"
)

// Adapter is a test/CI adapter that uses a tiny shell script or printf.
type Adapter struct {
	agents.BaseAdapter
	scriptPath string
}

// New creates a mock adapter. If command is empty, a temporary echo script is used.
func New(command string, args []string) *Adapter {
	if command == "" {
		command = "sh"
	}
	return &Adapter{BaseAdapter: agents.NewBaseAdapter("mock", "QtMesh", command, args)}
}

// NewWithScript creates a mock adapter backed by an executable script path.
func NewWithScript(scriptPath string) *Adapter {
	return &Adapter{
		BaseAdapter: agents.NewBaseAdapter("mock", "QtMesh", scriptPath, nil),
		scriptPath:  scriptPath,
	}
}

// WriteFixtureScript writes a portable mock agent script and returns its path.
func WriteFixtureScript(dir string) (string, error) {
	path := filepath.Join(dir, "qma-mock-agent")
	if runtime.GOOS == "windows" {
		path += ".bat"
		content := "@echo off\necho mock-agent ready\necho MOCK_PROMPT=%*\ntimeout /t 1 >nul\n"
		if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
			return "", err
		}
		return path, nil
	}
	content := `#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "mock-agent 0.1.0"
  exit 0
fi
echo "mock-agent ready"
# Stay alive for interactive attach/detach tests; echo lines until timeout.
i=0
while [ "$i" -lt 60 ]; do
  printf "mock> "
  if IFS= read -r -t 1 line; then
    echo "echo: $line"
    if [ "$line" = "exit" ]; then
      break
    fi
  fi
  i=$((i+1))
done
echo "mock-agent done"
`
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		return "", err
	}
	return path, nil
}

func (a *Adapter) Capabilities(ctx context.Context) (agents.AgentCapabilities, error) {
	return agents.AgentCapabilities{
		Interactive:      true,
		Headless:         true,
		SessionRestore:   false,
		StructuredOutput: false,
		UsageReporting:   false,
		Version:          "0.1.0",
	}, nil
}

func (a *Adapter) Detect(ctx context.Context) (bool, error) {
	if a.scriptPath != "" {
		_, err := os.Stat(a.scriptPath)
		return err == nil, nil
	}
	// Headless Execute is in-process — always available for --agent mock / CI,
	// even when the optional qma-mock-agent CLI is not on PATH.
	return true, nil
}

func (a *Adapter) StartSession(ctx context.Context, opts agents.SessionOptions) (agents.AgentSession, error) {
	cmdName := a.Command()
	args := append([]string{}, a.BaseAdapter.Args()...)
	if a.scriptPath != "" {
		cmdName = a.scriptPath
		args = nil
	} else if cmdName == "sh" && len(args) == 0 {
		// Fallback: inline shell that behaves like a trivial REPL.
		args = []string{"-c", `echo "mock-agent ready"; printf "mock> "; read line; echo "echo: $line"; echo "mock-agent done"`}
	}
	cmd := exec.CommandContext(ctx, cmdName, args...)
	if opts.WorkDir != "" {
		cmd.Dir = opts.WorkDir
	}
	cmd.Env = os.Environ()
	for k, v := range opts.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	// Reuse PTY via a temporary BaseAdapter with resolved command.
	tmp := agents.NewBaseAdapter("mock", "QtMesh", cmdName, args)
	return tmp.StartPTY(ctx, opts)
}

func (a *Adapter) Execute(ctx context.Context, task agents.AgentTask) (agents.Execution, error) {
	started := time.Now()
	out := fmt.Sprintf("mock executed: %s", strings.TrimSpace(task.Prompt))
	// Isolated worktrees: leave a tiny deterministic diff for integrate/review e2e.
	if task.WorkDir != "" && strings.Contains(task.WorkDir, ".qma-worktrees") {
		readme := filepath.Join(task.WorkDir, "README.md")
		if b, err := os.ReadFile(readme); err == nil {
			if !strings.Contains(string(b), "qma-mock") {
				_ = os.WriteFile(readme, append(b, []byte("\n<!-- qma-mock -->\n")...), 0o644)
				out += "\nwrote README.md comment"
			}
		} else {
			path := filepath.Join(task.WorkDir, ".qma-mock-change")
			_ = os.WriteFile(path, []byte("mock:"+task.ID+"\n"), 0o644)
			out += "\nwrote .qma-mock-change"
		}
	}
	return agents.Execution{
		ID:        task.ID,
		AgentID:   "mock",
		ExitCode:  0,
		Output:    out,
		StartedAt: started,
		EndedAt:   time.Now(),
	}, nil
}

