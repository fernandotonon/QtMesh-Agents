package orchestrator

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fernandotonon/QtMesh-Agents/internal/agents"
	"github.com/fernandotonon/QtMesh-Agents/internal/config"
	"github.com/fernandotonon/QtMesh-Agents/internal/storage"
)

type scriptedAdapter struct {
	agents.BaseAdapter
	outputs []string
	i       int
}

func (s *scriptedAdapter) Detect(context.Context) (bool, error) { return true, nil }
func (s *scriptedAdapter) Capabilities(context.Context) (agents.AgentCapabilities, error) {
	return agents.AgentCapabilities{Headless: true}, nil
}
func (s *scriptedAdapter) StartSession(context.Context, agents.SessionOptions) (agents.AgentSession, error) {
	return nil, nil
}
func (s *scriptedAdapter) Execute(ctx context.Context, task agents.AgentTask) (agents.Execution, error) {
	out := "{}"
	if s.i < len(s.outputs) {
		out = s.outputs[s.i]
		s.i++
	}
	return agents.Execution{Output: out, ExitCode: 0, EndedAt: time.Now()}, nil
}

func TestParseAgentTurn(t *testing.T) {
	raw := "Here you go\n```json\n{\"tools\":[{\"name\":\"git_status\",\"args\":{\"project_id\":\"demo\"}}],\"answer\":\"\",\"actions\":[]}\n```\n"
	turn, err := parseAgentTurn(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(turn.Tools) != 1 || turn.Tools[0].Name != "git_status" {
		t.Fatalf("%+v", turn)
	}
}

func TestAgenticChatToolThenAnswer(t *testing.T) {
	base := t.TempDir()
	store, err := storage.Open(filepath.Join(base, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	eng := New(Deps{Config: config.Default(), Store: store, Planner: HeuristicPlanner{}})

	toolJSON, _ := json.Marshal(agentTurn{
		Tools: []agentToolCall{{Name: "binary_info", Args: map[string]string{}}},
	})
	ansJSON, _ := json.Marshal(agentTurn{
		Answer: "Binary looks fine for tests.",
	})
	ad := &scriptedAdapter{
		BaseAdapter: agents.NewBaseAdapter("cursor", "Cursor", "true", nil),
		outputs:     []string{string(toolJSON), string(ansJSON)},
	}
	c := NewConductor(eng, memProjects{list: []storage.Project{
		{ID: "demo", Name: "Demo", Path: base},
	}}, ad)

	res, err := c.Chat(context.Background(), "is the binary installed?")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Reply, "Binary looks fine") {
		t.Fatalf("reply=%q", res.Reply)
	}
	if ad.i != 2 {
		t.Fatalf("expected 2 agent turns, got %d", ad.i)
	}
}

func TestExecOrchToolBinaryInfo(t *testing.T) {
	base := t.TempDir()
	store, err := storage.Open(filepath.Join(base, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	eng := New(Deps{Config: config.Default(), Store: store, Planner: HeuristicPlanner{}})
	c := NewConductor(eng, memProjects{list: []storage.Project{
		{ID: "demo", Name: "Demo", Path: base},
	}}, nil)
	out := c.execOrchTool(context.Background(), agentToolCall{Name: "binary_info"}, []ProjectRef{{ID: "demo", Path: base}}, "demo")
	if !strings.Contains(out, "Build version") {
		t.Fatalf("%s", out)
	}
}
