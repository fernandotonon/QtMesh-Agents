package scheduler

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunDAGParallelIndependent(t *testing.T) {
	tasks := []Task{
		{ID: "a", Description: "A", Acceptance: []string{"ok"}, Status: StatusPending},
		{ID: "b", Description: "B", Acceptance: []string{"ok"}, Status: StatusPending},
		{ID: "c", Description: "C", Dependencies: []string{"a", "b"}, Acceptance: []string{"ok"}, Status: StatusPending},
	}
	var concurrent int32
	var maxConc int32
	err := RunDAG(context.Background(), tasks, 2, func(ctx context.Context, task *Task) error {
		n := atomic.AddInt32(&concurrent, 1)
		for {
			old := atomic.LoadInt32(&maxConc)
			if n <= old || atomic.CompareAndSwapInt32(&maxConc, old, n) {
				break
			}
		}
		time.Sleep(80 * time.Millisecond)
		atomic.AddInt32(&concurrent, -1)
		task.Status = StatusSucceeded
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if maxConc < 2 {
		t.Fatalf("expected parallel execution, maxConc=%d", maxConc)
	}
	if !Succeeded(tasks) {
		t.Fatalf("tasks=%+v", tasks)
	}
}

func TestRunDAGStopsOnFailure(t *testing.T) {
	tasks := []Task{
		{ID: "a", Description: "A", Acceptance: []string{"ok"}, Status: StatusPending},
		{ID: "b", Description: "B", Dependencies: []string{"a"}, Acceptance: []string{"ok"}, Status: StatusPending},
	}
	err := RunDAG(context.Background(), tasks, 1, func(ctx context.Context, task *Task) error {
		if task.ID == "a" {
			return fmt.Errorf("boom")
		}
		t.Fatal("b should not run")
		return nil
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if tasks[0].Status != StatusFailed {
		t.Fatalf("a status=%s", tasks[0].Status)
	}
	if tasks[1].Status != StatusBlocked {
		t.Fatalf("b status=%s", tasks[1].Status)
	}
}
