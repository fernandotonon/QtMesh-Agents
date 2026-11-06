package orchestrator

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fernandotonon/QtMesh-Agents/internal/scheduler"
	"github.com/fernandotonon/QtMesh-Agents/internal/storage"
	"github.com/fernandotonon/QtMesh-Agents/internal/worktree"
)

func gitReliability(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}

func reliabilityRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitReliability(t, dir, "init")
	gitReliability(t, dir, "config", "user.name", "Test")
	gitReliability(t, dir, "config", "user.email", "test@example.com")
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("original\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitReliability(t, dir, "add", ".")
	gitReliability(t, dir, "commit", "-m", "initial")
	return dir
}

func TestConflictingIntegrationPreservesAllFilesAndIndex(t *testing.T) {
	dir := reliabilityRepo(t)
	for _, name := range []string{"a.txt", "b.txt"} {
		os.WriteFile(filepath.Join(dir, name), []byte("agent\n"), 0o644)
	}
	patch := gitReliability(t, dir, "diff")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("original\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("user\n"), 0o644)
	before := gitReliability(t, dir, "status", "--porcelain")
	if err := applyUnifiedDiff(dir, patch); err == nil {
		t.Fatal("conflict accepted")
	}
	for name, want := range map[string]string{"a.txt": "original\n", "b.txt": "user\n"} {
		got, _ := os.ReadFile(filepath.Join(dir, name))
		if string(got) != want {
			t.Fatalf("%s overwritten: %q", name, got)
		}
	}
	if after := gitReliability(t, dir, "status", "--porcelain"); after != before {
		t.Fatalf("index/status changed: %s", after)
	}
}

func TestVerificationWorkspaceContainsImplementation(t *testing.T) {
	ctx := context.Background()
	dir := reliabilityRepo(t)
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	mgr := worktree.New(dir, filepath.Join(dir, ".qma-worktrees"))
	impl, err := mgr.Create(ctx, "impl", "qma/impl")
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(impl, "a.txt"), []byte("implementation\n"), 0o644)
	patch, err := mgr.Diff(ctx, impl)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "impl.diff")
	os.WriteFile(path, []byte(patch), 0o600)
	now := time.Now().UTC()
	if err := store.UpsertRun(ctx, storage.Run{ID: "run", Objective: "test", Status: "running", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertTask(ctx, storage.TaskRecord{ID: "impl", RunID: "run", Status: "succeeded", WorktreePath: impl, DiffPath: path, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	verify, err := mgr.Create(ctx, "verify", "qma/verify")
	if err != nil {
		t.Fatal(err)
	}
	e := &Executor{Store: store}
	if err := e.prepareDependencies(ctx, "run", &scheduler.Task{Dependencies: []string{"impl"}}, verify); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(verify, "a.txt"))
	if string(got) != "implementation\n" {
		t.Fatalf("verification saw %q", got)
	}
	records := []storage.TaskRecord{
		{ID: "impl", WorktreePath: impl, DiffPath: path},
		{ID: "verify", WorktreePath: verify, Dependencies: []string{"impl"}},
	}
	frontier := artifactFrontier(records)
	if len(frontier) != 1 || frontier[0].ID != "verify" {
		t.Fatalf("duplicate ancestor artifacts: %+v", frontier)
	}
}

func TestStreamedResultsKeepExplicitHumanControls(t *testing.T) {
	previous := `{"human_reply":"use local fixture","elevate_sandbox":true,"interactive_approvals":true}`
	got := preserveTaskControls(`{"output":"working"}`, previous)
	elevate, interactive := permissionFlagsFromResult(got)
	if !elevate || !interactive || humanReplyFromResult(got) != "use local fixture" || !strings.Contains(got, "working") {
		t.Fatalf("lost controls: %s", got)
	}
}

func TestNegativeMergeReplyCannotApprove(t *testing.T) {
	for _, reply := range []string{"do not approve apply to main", "reject, do not apply to main working tree", "don't approve merge"} {
		if isMergeApproveReply(reply) {
			t.Fatalf("negative reply approved: %q", reply)
		}
	}
}

func TestChangedArtifactInvalidatesMergeApproval(t *testing.T) {
	ctx := context.Background()
	dir := reliabilityRepo(t)
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("agent\n"), 0o644)
	patch := gitReliability(t, dir, "diff")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("original\n"), 0o644)
	path := filepath.Join(t.TempDir(), "patch.diff")
	os.WriteFile(path, []byte(patch), 0o600)
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	if err := store.UpsertRun(ctx, storage.Run{ID: "run", Objective: "test", Status: "running", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertTask(ctx, storage.TaskRecord{ID: "impl", RunID: "run", Status: "succeeded", DiffPath: path, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	e := &Executor{Store: store}
	approved, _, err := e.collectRunDiff(ctx, "run", PlanRequest{WorkDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	task := &scheduler.Task{ID: "integrate", ResultJSON: fmt.Sprintf(`{"patch_digest":%q}`, patchDigest(approved))}
	// Simulate an artifact changing between preparation and approval.
	os.WriteFile(path, []byte(strings.ReplaceAll(patch, "+agent", "+changed")), 0o600)
	if err := e.applyMergeToMain(ctx, "run", PlanRequest{WorkDir: dir}, task); err == nil {
		t.Fatal("changed artifact accepted")
	}
	got, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
	if string(got) != "original\n" {
		t.Fatalf("project modified: %q", got)
	}
}
