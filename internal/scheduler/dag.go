package scheduler

import (
	"fmt"
)

// Task is a unit of orchestrated work.
type Task struct {
	ID            string
	Description   string
	Category      string
	Dependencies  []string
	AssignedAgent string
	Status        TaskStatus
	Acceptance    []string
	MaxRetries    int
	RetryCount    int
	ProjectID     string
	RunID         string
	WorktreePath  string
	Branch        string
	Error         string
	DiffPath      string
	ResultJSON    string
}

type TaskStatus string

const (
	StatusPending       TaskStatus = "pending"
	StatusReady         TaskStatus = "ready"
	StatusRunning       TaskStatus = "running"
	StatusSucceeded     TaskStatus = "succeeded"
	StatusFailed        TaskStatus = "failed"
	StatusBlocked       TaskStatus = "blocked"
	StatusCancelled     TaskStatus = "cancelled"
	StatusAwaitingInput TaskStatus = "awaiting_input" // needs human attention
)

// Plan is a validated DAG of tasks.
type Plan struct {
	Tasks []Task
}

// Validate checks uniqueness, dependency references, and cycles.
func (p Plan) Validate() error {
	ids := map[string]*Task{}
	for i := range p.Tasks {
		t := &p.Tasks[i]
		if t.ID == "" {
			return fmt.Errorf("task at index %d has empty id", i)
		}
		if _, ok := ids[t.ID]; ok {
			return fmt.Errorf("duplicate task id %q", t.ID)
		}
		if t.Description == "" {
			return fmt.Errorf("task %q missing description", t.ID)
		}
		if len(t.Acceptance) == 0 {
			return fmt.Errorf("task %q missing acceptance criteria", t.ID)
		}
		ids[t.ID] = t
	}
	for _, t := range p.Tasks {
		for _, dep := range t.Dependencies {
			if _, ok := ids[dep]; !ok {
				return fmt.Errorf("task %q depends on unknown %q", t.ID, dep)
			}
		}
	}
	if err := detectCycles(p.Tasks); err != nil {
		return err
	}
	return nil
}

func detectCycles(tasks []Task) error {
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := map[string]int{}
	deps := map[string][]string{}
	for _, t := range tasks {
		color[t.ID] = white
		deps[t.ID] = t.Dependencies
	}
	var visit func(string) error
	visit = func(id string) error {
		color[id] = gray
		for _, d := range deps[id] {
			switch color[d] {
			case gray:
				return fmt.Errorf("dependency cycle involving %q -> %q", id, d)
			case white:
				if err := visit(d); err != nil {
					return err
				}
			}
		}
		color[id] = black
		return nil
	}
	for id := range color {
		if color[id] == white {
			if err := visit(id); err != nil {
				return err
			}
		}
	}
	return nil
}

// ReadyTasks returns tasks whose dependencies are all succeeded.
func ReadyTasks(tasks []Task) []Task {
	status := map[string]TaskStatus{}
	for _, t := range tasks {
		status[t.ID] = t.Status
	}
	var ready []Task
	for _, t := range tasks {
		if t.Status != StatusPending && t.Status != StatusReady && t.Status != "" {
			continue
		}
		ok := true
		for _, d := range t.Dependencies {
			if status[d] != StatusSucceeded {
				ok = false
				break
			}
		}
		if ok {
			ready = append(ready, t)
		}
	}
	return ready
}

// AllTerminal reports whether every task is in a terminal state.
func AllTerminal(tasks []Task) bool {
	for _, t := range tasks {
		switch t.Status {
		case StatusSucceeded, StatusFailed, StatusCancelled, StatusBlocked, StatusAwaitingInput:
			continue
		default:
			return false
		}
	}
	return len(tasks) > 0
}

// NeedsAttention reports whether any task is waiting on the user.
func NeedsAttention(tasks []Task) bool {
	for _, t := range tasks {
		if t.Status == StatusAwaitingInput {
			return true
		}
	}
	return false
}

// Succeeded reports whether all tasks succeeded.
func Succeeded(tasks []Task) bool {
	if len(tasks) == 0 {
		return false
	}
	for _, t := range tasks {
		if t.Status != StatusSucceeded {
			return false
		}
	}
	return true
}
