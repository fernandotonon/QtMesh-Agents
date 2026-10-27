package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/fernandotonon/QtMesh-Agents/internal/scheduler"
)

// runLocalLead produces a fast, deterministic leadership brief without calling a vendor CLI.
// This keeps benches moving instead of stalling on a long Claude headless call.
func runLocalLead(ctx context.Context, req PlanRequest, task *scheduler.Task) error {
	_ = ctx
	workDir := req.WorkDir
	if workDir == "" {
		workDir = "."
	}

	var b strings.Builder
	b.WriteString("# Project leader brief\n\n")
	b.WriteString("Objective: " + req.Objective + "\n\n")
	b.WriteString("## Priorities\n")
	b.WriteString("1. Inspect current repo state (status, recent commits, failing tests).\n")
	b.WriteString("2. Implement the highest-priority unfinished piece in isolation.\n")
	b.WriteString("3. Verify with build/tests; summarize what remains.\n")
	b.WriteString("4. Do not merge protected branches.\n\n")

	b.WriteString("## Repo snapshot\n")
	b.WriteString("- workdir: " + workDir + "\n")
	if out, err := exec.Command("git", "-C", workDir, "status", "--short").CombinedOutput(); err == nil {
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		if len(lines) == 1 && lines[0] == "" {
			b.WriteString("- git status: clean\n")
		} else {
			b.WriteString(fmt.Sprintf("- git status: %d dirty paths\n", len(lines)))
			for i, line := range lines {
				if i >= 12 {
					b.WriteString("  …\n")
					break
				}
				b.WriteString("  " + line + "\n")
			}
		}
	}
	if out, err := exec.Command("git", "-C", workDir, "log", "-5", "--oneline").CombinedOutput(); err == nil {
		b.WriteString("- recent commits:\n")
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if line == "" {
				continue
			}
			b.WriteString("  " + line + "\n")
		}
	}
	// Mention known unfinished areas if present in the tree.
	hints := []string{
		"internal/tui", "internal/orchestrator", "internal/storage",
		"internal/agents/codex", "docs/architecture.md",
	}
	b.WriteString("\n## Likely focus areas\n")
	for _, h := range hints {
		if _, err := os.Stat(filepath.Join(workDir, h)); err == nil {
			b.WriteString("- " + h + "\n")
		}
	}
	b.WriteString("\n## Worker guidance\n")
	b.WriteString("- discover: clarify scope from the objective + snapshot above.\n")
	b.WriteString("- impl: one cohesive change set with tests.\n")
	b.WriteString("- verify: go test ./... and go build ./...\n")
	b.WriteString("- integrate: summarize diffs; do not merge.\n")

	result := map[string]any{
		"leader":    "local",
		"brief":     b.String(),
		"generated": time.Now().UTC().Format(time.RFC3339),
	}
	raw, _ := json.Marshal(result)
	task.ResultJSON = string(raw)
	task.Error = ""
	return nil
}

func isLeadershipTask(task *scheduler.Task) bool {
	if task == nil {
		return false
	}
	if task.Category == "leadership" {
		return true
	}
	id := strings.ToLower(task.ID)
	return strings.HasSuffix(id, "-lead") || id == "lead" || strings.HasSuffix(id, "lead")
}
