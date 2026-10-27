package orchestrator

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/fernandotonon/QtMesh-Agents/internal/scheduler"
)

func TestRunLocalLead(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	run("git", "init")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("git", "add", ".")
	run("git", "commit", "-m", "init")

	task := &scheduler.Task{ID: "lead", Category: "leadership", Description: "lead"}
	err := runLocalLead(context.Background(), PlanRequest{
		Objective: "ship feature X",
		WorkDir:   dir,
	}, task)
	if err != nil {
		t.Fatal(err)
	}
	if task.ResultJSON == "" {
		t.Fatal("expected result json")
	}
	if !isLeadershipTask(task) {
		t.Fatal("expected leadership detection")
	}
}
