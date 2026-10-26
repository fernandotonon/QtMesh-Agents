package router

import (
	"fmt"
	"strings"

	"github.com/fernandotonon/QtMesh-Agents/internal/config"
)

// Selection is the result of routing a task category.
type Selection struct {
	AgentID  string
	Fallback []string
	Reason   string
}

// Router selects agents using deterministic preferences.
type Router struct {
	cfg       config.RoutingConfig
	available map[string]bool
}

func New(cfg config.RoutingConfig, available map[string]bool) *Router {
	return &Router{cfg: cfg, available: available}
}

// Select chooses an agent for a category (architecture, implementation, ...).
func (r *Router) Select(category string) (Selection, error) {
	category = strings.ToLower(strings.TrimSpace(category))
	preferred := ""
	if r.cfg.Preferences != nil {
		preferred = r.cfg.Preferences[category]
	}
	candidates := []string{}
	if preferred != "" {
		candidates = append(candidates, preferred)
	}
	candidates = append(candidates, r.cfg.Fallback...)

	seen := map[string]bool{}
	var fallback []string
	for _, id := range candidates {
		id = strings.ToLower(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		if r.available != nil && !r.available[id] {
			fallback = append(fallback, id)
			continue
		}
		return Selection{
			AgentID:  id,
			Fallback: append([]string{}, fallback...),
			Reason:   fmt.Sprintf("category=%s preferred=%s", category, preferred),
		}, nil
	}
	return Selection{}, fmt.Errorf("no available agent for category %q", category)
}
