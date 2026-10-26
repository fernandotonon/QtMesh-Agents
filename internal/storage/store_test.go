package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestProjectAndSessionRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	p := Project{ID: "qtmesheditor", Name: "QtMeshEditor", Path: "/tmp/qte", Active: true}
	if err := store.UpsertProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetProject(ctx, "qtmesheditor")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "QtMeshEditor" {
		t.Fatalf("%+v", got)
	}

	rec := SessionRecord{
		ID: "claude-1", AgentID: "claude", ProjectID: "qtmesheditor",
		Status: "running", PID: 123, CreatedAt: time.Now().UTC(),
	}
	if err := store.UpsertSession(ctx, rec); err != nil {
		t.Fatal(err)
	}
	list, err := store.ListSessions(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("list=%v err=%v", list, err)
	}
}
