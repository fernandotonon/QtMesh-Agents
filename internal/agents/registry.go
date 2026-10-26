package agents

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// Registry holds registered agent adapters.
type Registry struct {
	mu       sync.RWMutex
	adapters map[string]AgentAdapter
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{adapters: make(map[string]AgentAdapter)}
}

// Register adds an adapter. Later registrations with the same ID replace.
func (r *Registry) Register(a AgentAdapter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.adapters[a.ID()] = a
}

// Get returns an adapter by ID.
func (r *Registry) Get(id string) (AgentAdapter, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.adapters[id]
	return a, ok
}

// List returns adapters sorted by ID.
func (r *Registry) List() []AgentAdapter {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]AgentAdapter, 0, len(r.adapters))
	for _, a := range r.adapters {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID() < out[j].ID() })
	return out
}

// DetectAll probes which adapters are available on PATH.
func (r *Registry) DetectAll(ctx context.Context) map[string]bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]bool, len(r.adapters))
	for id, a := range r.adapters {
		ok, err := a.Detect(ctx)
		out[id] = ok && err == nil
	}
	return out
}

// Require returns an adapter or an error.
func (r *Registry) Require(id string) (AgentAdapter, error) {
	a, ok := r.Get(id)
	if !ok {
		return nil, fmt.Errorf("unknown agent %q", id)
	}
	return a, nil
}
