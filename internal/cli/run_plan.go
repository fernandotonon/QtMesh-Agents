package cli

import (
	"github.com/fernandotonon/QtMesh-Agents/internal/agents"
	"github.com/fernandotonon/QtMesh-Agents/internal/agents/claude"
	"github.com/fernandotonon/QtMesh-Agents/internal/agents/codex"
	"github.com/fernandotonon/QtMesh-Agents/internal/agents/cursor"
	"github.com/fernandotonon/QtMesh-Agents/internal/agents/mock"
	"github.com/fernandotonon/QtMesh-Agents/internal/config"
	"github.com/fernandotonon/QtMesh-Agents/internal/orchestrator"
)

func newLocalPlanner(cfg config.Config) *orchestrator.Engine {
	reg := agents.NewRegistry()
	reg.Register(claude.New(cfg.Agents.Claude.Command, cfg.Agents.Claude.Args))
	reg.Register(codex.New(cfg.Agents.Codex.Command, cfg.Agents.Codex.Args))
	reg.Register(cursor.New(cfg.Agents.Cursor.Command, cfg.Agents.Cursor.Args))
	reg.Register(mock.New(cfg.Agents.Mock.Command, cfg.Agents.Mock.Args))
	return orchestrator.New(orchestrator.Deps{
		Config:   cfg,
		Registry: reg,
		Planner:  orchestrator.HeuristicPlanner{},
	})
}

func planReq(objective, agent string) orchestrator.PlanRequest {
	return orchestrator.PlanRequest{Objective: objective, AgentOverride: agent}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
