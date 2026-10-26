package scheduler

import "testing"

func TestValidateOK(t *testing.T) {
	plan := Plan{Tasks: []Task{
		{ID: "a", Description: "A", Acceptance: []string{"ok"}},
		{ID: "b", Description: "B", Dependencies: []string{"a"}, Acceptance: []string{"ok"}},
	}}
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateCycle(t *testing.T) {
	plan := Plan{Tasks: []Task{
		{ID: "a", Description: "A", Dependencies: []string{"b"}, Acceptance: []string{"ok"}},
		{ID: "b", Description: "B", Dependencies: []string{"a"}, Acceptance: []string{"ok"}},
	}}
	if err := plan.Validate(); err == nil {
		t.Fatal("expected cycle error")
	}
}

func TestValidateMissingDep(t *testing.T) {
	plan := Plan{Tasks: []Task{
		{ID: "a", Description: "A", Dependencies: []string{"z"}, Acceptance: []string{"ok"}},
	}}
	if err := plan.Validate(); err == nil {
		t.Fatal("expected missing dep")
	}
}

func TestReadyTasks(t *testing.T) {
	tasks := []Task{
		{ID: "a", Status: StatusSucceeded, Acceptance: []string{"ok"}, Description: "A"},
		{ID: "b", Status: StatusPending, Dependencies: []string{"a"}, Acceptance: []string{"ok"}, Description: "B"},
		{ID: "c", Status: StatusPending, Dependencies: []string{"b"}, Acceptance: []string{"ok"}, Description: "C"},
	}
	ready := ReadyTasks(tasks)
	if len(ready) != 1 || ready[0].ID != "b" {
		t.Fatalf("ready=%v", ready)
	}
}
