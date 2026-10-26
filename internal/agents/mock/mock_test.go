package mock

import (
	"context"
	"testing"
	"time"

	"github.com/fernandotonon/QtMesh-Agents/internal/agents"
)

func TestMockExecute(t *testing.T) {
	a := New("", nil)
	exec, err := a.Execute(context.Background(), agents.AgentTask{
		ID: "t1", Prompt: "hello",
	})
	if err != nil {
		t.Fatal(err)
	}
	if exec.ExitCode != 0 || exec.Output == "" {
		t.Fatalf("%+v", exec)
	}
}

func TestMockSession(t *testing.T) {
	dir := t.TempDir()
	script, err := WriteFixtureScript(dir)
	if err != nil {
		t.Fatal(err)
	}
	a := NewWithScript(script)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ok, err := a.Detect(ctx)
	if err != nil || !ok {
		t.Fatalf("detect: %v %v", ok, err)
	}

	sess, err := a.StartSession(ctx, agents.SessionOptions{
		SessionID: "mock-1", Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Kill() }()

	// Give it a moment then send input.
	time.Sleep(100 * time.Millisecond)
	_, _ = sess.Write([]byte("hi\n"))

	select {
	case <-sess.Done():
	case <-time.After(3 * time.Second):
		_ = sess.Kill()
	}
}
