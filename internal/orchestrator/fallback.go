package orchestrator

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/fernandotonon/QtMesh-Agents/internal/agents"
	"github.com/fernandotonon/QtMesh-Agents/internal/scheduler"
)

// SetFallbackAgents sets ordered agent IDs to try when Detect fails or empty exit -1.
func (e *Executor) SetFallbackAgents(ids []string) {
	e.muFB.Lock()
	defer e.muFB.Unlock()
	e.fallbackAgents = append([]string(nil), ids...)
}

func (e *Executor) pickAdapter(ctx context.Context, task *scheduler.Task) (agents.AgentAdapter, error) {
	adapter, err := e.Registry.Require(task.AssignedAgent)
	if err == nil {
		ok, derr := adapter.Detect(ctx)
		if derr == nil && ok {
			return adapter, nil
		}
		e.log().Warn("assigned agent detect failed; trying fallback", "agent", task.AssignedAgent, "err", derr)
	} else {
		e.log().Warn("assigned agent missing; trying fallback", "agent", task.AssignedAgent, "err", err)
	}
	for _, id := range e.fallbackList() {
		if id == "" || id == task.AssignedAgent {
			continue
		}
		a, rerr := e.Registry.Require(id)
		if rerr != nil {
			continue
		}
		ok, _ := a.Detect(ctx)
		if !ok {
			continue
		}
		e.log().Info("falling back to agent", "from", task.AssignedAgent, "to", id)
		task.AssignedAgent = id
		return a, nil
	}
	if err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("agent %q not available and no fallback succeeded", task.AssignedAgent)
}

func (e *Executor) tryFallbackAgent(task *scheduler.Task) {
	cur := task.AssignedAgent
	for _, id := range e.fallbackList() {
		if id == "" || id == cur {
			continue
		}
		if e.Registry == nil {
			return
		}
		if _, err := e.Registry.Require(id); err != nil {
			continue
		}
		e.log().Info("retry with fallback agent", "from", cur, "to", id)
		task.AssignedAgent = id
		return
	}
}

func (e *Executor) fallbackList() []string {
	e.muFB.Lock()
	defer e.muFB.Unlock()
	return append([]string(nil), e.fallbackAgents...)
}

const maxAttentionSameFingerprint = 2

func (e *Executor) shouldRaiseFingerprint(runID, taskID, msg string) bool {
	fp := attentionFingerprint(msg)
	key := runID + "|" + taskID + "|" + fp
	e.muFB.Lock()
	defer e.muFB.Unlock()
	if e.attnFP == nil {
		e.attnFP = map[string]int{}
	}
	e.attnFP[key]++
	return e.attnFP[key] <= maxAttentionSameFingerprint
}

func attentionFingerprint(msg string) string {
	norm := strings.ToLower(strings.TrimSpace(msg))
	if len(norm) > 240 {
		norm = norm[:240]
	}
	sum := sha1.Sum([]byte(norm))
	return hex.EncodeToString(sum[:8])
}
