package daemon_test

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fernandotonon/QtMesh-Agents/internal/agents/mock"
	"github.com/fernandotonon/QtMesh-Agents/internal/config"
	"github.com/fernandotonon/QtMesh-Agents/internal/daemon"
	"github.com/fernandotonon/QtMesh-Agents/internal/ipc"
)

func TestDaemonSpawnMockSession(t *testing.T) {
	base := t.TempDir()
	t.Setenv("QTMESH_AGENTS_CONFIG_DIR", filepath.Join(base, "cfg"))
	t.Setenv("QTMESH_AGENTS_DATA_DIR", filepath.Join(base, "data"))
	t.Setenv("QTMESH_AGENTS_RUNTIME_DIR", filepath.Join(base, "run"))

	script, err := mock.WriteFixtureScript(base)
	if err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.Agents.Mock.Command = script
	cfg.Storage.Path = filepath.Join(base, "data", "test.db")
	// Keep socket path short — macOS sun_path is ~104 bytes.
	socket := filepath.Join(os.TempDir(), fmt.Sprintf("qma-%d.sock", time.Now().UnixNano()))
	t.Cleanup(func() { _ = os.Remove(socket) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	d, err := daemon.Start(ctx, daemon.Options{
		Config:     cfg,
		SocketPath: socket,
		Logger:     slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Stop()

	deadline := time.Now().Add(3 * time.Second)
	for !ipc.IsDaemonAlive(socket) {
		if time.Now().After(deadline) {
			t.Fatal("daemon not alive")
		}
		time.Sleep(20 * time.Millisecond)
	}

	client, err := ipc.Connect(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	if err := client.Ping(); err != nil {
		t.Fatal(err)
	}
	st, err := client.Status()
	if err != nil || !st.DaemonRunning {
		t.Fatalf("status=%+v err=%v", st, err)
	}

	resp, err := client.Spawn(ipc.SpawnRequest{AgentID: "mock", Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	if resp.SessionID == "" || resp.PID == 0 {
		t.Fatalf("%+v", resp)
	}

	sessions, err := client.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range sessions {
		if s.ID == resp.SessionID && s.Status == "running" {
			found = true
		}
	}
	if !found {
		t.Fatalf("session not listed: %+v", sessions)
	}

	if err := client.Attach(resp.SessionID); err != nil {
		t.Fatal(err)
	}
	_ = client.InputFire(resp.SessionID, []byte("hello\n"))

	// Detach should not kill the session.
	if err := client.Detach(); err != nil {
		t.Fatal(err)
	}
	sessions, err = client.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	alive := false
	for _, s := range sessions {
		if s.ID == resp.SessionID && s.Status == "running" {
			alive = true
		}
	}
	if !alive {
		t.Fatal("session died after detach")
	}

	if err := client.Stop(resp.SessionID); err != nil {
		t.Fatal(err)
	}
}
