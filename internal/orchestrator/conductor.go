package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/fernandotonon/QtMesh-Agents/internal/agents"
	"github.com/fernandotonon/QtMesh-Agents/internal/storage"
)

// ChatMessage is one turn in the orchestrator conversation.
type ChatMessage struct {
	Role      string    `json:"role"` // user | assistant | system
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
}

// ChatAction is a structured action the conductor decided to take.
type ChatAction struct {
	Type      string `json:"type"` // run | none
	ProjectID string `json:"project_id,omitempty"`
	Objective string `json:"objective,omitempty"`
	RunID     string `json:"run_id,omitempty"`
}

// ChatResult is the conductor's response to a user message.
type ChatResult struct {
	Reply   string       `json:"reply"`
	Actions []ChatAction `json:"actions,omitempty"`
}

// ProjectLister provides registered projects (usually projects.Service).
type ProjectLister interface {
	List(ctx context.Context) ([]storage.Project, error)
}

// Conductor is the conversational top-level orchestrator across all projects.
type Conductor struct {
	engine     *Engine
	projects   ProjectLister
	adapter    agents.AgentAdapter // optional conversational brain
	forceAgent string              // optional AgentOverride for all started runs

	mu      sync.Mutex
	history []ChatMessage
}

func NewConductor(engine *Engine, projects ProjectLister, adapter agents.AgentAdapter) *Conductor {
	return &Conductor{
		engine:   engine,
		projects: projects,
		adapter:  adapter,
		history: []ChatMessage{{
			Role:      "system",
			Content:   "QtMesh Agents orchestrator online. You can discuss work across all registered projects; no single active project is required.",
			Timestamp: time.Now().UTC(),
		}},
	}
}

// SetForceAgent forces every started run onto one agent (useful for tests / mock mode).
func (c *Conductor) SetForceAgent(agentID string) { c.forceAgent = agentID }

// History returns a copy of the conversation.
func (c *Conductor) History() []ChatMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]ChatMessage, len(c.history))
	copy(out, c.history)
	return out
}

// ClearHistory resets the conversation (keeps the system banner).
func (c *Conductor) ClearHistory() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.history = c.history[:1]
}

// Chat handles one user turn: reply, and optionally start one or more project runs.
func (c *Conductor) Chat(ctx context.Context, userText string) (ChatResult, error) {
	userText = strings.TrimSpace(userText)
	if userText == "" {
		return ChatResult{Reply: "Say what you'd like to work on across your projects."}, nil
	}

	c.mu.Lock()
	c.history = append(c.history, ChatMessage{Role: "user", Content: userText, Timestamp: time.Now().UTC()})
	c.mu.Unlock()

	projs, err := c.projects.List(ctx)
	if err != nil {
		return ChatResult{}, err
	}
	refs := refsFromStorage(projs)

	intent, err := c.interpret(ctx, userText, refs)
	if err != nil {
		intent = heuristicIntent(userText, refs)
	}

	result := ChatResult{Reply: intent.Reply}
	for _, act := range intent.Actions {
		if act.Type != "run" || act.Objective == "" {
			continue
		}
		ref, ok := findProject(refs, act.ProjectID)
		if !ok {
			result.Reply += fmt.Sprintf("\n\n(Could not resolve project %q for run.)", act.ProjectID)
			continue
		}
		runID, err := c.engine.StartRun(ctx, PlanRequest{
			Objective:     act.Objective,
			ProjectID:     ref.ID,
			WorkDir:       ref.Path,
			AgentOverride: c.forceAgent,
		})
		if err != nil {
			result.Reply += fmt.Sprintf("\n\nFailed to start run on %s: %v", ref.Name, err)
			continue
		}
		act.RunID = runID
		act.ProjectID = ref.ID
		result.Actions = append(result.Actions, act)
		result.Reply += fmt.Sprintf("\n\n▶ Started run `%s` on **%s**: %s", runID, ref.Name, act.Objective)
	}

	c.mu.Lock()
	c.history = append(c.history, ChatMessage{Role: "assistant", Content: result.Reply, Timestamp: time.Now().UTC()})
	c.mu.Unlock()
	return result, nil
}

type rawIntent struct {
	Reply   string       `json:"reply"`
	Actions []ChatAction `json:"actions"`
}

func (c *Conductor) interpret(ctx context.Context, userText string, refs []ProjectRef) (rawIntent, error) {
	if c.adapter == nil {
		return heuristicIntent(userText, refs), nil
	}
	ok, err := c.adapter.Detect(ctx)
	if err != nil || !ok {
		return heuristicIntent(userText, refs), nil
	}

	prompt := conductorPrompt(userText, refs, c.History())
	exec, err := c.adapter.Execute(ctx, agents.AgentTask{
		ID:          fmt.Sprintf("orch-chat-%d", time.Now().UnixNano()),
		Description: "Orchestrator conversation turn",
		Prompt:      prompt,
	})
	if err != nil || exec.ExitCode != 0 {
		return heuristicIntent(userText, refs), nil
	}
	intent, err := parseConductorJSON(exec.Output)
	if err != nil {
		return heuristicIntent(userText, refs), nil
	}
	if intent.Reply == "" {
		intent.Reply = "Understood."
	}
	return intent, nil
}

func conductorPrompt(userText string, refs []ProjectRef, history []ChatMessage) string {
	var hist strings.Builder
	start := 0
	if len(history) > 12 {
		start = len(history) - 12
	}
	for _, m := range history[start:] {
		if m.Role == "system" {
			continue
		}
		hist.WriteString(m.Role)
		hist.WriteString(": ")
		hist.WriteString(m.Content)
		hist.WriteByte('\n')
	}
	return fmt.Sprintf(`You are the QtMesh Agents top-level orchestrator.
You talk with the developer and may start autonomous coding runs on ANY registered project.
You do NOT require a single "active" project. Choose project(s) from the list.

Registered projects:
%s
Recent conversation:
%s
User message:
%s

Respond with ONLY JSON (no markdown):
{
  "reply": "conversational response to the user",
  "actions": [
    {"type": "run", "project_id": "<id from list>", "objective": "<scoped objective for that repo>"}
  ]
}

Rules:
- If the user is just chatting, clarifying, or asking status, return actions: [].
- If they want work done, emit one run action per affected project.
- project_id MUST be an id from the registered list when starting a run.
- If the target project is ambiguous, ask in reply and do not start a run.
- Never merge protected branches; runs already enforce approval for merges.
- Keep reply concise and helpful.
`, formatProjectList(refs), hist.String(), userText)
}

func parseConductorJSON(raw string) (rawIntent, error) {
	raw = strings.TrimSpace(raw)
	if i := strings.Index(raw, "{"); i >= 0 {
		if j := strings.LastIndex(raw, "}"); j > i {
			raw = raw[i : j+1]
		}
	}
	var intent rawIntent
	if err := json.Unmarshal([]byte(raw), &intent); err != nil {
		return rawIntent{}, err
	}
	return intent, nil
}

func heuristicIntent(userText string, refs []ProjectRef) rawIntent {
	lower := strings.ToLower(userText)
	matched := MatchProjects(userText, refs)

	help := func(extra string) rawIntent {
		reply := extra
		if reply != "" {
			reply += "\n\n"
		}
		reply += "I'm your cross-project orchestrator. Registered projects:\n" + formatProjectList(refs)
		reply += "\nExamples:\n"
		reply += "- \"In QtMeshEditor, add hair material support\"\n"
		reply += "- \"Add matchmaking to Data Core Clash and update QtMeshEditor docs\"\n"
		reply += "- \"What projects do you know about?\""
		return rawIntent{Reply: reply}
	}

	if strings.Contains(lower, "what project") || strings.Contains(lower, "list project") ||
		lower == "projects" || lower == "help" || lower == "?" {
		return help("Here are the workspaces I can orchestrate across.")
	}

	// Explicit run-like phrasing or clear implementation ask with project mention.
	looksLikeWork := containsAny(lower, []string{
		"implement", "add ", "fix ", "build ", "create ", "refactor", "update ", "write ",
		"run ", "please ", "can you ", "could you ",
	})

	if !looksLikeWork {
		if len(matched) > 0 {
			names := make([]string, len(matched))
			for i, m := range matched {
				names[i] = m.Name
			}
			return rawIntent{Reply: fmt.Sprintf(
				"I see you mentioned %s. Tell me what you want done there (or across several projects), and I'll start orchestrated runs.",
				strings.Join(names, ", "),
			)}
		}
		return help("Happy to help. Tell me what to build and in which project(s).")
	}

	if len(matched) == 0 {
		if len(refs) == 0 {
			return rawIntent{Reply: "No projects registered yet. Add some with `qma project add <path>`, then tell me what to do."}
		}
		if len(refs) == 1 {
			matched = refs
		} else {
			return help("I need to know which project(s) this is for. Mention a project name in your message.")
		}
	}

	perProject := splitObjectivesByProject(userText, matched)
	actions := make([]ChatAction, 0, len(perProject))
	var names []string
	for _, item := range perProject {
		obj := strings.TrimSpace(item.Objective)
		if obj == "" {
			obj = stripProjectNames(userText, matched)
		}
		if obj == "" {
			obj = userText
		}
		actions = append(actions, ChatAction{
			Type:      "run",
			ProjectID: item.Project.ID,
			Objective: obj,
		})
		names = append(names, item.Project.Name)
	}
	return rawIntent{
		Reply:   fmt.Sprintf("Got it — I'll orchestrate that on %s.", strings.Join(names, " and ")),
		Actions: actions,
	}
}

type projectObjective struct {
	Project   ProjectRef
	Objective string
}

// splitObjectivesByProject tries to bind clauses to project mentions.
func splitObjectivesByProject(text string, matched []ProjectRef) []projectObjective {
	if len(matched) == 1 {
		return []projectObjective{{Project: matched[0], Objective: stripProjectNames(text, matched)}}
	}
	// Split on ";" or " and " when multiple projects appear.
	parts := regexp.MustCompile(`(?i)\s+;\s+|\s+and\s+`).Split(text, -1)
	var out []projectObjective
	used := map[string]bool{}
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		hits := MatchProjects(part, matched)
		if len(hits) == 1 {
			out = append(out, projectObjective{
				Project:   hits[0],
				Objective: stripProjectNames(part, hits),
			})
			used[hits[0].ID] = true
		}
	}
	if len(out) > 0 {
		// Any matched projects not covered get the shared leftover objective.
		shared := stripProjectNames(text, matched)
		for _, m := range matched {
			if used[m.ID] {
				continue
			}
			out = append(out, projectObjective{Project: m, Objective: shared})
		}
		return out
	}
	// Fallback: same objective on each mentioned project.
	shared := stripProjectNames(text, matched)
	for _, m := range matched {
		out = append(out, projectObjective{Project: m, Objective: shared})
	}
	return out
}

func stripProjectNames(text string, matched []ProjectRef) string {
	out := text
	for _, m := range matched {
		for _, c := range []string{m.Name, m.ID, filepathBase(m.Path)} {
			if c == "" {
				continue
			}
			out = replaceInsensitive(out, c, "")
		}
	}
	out = strings.ReplaceAll(out, "  ", " ")
	out = strings.Trim(out, " :,.-")
	// Drop leading filler.
	lower := strings.ToLower(out)
	for _, p := range []string{"in ", "on ", "for ", "please ", "can you ", "could you "} {
		if strings.HasPrefix(lower, p) {
			out = strings.TrimSpace(out[len(p):])
			lower = strings.ToLower(out)
		}
	}
	return out
}

func replaceInsensitive(s, old, neu string) string {
	if old == "" {
		return s
	}
	lower := strings.ToLower(s)
	oldLower := strings.ToLower(old)
	var b strings.Builder
	i := 0
	for {
		j := strings.Index(lower[i:], oldLower)
		if j < 0 {
			b.WriteString(s[i:])
			break
		}
		j += i
		b.WriteString(s[i:j])
		b.WriteString(neu)
		i = j + len(old)
	}
	return b.String()
}

func filepathBase(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

func containsAny(s string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(s, n) {
			return true
		}
	}
	return false
}

func findProject(refs []ProjectRef, idOrName string) (ProjectRef, bool) {
	idOrName = strings.TrimSpace(idOrName)
	for _, p := range refs {
		if strings.EqualFold(p.ID, idOrName) || strings.EqualFold(p.Name, idOrName) {
			return p, true
		}
	}
	return ProjectRef{}, false
}
