package cursor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/fernandotonon/QtMesh-Agents/internal/agents"
)

func TestExecuteTrustsWorktree(t *testing.T) {
	workdir := filepath.Join(t.TempDir(), ".qma-worktrees", "worker")
	if err := os.MkdirAll(workdir, 0700); err != nil {
		t.Fatal(err)
	}
	adapter := New(os.Args[0], []string{"-test.run=^TestCursorProcess$", "--", "--model", "configured-model"})
	result, err := adapter.Execute(context.Background(), agents.AgentTask{
		WorkDir: workdir, Prompt: "implement routing", ExtraArgs: []string{"--output-format", "json"},
		Env: map[string]string{"QMA_CURSOR_TEST_PROCESS": "1"},
	})
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("execution: %v, %+v", err, result)
	}
	var got struct {
		Args []string
		Dir  string
	}
	if err := json.Unmarshal([]byte(result.Output), &got); err != nil {
		t.Fatalf("output %q: %v", result.Output, err)
	}
	want := []string{"--model", "configured-model", "--trust", "-p", "implement routing", "--output-format", "json"}
	if !reflect.DeepEqual(got.Args, want) {
		t.Fatalf("args = %q, want %q", got.Args, want)
	}
	actual, err := filepath.EvalSymlinks(workdir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Dir != actual {
		t.Fatalf("dir = %q, want %q", got.Dir, actual)
	}
}

func TestCursorProcess(t *testing.T) {
	if os.Getenv("QMA_CURSOR_TEST_PROCESS") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg != "--" {
			continue
		}
		dir, _ := os.Getwd()
		data, _ := json.Marshal(struct {
			Args []string
			Dir  string
		}{os.Args[i+1:], dir})
		fmt.Print(string(data))
		os.Exit(0)
	}
	os.Exit(1)
}
