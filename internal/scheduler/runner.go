package scheduler

import (
	"context"
	"errors"
	"fmt"
)

// ErrNeedsAttention signals the task paused for human input (permissions, approval).
var ErrNeedsAttention = errors.New("needs attention")

// TaskFunc executes a single task; it should update task.Status and related fields.
type TaskFunc func(ctx context.Context, task *Task) error

// RunDAG executes tasks respecting dependencies with bounded concurrency.
// It mutates the provided task slice in place.
func RunDAG(ctx context.Context, tasks []Task, maxConcurrent int, fn TaskFunc) error {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	// Workers own private task copies. Only this event loop mutates the DAG.
	byID := map[string]int{}
	for i := range tasks {
		if tasks[i].ID == "" {
			return fmt.Errorf("empty task id")
		}
		if _, exists := byID[tasks[i].ID]; exists {
			return fmt.Errorf("duplicate task id %q", tasks[i].ID)
		}
		byID[tasks[i].ID] = i
		if tasks[i].Status == "" {
			tasks[i].Status = StatusPending
		}
	}
	for _, t := range tasks {
		for _, dep := range t.Dependencies {
			if _, ok := byID[dep]; !ok {
				return fmt.Errorf("unknown dependency %q", dep)
			}
		}
	}
	if err := detectCycles(tasks); err != nil {
		return err
	}
	type completion struct {
		task Task
		err  error
	}
	done := make(chan completion, maxConcurrent)
	running := 0
	var firstErr error
	cancelled := false
	for {
		if ctx.Err() != nil {
			cancelled = true
		}
		if !cancelled {
			for _, ready := range ReadyTasks(tasks) {
				if running >= maxConcurrent {
					break
				}
				i := byID[ready.ID]
				tasks[i].Status = StatusRunning
				worker := tasks[i]
				worker.Dependencies = append([]string(nil), worker.Dependencies...)
				worker.Acceptance = append([]string(nil), worker.Acceptance...)
				running++
				go func(t Task) { err := fn(ctx, &t); done <- completion{t, err} }(worker)
			}
		}
		if running == 0 {
			if cancelled {
				for i := range tasks {
					if tasks[i].Status == StatusPending || tasks[i].Status == StatusReady {
						tasks[i].Status = StatusCancelled
					}
				}
				return ctx.Err()
			}
			if NeedsAttention(tasks) {
				return firstErr
			}
			if Succeeded(tasks) {
				return nil
			}
			for i := range tasks {
				if tasks[i].Status == StatusPending || tasks[i].Status == StatusReady {
					tasks[i].Status = StatusBlocked
				}
			}
			if firstErr != nil {
				return firstErr
			}
			return fmt.Errorf("orchestration incomplete")
		}
		var result completion
		if cancelled {
			result = <-done
		} else {
			select {
			case result = <-done:
			case <-ctx.Done():
				cancelled = true
				continue
			}
		}
		running--
		t := result.task
		if ctx.Err() != nil {
			t.Status = StatusCancelled
			t.Error = ctx.Err().Error()
		} else if errors.Is(result.err, ErrNeedsAttention) || t.Status == StatusAwaitingInput {
			t.Status = StatusAwaitingInput
			if result.err != nil && t.Error == "" {
				t.Error = result.err.Error()
			}
		} else if result.err != nil {
			t.Status = StatusFailed
			if t.Error == "" {
				t.Error = result.err.Error()
			}
			if firstErr == nil {
				firstErr = result.err
			}
		} else if t.Status == StatusRunning || t.Status == "" {
			t.Status = StatusSucceeded
		}
		tasks[byID[t.ID]] = t
	}
}
