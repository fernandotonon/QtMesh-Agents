package orchestrator

import (
	"os"
	"os/exec"
	"sync"
)

// procTracker remembers PIDs started for a run so cancel can reap orphans.
type procTracker struct {
	mu   sync.Mutex
	byRun map[string]map[int]struct{}
}

func newProcTracker() *procTracker {
	return &procTracker{byRun: map[string]map[int]struct{}{}}
}

func (p *procTracker) add(runID string, pid int) {
	if runID == "" || pid <= 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.byRun[runID] == nil {
		p.byRun[runID] = map[int]struct{}{}
	}
	p.byRun[runID][pid] = struct{}{}
}

func (p *procTracker) remove(runID string, pid int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if m := p.byRun[runID]; m != nil {
		delete(m, pid)
		if len(m) == 0 {
			delete(p.byRun, runID)
		}
	}
}

func (p *procTracker) killRun(runID string) {
	p.mu.Lock()
	pids := make([]int, 0, 8)
	for pid := range p.byRun[runID] {
		pids = append(pids, pid)
	}
	delete(p.byRun, runID)
	p.mu.Unlock()
	for _, pid := range pids {
		proc, err := os.FindProcess(pid)
		if err != nil {
			continue
		}
		_ = proc.Kill()
	}
}

// killOrphanWorkers best-effort kills vendor CLIs whose argv still mention the run id.
func killOrphanWorkers(runID string) {
	if runID == "" {
		return
	}
	// Match codex/claude headless workers tagged with this run in their prompt/task id.
	_ = exec.Command("pkill", "-f", runID).Run()
}
