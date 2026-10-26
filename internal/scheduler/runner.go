package scheduler

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// TaskFunc executes a single task; it should update task.Status and related fields.
type TaskFunc func(ctx context.Context, task *Task) error

// RunDAG executes tasks respecting dependencies with bounded concurrency.
// It mutates the provided task slice in place.
func RunDAG(ctx context.Context, tasks []Task, maxConcurrent int, fn TaskFunc) error {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	byID := map[string]*Task{}
	for i := range tasks {
		byID[tasks[i].ID] = &tasks[i]
		if tasks[i].Status == "" {
			tasks[i].Status = StatusPending
		}
	}

	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		sem      = make(chan struct{}, maxConcurrent)
		firstErr error
		running  = map[string]bool{}
	)

	for {
		if ctx.Err() != nil {
			mu.Lock()
			for i := range tasks {
				switch tasks[i].Status {
				case StatusPending, StatusReady, StatusRunning:
					tasks[i].Status = StatusCancelled
				}
			}
			mu.Unlock()
			wg.Wait()
			return ctx.Err()
		}

		mu.Lock()
		snapshot := make([]Task, len(tasks))
		copy(snapshot, tasks)
		inflight := len(running)
		mu.Unlock()

		if AllTerminal(snapshot) {
			wg.Wait()
			mu.Lock()
			err := firstErr
			mu.Unlock()
			if !Succeeded(snapshot) && err == nil {
				err = fmt.Errorf("orchestration incomplete")
			}
			return err
		}

		ready := ReadyTasks(snapshot)
		started := 0
		for _, r := range ready {
			mu.Lock()
			t := byID[r.ID]
			if running[t.ID] || t.Status == StatusRunning || t.Status == StatusSucceeded || t.Status == StatusFailed {
				mu.Unlock()
				continue
			}
			t.Status = StatusRunning
			running[t.ID] = true
			taskPtr := t
			mu.Unlock()

			started++
			wg.Add(1)
			go func(tp *Task) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()

				err := fn(ctx, tp)

				mu.Lock()
				delete(running, tp.ID)
				if err != nil {
					if tp.Status != StatusSucceeded {
						tp.Status = StatusFailed
						if tp.Error == "" {
							tp.Error = err.Error()
						}
					}
					if firstErr == nil {
						firstErr = err
					}
				} else if tp.Status == StatusRunning || tp.Status == "" {
					tp.Status = StatusSucceeded
				}
				mu.Unlock()
			}(taskPtr)
		}

		if started == 0 && inflight == 0 {
			mu.Lock()
			// Recheck under lock to avoid a scheduling race.
			snap := make([]Task, len(tasks))
			copy(snap, tasks)
			if len(ReadyTasks(snap)) > 0 {
				mu.Unlock()
				continue
			}
			blocked := false
			failed := false
			for i := range tasks {
				switch tasks[i].Status {
				case StatusFailed:
					failed = true
				case StatusPending, StatusReady:
					tasks[i].Status = StatusBlocked
					blocked = true
				}
			}
			err := firstErr
			mu.Unlock()
			wg.Wait()
			if blocked || failed {
				if err == nil {
					err = fmt.Errorf("tasks blocked by failed dependencies")
				}
				return err
			}
			return err
		}

		select {
		case <-ctx.Done():
		case <-time.After(20 * time.Millisecond):
		}
	}
}
