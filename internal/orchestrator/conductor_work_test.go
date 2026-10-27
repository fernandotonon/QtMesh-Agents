package orchestrator

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fernandotonon/QtMesh-Agents/internal/config"
	"github.com/fernandotonon/QtMesh-Agents/internal/storage"
)

func TestWorkSummaryIntentReturnsGitBriefing(t *testing.T) {
	base := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = base
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run("init")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(base, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "README.md")
	run("commit", "-m", "init")
	if err := os.WriteFile(filepath.Join(base, "README.md"), []byte("hello\nworld\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	store, err := storage.Open(filepath.Join(base, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	eng := New(Deps{Config: config.Default(), Store: store, Planner: HeuristicPlanner{}})
	c := NewConductor(eng, memProjects{list: []storage.Project{
		{ID: "demo", Name: "Demo", Path: base},
	}}, nil)

	res, err := c.Chat(context.Background(), "what improved? which improvements were implemented?")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Actions) != 0 {
		t.Fatalf("should not start runs: %+v", res.Actions)
	}
	low := strings.ToLower(res.Reply)
	if !strings.Contains(low, "live work briefing") && !strings.Contains(low, "git") {
		t.Fatalf("expected live briefing, got:\n%s", res.Reply)
	}
	if !strings.Contains(res.Reply, "README.md") {
		t.Fatalf("expected dirty README in briefing:\n%s", res.Reply)
	}
}

func TestIsWorkSummaryAndLeaderQueries(t *testing.T) {
	if !isWorkSummaryQuery("did it improve the project? which were the improvements implemented?") {
		t.Fatal("expected work summary")
	}
	if !isLeaderConsultQuery("ask the leader what we should prioritize") {
		t.Fatal("expected leader consult")
	}
	if !isJudgmentQuery("what should we do next?") {
		t.Fatal("expected judgment")
	}
	if !isOutcomeQuery("did it work?") {
		t.Fatal("expected outcome query")
	}
}

func TestDidItWorkNotYoureWelcome(t *testing.T) {
	base := t.TempDir()
	store, err := storage.Open(filepath.Join(base, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	eng := New(Deps{Config: config.Default(), Store: store, Planner: HeuristicPlanner{}})
	c := NewConductor(eng, memProjects{list: []storage.Project{
		{ID: "demo", Name: "Demo", Path: base},
	}}, nil)
	res, err := c.Chat(context.Background(), "did it work?")
	if err != nil {
		t.Fatal(err)
	}
	low := strings.ToLower(res.Reply)
	if strings.Contains(low, "you're welcome") || strings.Contains(low, "youre welcome") {
		t.Fatalf("must not treat outcome as thanks: %s", res.Reply)
	}
	if !strings.Contains(low, "verdict") && !strings.Contains(low, "latest run") && !strings.Contains(low, "no runs") {
		t.Fatalf("expected outcome verdict, got:\n%s", res.Reply)
	}
}
