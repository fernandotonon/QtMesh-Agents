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

func TestRunDAGAwaitingInputKeepsDownstreamPending(t *testing.T) {
	tasks := []Task{
		{ID: "impl", Description: "impl", Acceptance: []string{"ok"}, Status: StatusPending},
		{ID: "verify", Description: "verify", Dependencies: []string{"impl"}, Acceptance: []string{"ok"}, Status: StatusPending},
		{ID: "integrate", Description: "integrate", Dependencies: []string{"verify"}, Acceptance: []string{"ok"}, Status: StatusPending},
	}
	err := RunDAG(context.Background(), tasks, 1, func(ctx context.Context, task *Task) error {
		if task.ID == "impl" {
			task.Status = StatusAwaitingInput
			return fmt.Errorf("%w: needs graphics", ErrNeedsAttention)
		}
		t.Fatalf("%s should not run while impl awaits input", task.ID)
		return nil
	})
	if err != nil {
		t.Fatalf("awaiting_input must soft-pause without error, got %v", err)
	}
	if tasks[0].Status != StatusAwaitingInput {
		t.Fatalf("impl=%s", tasks[0].Status)
	}
	if tasks[1].Status != StatusPending {
		t.Fatalf("verify must stay pending (not blocked), got %s", tasks[1].Status)
	}
	if tasks[2].Status != StatusPending {
		t.Fatalf("integrate must stay pending (not blocked), got %s", tasks[2].Status)
	}
}

func TestCancellationDoesNotLaunchQueuedTasks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tasks := []Task{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	var calls int
	err := RunDAG(ctx, tasks, 1, func(ctx context.Context, task *Task) error {
		calls++
		cancel()
		<-ctx.Done()
		return ctx.Err()
	})
	if err != context.Canceled || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
	for _, task := range tasks {
		if task.Status != StatusCancelled {
			t.Fatalf("%s: %s", task.ID, task.Status)
		}
	}
}

func TestRunDAGRejectsMalformedDependencies(t *testing.T) {
	for _, tasks := range [][]Task{
		{{ID: "a"}, {ID: "a"}},
		{{ID: "a", Dependencies: []string{"missing"}}},
		{{ID: "a", Dependencies: []string{"b"}}, {ID: "b", Dependencies: []string{"a"}}},
	} {
		if err := RunDAG(context.Background(), tasks, 2, func(context.Context, *Task) error { t.Fatal("invalid plan executed"); return nil }); err == nil {
			t.Fatal("invalid plan accepted")
		}
	}
}
