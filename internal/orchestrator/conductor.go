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
	Type      string `json:"type"` // run | goal | none
	ProjectID string `json:"project_id,omitempty"`
	Objective string `json:"objective,omitempty"`
	RunID     string `json:"run_id,omitempty"` // run id or goal id
}

// ChatResult is the conductor's response to a user message.
type ChatResult struct {
	Reply   string       `json:"reply"`
	Actions []ChatAction `json:"actions,omitempty"`
}

// ChatOpts scopes an orchestrator turn (e.g. TUI tree project focus).
type ChatOpts struct {
	ProjectID string
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
	loops   *goalLoopState // durable bench goal loops (Bee-like)
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
func (c *Conductor) SetForceAgent(agentID string) { c.forceAgent = strings.TrimSpace(agentID) }

// ForceAgent returns the current worker override (empty = auto-route).
func (c *Conductor) ForceAgent() string { return c.forceAgent }

// SetAdapter swaps the conversational orchestrator brain at runtime.
func (c *Conductor) SetAdapter(adapter agents.AgentAdapter) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.adapter = adapter
}

// AdapterID returns the current orchestrator agent id, or empty if heuristic/none.
func (c *Conductor) AdapterID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.adapter == nil {
		return ""
	}
	return c.adapter.ID()
}

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
	return c.ChatWith(ctx, userText, ChatOpts{})
}

// ChatWith is Chat plus optional project focus from the TUI tree.
func (c *Conductor) ChatWith(ctx context.Context, userText string, opts ChatOpts) (ChatResult, error) {
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
	focus := strings.TrimSpace(opts.ProjectID)

	// Tiny fast path only (help / thanks / project list). Everything else is agentic.
	if intent, ok := c.simpleIntent(ctx, userText, refs, focus); ok {
		c.mu.Lock()
		c.history = append(c.history, ChatMessage{Role: "assistant", Content: intent.Reply, Timestamp: time.Now().UTC()})
		c.mu.Unlock()
		return ChatResult{Reply: intent.Reply, Actions: intent.Actions}, nil
	}

	lower := strings.ToLower(userText)

	// Fresh generation / "continue generating" first (compounds may also ask status).
	if isFreshWorkIntent(userText) {
		res := c.handleFreshWork(ctx, userText, refs, focus)
		c.mu.Lock()
		c.history = append(c.history, ChatMessage{Role: "assistant", Content: res.Reply, Timestamp: time.Now().UTC()})
		c.mu.Unlock()
		return res, nil
	}

	// User correcting / steering an active goal (skip hover, only the other 2…).
	if isGoalSteerIntent(userText) {
		if res, ok := c.handleGoalSteer(ctx, userText, refs, focus); ok {
			c.mu.Lock()
			c.history = append(c.history, ChatMessage{Role: "assistant", Content: res.Reply, Timestamp: time.Now().UTC()})
			c.mu.Unlock()
			return res, nil
		}
		// No active goal: start one from the correction (do not slice into "generate the hover…").
		obj := steerObjective(userText, "")
		res := c.dispatchBenchGoals(ctx, obj, refs, focus, false)
		c.mu.Lock()
		c.history = append(c.history, ChatMessage{Role: "assistant", Content: res.Reply, Timestamp: time.Now().UTC()})
		c.mu.Unlock()
		return res, nil
	}

	// Short status about generation ("generated?") — never a new goal.
	if isGenerationStatusAsk(lower) || isProgressQuery(lower) {
		res := c.progressReply(ctx, userText, refs, focus)
		c.mu.Lock()
		c.history = append(c.history, ChatMessage{Role: "assistant", Content: res.Reply, Timestamp: time.Now().UTC()})
		c.mu.Unlock()
		return res, nil
	}

	// Pure continue/unstick — resume incomplete verify/integrate or paused goal.
	if isResumeOnlyIntent(userText) {
		if res, ok := c.tryResumeIncomplete(ctx, userText, refs, focus); ok {
			c.mu.Lock()
			c.history = append(c.history, ChatMessage{Role: "assistant", Content: res.Reply, Timestamp: time.Now().UTC()})
			c.mu.Unlock()
			return res, nil
		}
		if pid := resolveChatProjectID(userText, focus, refs); pid != "" && c.engine != nil {
			if g, err := c.engine.store.GetActiveBenchGoal(ctx, pid); err == nil && g.Status == storage.GoalPausedNeedsYou {
				if err := c.ResumeBenchGoal(ctx, g.ID, ""); err == nil {
					reply := fmt.Sprintf("Resuming paused bench goal `%s` on **%s**.", g.ID, pid)
					c.mu.Lock()
					c.history = append(c.history, ChatMessage{Role: "assistant", Content: reply, Timestamp: time.Now().UTC()})
					c.mu.Unlock()
					return ChatResult{Reply: reply, Actions: []ChatAction{{Type: "goal", ProjectID: pid, Objective: g.Objective, RunID: g.ID}}}, nil
				}
			}
		}
	}

	// Other work intents → durable bench goal loop.
	if isLeaderActionIntent(userText) {
		res := c.dispatchBenchGoals(ctx, userText, refs, focus, isResumeOnlyIntent(userText))
		c.mu.Lock()
		c.history = append(c.history, ChatMessage{Role: "assistant", Content: res.Reply, Timestamp: time.Now().UTC()})
		c.mu.Unlock()
		return res, nil
	}

	// Full orchestrator agent (Cursor preferred): tool loop, then answer/actions.
	var agenticErr error
	if result, err := c.agenticChat(ctx, userText, refs, focus); err == nil {
		if len(result.Actions) > 0 {
			result = c.materializeGoalActions(ctx, result, refs)
		}
		c.mu.Lock()
		c.history = append(c.history, ChatMessage{Role: "assistant", Content: result.Reply, Timestamp: time.Now().UTC()})
		c.mu.Unlock()
		return result, nil
	} else {
		agenticErr = err
	}

	// If the agent CLI is unavailable, still answer factual questions from local tools / leaders.
	if isOutcomeQuery(lower) || isWorkSummaryQuery(lower) {
		intent := c.outcomeOrSummaryReply(ctx, refs, focus, lower)
		reply := intent.Reply
		if agenticErr != nil {
			reply = appendOrchUnavailableNote(reply, agenticErr)
		}
		c.mu.Lock()
		c.history = append(c.history, ChatMessage{Role: "assistant", Content: reply, Timestamp: time.Now().UTC()})
		c.mu.Unlock()
		return ChatResult{Reply: reply}, nil
	}
	if isAttentionQuery(lower) || isAuthOrPermissionQuery(lower) {
		intent := c.attentionDetailReply(ctx)
		reply := intent.Reply
		if agenticErr != nil {
			reply = appendOrchUnavailableNote(reply, agenticErr)
		}
		c.mu.Lock()
		c.history = append(c.history, ChatMessage{Role: "assistant", Content: reply, Timestamp: time.Now().UTC()})
		c.mu.Unlock()
		return ChatResult{Reply: reply}, nil
	}
	if isLeaderConsultQuery(lower) || isJudgmentQuery(lower) {
		pid := resolveChatProjectID(userText, focus, refs)
		reply, err := c.AskLeader(ctx, pid, userText, refs)
		if err != nil {
			reply = err.Error()
		}
		if agenticErr != nil {
			reply = appendOrchUnavailableNote(reply, agenticErr)
		}
		c.mu.Lock()
		c.history = append(c.history, ChatMessage{Role: "assistant", Content: reply, Timestamp: time.Now().UTC()})
		c.mu.Unlock()
		return ChatResult{Reply: reply}, nil
	}
	// Any other interrogative: briefing + leader consult — never canned heuristic help.
	if isInterrogative(userText) {
		res := c.progressReply(ctx, userText, refs, focus)
		if agenticErr != nil {
			res.Reply = appendOrchUnavailableNote(res.Reply, agenticErr)
		}
		c.mu.Lock()
		c.history = append(c.history, ChatMessage{Role: "assistant", Content: res.Reply, Timestamp: time.Now().UTC()})
		c.mu.Unlock()
		return res, nil
	}

	// Fallback: previous interpret / heuristic path if agent unavailable (work requests only).
	intent, err := c.interpret(ctx, userText, refs, focus)
	if err != nil {
		intent = heuristicIntent(userText, refs)
	}

	result := c.applyGoalActions(ctx, intent.Reply, intent.Actions, refs, isContinueIntent(userText))
	c.mu.Lock()
	c.history = append(c.history, ChatMessage{Role: "assistant", Content: result.Reply, Timestamp: time.Now().UTC()})
	c.mu.Unlock()
	return result, nil
}

// dispatchBenchGoals starts durable goals from a work-intent message.
func (c *Conductor) dispatchBenchGoals(ctx context.Context, userText string, refs []ProjectRef, focus string, continuing bool) ChatResult {
	intent := heuristicIntent(userText, refs)
	if focus != "" && len(intent.Actions) == 0 {
		if ref, ok := findProject(refs, focus); ok {
			intent.Actions = []ChatAction{{Type: "run", ProjectID: ref.ID, Objective: userText}}
			intent.Reply = fmt.Sprintf("Got it — I'll drive a bench goal on %s.", ref.Name)
		}
	}
	if len(intent.Actions) == 0 {
		pid := resolveChatProjectID(userText, focus, refs)
		if pid != "" {
			if ref, ok := findProject(refs, pid); ok {
				intent.Actions = []ChatAction{{Type: "run", ProjectID: ref.ID, Objective: userText}}
				intent.Reply = fmt.Sprintf("Got it — I'll drive a bench goal on %s.", ref.Name)
			}
		}
	}
	if len(intent.Actions) == 0 {
		if intent.Reply == "" {
			intent.Reply = "Which project should I start a bench goal on?"
		}
		return ChatResult{Reply: intent.Reply}
	}
	return c.applyGoalActions(ctx, intent.Reply, intent.Actions, refs, continuing)
}

func (c *Conductor) materializeGoalActions(ctx context.Context, result ChatResult, refs []ProjectRef) ChatResult {
	return c.applyGoalActions(ctx, result.Reply, result.Actions, refs, false)
}

func (c *Conductor) applyGoalActions(ctx context.Context, reply string, actions []ChatAction, refs []ProjectRef, supersede bool) ChatResult {
	result := ChatResult{Reply: reply}
	for _, act := range actions {
		if (act.Type != "run" && act.Type != "goal") || act.Objective == "" {
			continue
		}
		ref, ok := findProject(refs, act.ProjectID)
		if !ok && len(refs) == 1 {
			ref, ok = refs[0], true
		}
		if !ok {
			result.Reply += fmt.Sprintf("\n\n(Could not resolve project %q for goal.)", act.ProjectID)
			continue
		}
		if supersede && c.engine != nil {
			cancelled, _ := c.engine.CancelActiveRunsForProject(ctx, ref.ID)
			if len(cancelled) > 0 {
				result.Reply += fmt.Sprintf("\n\nStopped %d prior run(s) on %s.", len(cancelled), ref.Name)
			}
		}
		obj := act.Objective
		if supersede {
			obj = continueObjective(obj, ref.Name)
		}
		goalID, err := c.StartBenchGoal(ctx, ref.ID, obj, c.forceAgent)
		if err != nil {
			result.Reply += fmt.Sprintf("\n\nFailed to start bench goal on %s: %v", ref.Name, err)
			continue
		}
		act.Type = "goal"
		act.RunID = goalID
		act.ProjectID = ref.ID
		act.Objective = obj
		result.Actions = append(result.Actions, act)
		if strings.TrimSpace(result.Reply) == "" || strings.HasPrefix(strings.ToLower(result.Reply), "got it") {
			result.Reply = fmt.Sprintf("Started on %s: %s", ref.Name, truncateObj(obj, 120))
		} else {
			result.Reply = strings.TrimSpace(result.Reply) + fmt.Sprintf("\nStarted on %s: %s", ref.Name, truncateObj(obj, 100))
		}
	}
	return result
}

type rawIntent struct {
	Reply   string       `json:"reply"`
	Actions []ChatAction `json:"actions"`
}

func (c *Conductor) interpret(ctx context.Context, userText string, refs []ProjectRef, focusProjectID string) (rawIntent, error) {
	if c.adapter == nil {
		return heuristicIntent(userText, refs), nil
	}
	ok, err := c.adapter.Detect(ctx)
	if err != nil || !ok {
		return heuristicIntent(userText, refs), nil
	}

	briefing := c.gatherWorkBriefing(ctx, refs, focusProjectID)
	prompt := conductorPrompt(userText, refs, c.History(), briefing, focusProjectID)
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

func conductorPrompt(userText string, refs []ProjectRef, history []ChatMessage, workBriefing, focusProjectID string) string {
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
	if strings.TrimSpace(workBriefing) == "" {
		workBriefing = "No live briefing available."
	}
	focusNote := "none (all registered projects)"
	if focusProjectID != "" {
		focusNote = focusProjectID
	}
	return fmt.Sprintf(`You are the QtMesh Agents top-level orchestrator — the developer's representative coordinating multi-agent work.
You talk with the developer and may start autonomous coding runs on ANY registered project.
You do NOT require a single "active" project. Choose project(s) from the list.
TUI project focus: %s

Registered projects:
%s

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
- You ARE situation-aware. Answer from LIVE WORK BRIEFING first (git status/diff, runs, tasks, attention). Never say you have not inspected the diff when git data is present. Do not invent commits.
- If the user asks about auth, login, permissions, attention, or what is blocked: use the Needs-you section. Quote Auth URLs. Do NOT invent providers.
- qma cannot collect passwords in chat.
- If the user is just chatting, clarifying, or asking status / what changed, return actions: [].
- If they want work done, emit one run action per affected project.
- project_id MUST be an id from the registered list when starting a run.
- If the target project is ambiguous, ask in reply and do not start a run.
- Prefer the TUI focus project when relevant.
- Never merge protected branches; runs already enforce approval for merges.
- Keep reply concise and helpful.
`, focusNote, formatProjectList(refs), workBriefing, hist.String(), userText)
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

// simpleIntent answers lightweight requests without spawning agents or runs.
func (c *Conductor) simpleIntent(ctx context.Context, userText string, refs []ProjectRef, focusProjectID string) (rawIntent, bool) {
	lower := strings.ToLower(strings.TrimSpace(userText))
	switch {
	case lower == "help" || lower == "?" || lower == "hi" || lower == "hello":
		return rawIntent{Reply: "I'm a **full orchestrator agent** (Cursor-first). I use tools to inspect git, worktrees, runs, and can start work.\n" +
			"Ask anything about the project — or select **leader:…** in the tree to talk to the lead directly.\n" +
			"⚠ marks mean a worker needs your attention."}, true
	case strings.Contains(lower, "what project") || lower == "projects" || lower == "list projects":
		return rawIntent{Reply: "Registered projects:\n" + formatProjectList(refs)}, true
	case strings.Contains(lower, "bench") && (strings.Contains(lower, "list") || strings.Contains(lower, "show") || lower == "benches"):
		if c.engine == nil {
			return rawIntent{Reply: "No benches yet."}, true
		}
		benches, err := c.engine.ListBenches(ctx)
		if err != nil || len(benches) == 0 {
			return rawIntent{Reply: "No project benches open yet. Starting a run opens a bench automatically."}, true
		}
		var b strings.Builder
		b.WriteString("Project benches:\n")
		for _, bench := range benches {
			b.WriteString(fmt.Sprintf("  ◆ %s  leader=%s  [%s]\n", bench.Name, bench.LeaderAgentID, bench.Status))
		}
		return rawIntent{Reply: b.String()}, true
	}
	// Thanks-only acknowledgements — never treat real questions as "you're welcome".
	if !strings.Contains(lower, "?") && len(strings.Fields(userText)) <= 3 && !isContinueIntent(userText) &&
		!containsAny(lower, []string{"implement", "add ", "fix ", "build ", "create ", "run ", "did ", "work"}) {
		if containsAny(lower, []string{"thanks", "thank you", "ok", "okay", "cool", "great"}) {
			return rawIntent{Reply: "You're welcome. Ask me anything about the project anytime."}, true
		}
	}
	return rawIntent{}, false
}

func (c *Conductor) outcomeOrSummaryReply(ctx context.Context, refs []ProjectRef, focusProjectID, lower string) rawIntent {
	brief := c.gatherWorkBriefing(ctx, refs, focusProjectID)
	if isOutcomeQuery(lower) {
		verdict := c.latestRunVerdict(ctx, refs, focusProjectID)
		return rawIntent{Reply: verdict + "\n\n" + brief}
	}
	return rawIntent{Reply: "Here's what I see live (no new run started):\n\n" + brief}
}

func (c *Conductor) attentionDetailReply(ctx context.Context) rawIntent {
	if c.engine == nil {
		return rawIntent{Reply: "No engine wired — cannot inspect live agent state."}
	}
	blocked := c.gatherBlockedAgents(ctx)
	if len(blocked) > 0 {
		return rawIntent{Reply: formatLiveSituation(blocked)}
	}
	open, err := c.engine.ListOpenAttention(ctx)
	if err != nil {
		return rawIntent{Reply: "Could not load attention marks: " + err.Error()}
	}
	if len(open) == 0 {
		return rawIntent{Reply: "Nothing needs your attention right now. No auth/login prompts from agents."}
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%d attention mark(s) open (no task output excerpt available):\n", len(open)))
	for _, a := range open {
		b.WriteString(fmt.Sprintf("  ⚠ [%s] %s", a.Kind, a.Message))
		if a.TaskID != "" {
			b.WriteString(" (task " + a.TaskID + ")")
		}
		b.WriteString("\n")
	}
	b.WriteString("Open the ⚠ node in the TUI to pick a resolution option.")
	return rawIntent{Reply: b.String()}
}

// progressReply answers status/progress as the meta-leader: AskLeader when a
// project is resolved for a topical progress ask, otherwise a cross-bench snapshot.
func (c *Conductor) progressReply(ctx context.Context, userText string, refs []ProjectRef, focus string) ChatResult {
	lower := strings.ToLower(strings.TrimSpace(userText))
	// Bare status / "how's it going" → orchestrator-wide snapshot (not a single-leader consult).
	if lower == "status" || lower == "how's it going" || lower == "hows it going" ||
		lower == "how is it going" || lower == "how's it going?" || lower == "how is it going?" {
		intent := c.statusReply(ctx, refs, focus)
		return ChatResult{Reply: intent.Reply}
	}

	pid := resolveChatProjectID(userText, focus, refs)
	matched := MatchProjects(userText, refs)
	if len(matched) > 1 && strings.TrimSpace(focus) == "" {
		var b strings.Builder
		for _, ref := range matched {
			if gp := c.FormatGoalProgress(ctx, ref.ID); gp != "" {
				b.WriteString(ref.Name + ": " + gp + "\n")
			}
		}
		if b.Len() == 0 {
			return ChatResult{Reply: "No active bench goals on the matched projects."}
		}
		return ChatResult{Reply: strings.TrimSpace(b.String())}
	}
	if pid != "" {
		gp := c.FormatGoalProgress(ctx, pid)
		// Short status asks ("generated?") — local snapshot only, no leader dump.
		if isGenerationStatusAsk(lower) || len(strings.TrimSpace(userText)) < 24 {
			if gp == "" {
				return ChatResult{Reply: "No bench goal on that project yet."}
			}
			return ChatResult{Reply: gp}
		}
		reply, err := c.AskLeader(ctx, pid, userText, refs)
		if err != nil {
			if gp != "" {
				return ChatResult{Reply: gp}
			}
			return ChatResult{Reply: fmt.Sprintf("No leader reply (%v).", err)}
		}
		reply = strings.TrimSpace(reply)
		if gp != "" && !strings.Contains(reply, "Goal [") {
			reply = gp + "\n" + truncateObj(reply, 400)
		} else {
			reply = truncateObj(reply, 500)
		}
		return ChatResult{Reply: strings.TrimSpace(reply)}
	}
	intent := c.statusReply(ctx, refs, focus)
	return ChatResult{Reply: intent.Reply}
}

func appendOrchUnavailableNote(reply string, err error) string {
	if err == nil {
		return reply
	}
	note := fmt.Sprintf("\n\n_(Orchestrator vendor agent unavailable: %v — answered from live briefing / project leaders.)_", err)
	return strings.TrimSpace(reply) + note
}

func (c *Conductor) statusReply(ctx context.Context, refs []ProjectRef, focusProjectID string) rawIntent {
	var b strings.Builder
	b.WriteString("Orchestrator status\n")
	b.WriteString(fmt.Sprintf("Projects: %d registered\n", len(refs)))
	if c.engine != nil {
		if benches, err := c.engine.ListBenches(ctx); err == nil {
			b.WriteString(fmt.Sprintf("Benches:  %d open\n", len(benches)))
			for _, bench := range benches {
				mark := "·"
				if bench.Status == BenchNeedsAttention {
					mark = "⚠"
				} else if bench.Status == BenchActive {
					mark = "●"
				}
				b.WriteString(fmt.Sprintf("  %s %s (leader %s)\n", mark, bench.Name, bench.LeaderAgentID))
			}
		}
		blocked := c.gatherBlockedAgents(ctx)
		if len(blocked) == 0 {
			b.WriteString("Attention: none open\n")
		} else {
			b.WriteString(fmt.Sprintf("Attention: %d blocked task(s) ⚠\n", len(blocked)))
			for i, ba := range blocked {
				if i >= 3 {
					break
				}
				line := fmt.Sprintf("  ⚠ %s [%s] %s", ba.TaskID, orDash(ba.AgentID), ba.Error)
				if label := authProviderLabel(ba.AuthHost); label != "" {
					line += " → " + label
				}
				if len(ba.AuthURLs) > 0 {
					line += "\n    " + ba.AuthURLs[0]
				}
				b.WriteString(line + "\n")
			}
			b.WriteString("Ask \"where are they authenticating?\" or \"attention\" for full details.\n")
		}
		if c.engine.store != nil {
			if runs, err := c.engine.store.ListRuns(ctx, 20); err == nil {
				var live []storage.Run
				for _, r := range runs {
					if r.Status == RunRunning || r.Status == RunPlanning || r.Status == RunNeedsAttention {
						live = append(live, r)
					}
				}
				b.WriteString(fmt.Sprintf("Runs: %d active\n", len(live)))
				for i, r := range live {
					if i >= 5 {
						break
					}
					b.WriteString(fmt.Sprintf("  %s [%s] %s\n", r.ID, r.Status, truncateObj(r.Objective, 48)))
				}
			}
		}
	}
	// Append a short live briefing so status is never blind.
	brief := c.gatherWorkBriefing(ctx, refs, focusProjectID)
	b.WriteString("\n")
	b.WriteString(truncateObj(brief, 3500))
	b.WriteString("\n\nTip: \"what changed?\" for a full briefing · \"ask the leader …\" for a lead consult · select leader in the tree to talk to them directly.")
	return rawIntent{Reply: b.String()}
}

func truncateObj(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func isAttentionQuery(lower string) bool {
	// Exact / status-style asks only — don't match "finish the attention feature".
	if isContinueIntent(lower) || containsAny(lower, []string{"implement", "finish ", "fix ", "build "}) {
		return false
	}
	return lower == "attention" ||
		strings.HasPrefix(lower, "attention ") ||
		strings.Contains(lower, "need me") ||
		strings.Contains(lower, "waiting for me") ||
		strings.Contains(lower, "needs me") ||
		strings.Contains(lower, "what needs my") ||
		strings.Contains(lower, "needs my attention") ||
		strings.Contains(lower, "open attention") ||
		strings.Contains(lower, "what's blocked") ||
		strings.Contains(lower, "what is blocked") ||
		strings.Contains(lower, "what's wrong") ||
		strings.Contains(lower, "what is wrong")
}

func isContinueIntent(text string) bool {
	lower := strings.ToLower(text)
	return containsAny(lower, []string{
		"continue", "resume", "keep going", "pick up", "finish the", "finish what",
		"carry on", "where we left", "work so far", "what you made", "made so far",
		"go ahead", "go on", "do it", "do that", "unstick", "unblock", "land it",
		"merge it", "ship it", "finish up", "wrap up",
	})
}

// isFreshWorkIntent is true for generate/regenerate verbs (and "continue generating"),
// which must start/drive a bench goal — never resume a stuck verify/integrate.
// Does NOT match past-tense-only "generated?" or negated "do not generate…" (status/steer).
func isFreshWorkIntent(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	if lower == "" || isNegatedGenerate(lower) {
		return false
	}
	if containsAny(lower, []string{
		"continue generating", "keep generating", "start generating",
		"resume generating", "re-generate", "regenerate",
		"please generate", "please regenerate",
	}) {
		return true
	}
	// Word-boundary: "generate" / "generating", not substring of "generated".
	if hasWord(lower, "generate") || hasWord(lower, "generating") {
		return true
	}
	return false
}

// isGenerationStatusAsk is a short/outcome ask about whether generation finished —
// must never become a bench goal objective (e.g. "generated?").
func isGenerationStatusAsk(lower string) bool {
	lower = strings.TrimSpace(strings.ToLower(lower))
	if lower == "" || isFreshWorkIntent(lower) || isNegatedGenerate(lower) {
		return false
	}
	switch lower {
	case "generated?", "generated", "done generating?", "finished generating?",
		"generation done?", "generation finished?", "was it generated?",
		"did it generate?", "another generated?", "was another generated?":
		return true
	}
	if !strings.Contains(lower, "?") && !strings.HasPrefix(lower, "was ") &&
		!strings.HasPrefix(lower, "did ") && !strings.HasPrefix(lower, "is ") {
		return false
	}
	// "was another deckbot generated?" / "did trellis finish?"
	if hasWord(lower, "generated") || strings.Contains(lower, "generation done") ||
		strings.Contains(lower, "generation finish") || strings.Contains(lower, "trellis finish") ||
		(strings.Contains(lower, "another") && strings.Contains(lower, "deckbot")) {
		return true
	}
	return false
}

// isNegatedGenerate matches corrections that forbid (re)generating something.
func isNegatedGenerate(lower string) bool {
	lower = strings.ToLower(lower)
	return containsAny(lower, []string{
		"do not generate", "don't generate", "dont generate",
		"do not regenerate", "don't regenerate", "dont regenerate",
		"stop generating", "never generate", "skip generating",
		"without generating", "don't re-generate", "do not re-generate",
		"do not redo", "don't redo", "dont redo",
	})
}

// isGoalSteerIntent is a mid-goal correction/constraint (skip asset, only remaining, OOM serial).
func isGoalSteerIntent(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	if lower == "" {
		return false
	}
	if isNegatedGenerate(lower) {
		return true
	}
	return containsAny(lower, []string{
		"only the other", "only the remaining", "only the missing",
		"missing only", "just the other", "skip the hover", "skip hover",
		"not the hover", "already generated", "already done",
		"do not generate the hover", "don't generate the hover",
	})
}

// isResumeOnlyIntent is continue/unstick without a fresh generate request.
func isResumeOnlyIntent(text string) bool {
	if isFreshWorkIntent(text) || isGoalSteerIntent(text) {
		return false
	}
	return isContinueIntent(text)
}

// handleGoalSteer amends an active/paused bench goal from a user correction.
// Returns ok=false when there is no goal to steer (caller may start fresh work).
func (c *Conductor) handleGoalSteer(ctx context.Context, userText string, refs []ProjectRef, focus string) (ChatResult, bool) {
	pid := resolveChatProjectID(userText, focus, refs)
	if pid == "" || c.engine == nil || c.engine.store == nil {
		return ChatResult{}, false
	}
	g, err := c.engine.store.GetActiveBenchGoal(ctx, pid)
	if err != nil || g.ID == "" {
		return ChatResult{}, false
	}
	obj := steerObjective(userText, g.Objective)
	if err := c.AmendBenchGoal(ctx, g.ID, obj, userText); err != nil {
		return ChatResult{Reply: "Could not update the running goal: " + err.Error()}, true
	}
	if g.Status == storage.GoalPausedNeedsYou {
		_ = c.ResumeBenchGoal(ctx, g.ID, "")
	}
	reply := fmt.Sprintf("Got it — updated the running goal to: %s", truncateObj(obj, 140))
	return ChatResult{
		Reply:   reply,
		Actions: []ChatAction{{Type: "goal", ProjectID: pid, Objective: obj, RunID: g.ID}},
	}, true
}

// steerObjective turns a correction into a durable goal objective.
func steerObjective(userText, current string) string {
	obj := strings.TrimSpace(userText)
	lower := strings.ToLower(obj)
	// Keep prior scope when the correction is a short constraint.
	if len(obj) < 40 && current != "" && !hasWord(lower, "generate") && !isNegatedGenerate(lower) {
		obj = strings.TrimSpace(current + " — " + obj)
	}
	if containsAny(lower, []string{"one at a time", "one a time", "avoid oom", "oom"}) &&
		!strings.Contains(strings.ToLower(obj), "one at a time to avoid oom") {
		obj += " Generate one at a time to avoid OOM; wait for each to finish before the next."
	}
	return obj
}

// handleFreshWork answers status if a goal is already active; otherwise starts a bench goal.
func (c *Conductor) handleFreshWork(ctx context.Context, userText string, refs []ProjectRef, focus string) ChatResult {
	pid := resolveChatProjectID(userText, focus, refs)
	obj := extractGenerateObjective(userText)
	if pid != "" && c.engine != nil && c.engine.store != nil {
		if g, err := c.engine.store.GetActiveBenchGoal(ctx, pid); err == nil && g.ID != "" {
			// Mid-flight correction phrased as generate work → amend, don't ignore.
			if isGoalSteerIntent(userText) {
				if res, ok := c.handleGoalSteer(ctx, userText, refs, focus); ok {
					return res
				}
			}
			switch g.Status {
			case storage.GoalActive:
				reply := fmt.Sprintf("Already running — %s", truncateObj(g.Objective, 120))
				return ChatResult{Reply: reply, Actions: []ChatAction{{Type: "goal", ProjectID: pid, Objective: g.Objective, RunID: g.ID}}}
			case storage.GoalPausedNeedsYou:
				_ = c.ResumeBenchGoal(ctx, g.ID, "")
				reply := fmt.Sprintf("Resumed — %s", truncateObj(g.Objective, 120))
				return ChatResult{Reply: reply, Actions: []ChatAction{{Type: "goal", ProjectID: pid, Objective: g.Objective, RunID: g.ID}}}
			}
		}
	}
	// Build objective that honors "one at a time" style constraints when present.
	if containsAny(strings.ToLower(userText), []string{"one at a time", "one a time", "avoid oom", "oom"}) &&
		!strings.Contains(strings.ToLower(obj), "one at a time to avoid oom") {
		obj = obj + " Generate one at a time to avoid OOM; wait for each to finish before the next."
	}
	return c.dispatchBenchGoals(ctx, obj, refs, focus, false)
}

// extractGenerateObjective picks the work clause from compound status+generate messages.
// Never slices into a negated "do not generate…".
func extractGenerateObjective(userText string) string {
	obj := strings.TrimSpace(userText)
	lower := strings.ToLower(obj)
	if isNegatedGenerate(lower) {
		return obj
	}
	if i := strings.Index(lower, "continue generating"); i >= 0 {
		return strings.TrimSpace(obj[i:])
	}
	// Find a generate/regenerate verb occurrence that is not part of "generated".
	for _, verb := range []string{"regenerate", "re-generate", "generating", "generate"} {
		search := lower
		offset := 0
		for {
			i := strings.Index(search, verb)
			if i < 0 {
				break
			}
			abs := offset + i
			after := abs + len(verb)
			if after < len(lower) && isAlphaByte(lower[after]) {
				// e.g. "generated" — keep scanning
				search = lower[after:]
				offset = after
				continue
			}
			before := lower[:abs]
			if strings.HasSuffix(strings.TrimSpace(before), "not") ||
				strings.HasSuffix(strings.TrimSpace(before), "don't") ||
				strings.HasSuffix(strings.TrimSpace(before), "dont") ||
				strings.Contains(before[max(0, abs-12):abs], "do not") ||
				strings.Contains(before[max(0, abs-8):abs], "don't") {
				search = lower[after:]
				offset = after
				continue
			}
			return strings.TrimSpace(obj[abs:])
		}
	}
	return obj
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func hasWord(s, word string) bool {
	if word == "" {
		return false
	}
	idx := 0
	for {
		i := strings.Index(s[idx:], word)
		if i < 0 {
			return false
		}
		i += idx
		beforeOK := i == 0 || !isAlphaByte(s[i-1])
		after := i + len(word)
		afterOK := after >= len(s) || !isAlphaByte(s[after])
		if beforeOK && afterOK {
			return true
		}
		idx = i + 1
	}
}

func isAlphaByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// isLeaderActionIntent is true when the human is telling the leader to act
// (start/resume work), not just asking for advice.
func isLeaderActionIntent(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	if lower == "" || isGenerationStatusAsk(lower) || isNegatedGenerate(lower) {
		return false
	}
	// Questions stay advisory ("did you generate…?", "where are the models?").
	if strings.HasSuffix(lower, "?") ||
		strings.HasPrefix(lower, "did ") || strings.HasPrefix(lower, "do you ") ||
		strings.HasPrefix(lower, "does ") || strings.HasPrefix(lower, "is ") ||
		strings.HasPrefix(lower, "are ") || strings.HasPrefix(lower, "what ") ||
		strings.HasPrefix(lower, "where ") || strings.HasPrefix(lower, "why ") ||
		strings.HasPrefix(lower, "how ") || strings.HasPrefix(lower, "when ") ||
		strings.HasPrefix(lower, "who ") || strings.HasPrefix(lower, "which ") {
		// Exception: "can you generate…" / "could you fix…" are requests.
		if !(strings.HasPrefix(lower, "can you ") || strings.HasPrefix(lower, "could you ") ||
			strings.HasPrefix(lower, "would you ") || strings.HasPrefix(lower, "will you ")) {
			return false
		}
	}
	if isContinueIntent(lower) {
		return true
	}
	if hasWord(lower, "generate") || hasWord(lower, "generating") ||
		containsAny(lower, []string{"regenerate", "re-generate"}) {
		return true
	}
	return containsAny(lower, []string{
		"implement", "start ", "start a", "run ",
		"please do", "do the", "fix ", "build ", "create ", "update ",
		"then continue", "orchestrate", "kick off", "retry", "re-run", "rerun",
	})
}

func continueObjective(userObj, projectName string) string {
	base := strings.TrimSpace(userObj)
	if base == "" || isContinueIntent(base) && len(base) < 80 {
		base = fmt.Sprintf(
			"Continue unfinished work in %s. Inspect git status, recent commits, open TODOs, failing tests, and any .qma-worktrees leftovers. Pick the single highest-priority incomplete feature from prior orchestration, implement it with tests on a feature branch, verify build/tests, and summarize what changed and what remains. Do not merge protected branches.",
			projectName,
		)
		return base
	}
	lower := strings.ToLower(base)
	if !strings.Contains(lower, "do not merge") && !strings.Contains(lower, "don't merge") {
		base += " Do not merge protected branches."
	}
	return base
}

func heuristicIntent(userText string, refs []ProjectRef) rawIntent {
	lower := strings.ToLower(userText)
	matched := MatchProjects(userText, refs)
	continuing := isContinueIntent(userText)

	help := func(extra string) rawIntent {
		reply := extra
		if reply != "" {
			reply += "\n\n"
		}
		reply += "I'm your cross-project orchestrator. Registered projects:\n" + formatProjectList(refs)
		reply += "\nExamples:\n"
		reply += "- \"In QtMeshEditor, add hair material support\"\n"
		reply += "- \"Continue the work on QtMesh-Agents\"\n"
		reply += "- \"What projects do you know about?\""
		return rawIntent{Reply: reply}
	}

	if strings.Contains(lower, "what project") || strings.Contains(lower, "list project") ||
		lower == "projects" || lower == "help" || lower == "?" {
		return help("Here are the workspaces I can orchestrate across.")
	}

	if strings.Contains(lower, "status") || strings.Contains(lower, "is it working") ||
		strings.Contains(lower, "what's running") || strings.Contains(lower, "what is running") {
		return rawIntent{Reply: "Check the left tree for ● working agents, or run `qma run list` / expand a run and press enter. Say \"continue on <project>\" to supersede stuck runs with a fresh orchestration."}
	}

	// Explicit run-like phrasing or clear implementation ask with project mention.
	looksLikeWork := continuing || containsAny(lower, []string{
		"implement", "add ", "fix ", "build ", "create ", "refactor", "update ", "write ",
		"run ", "please ", "can you ", "could you ", "orchestrate", "ship ", "complete ",
		"generate", "regenerate", "go ahead",
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
		if len(refs) == 1 || continuing {
			// Continue with a single registered project, or the only one available.
			if len(refs) == 1 {
				matched = refs
			} else {
				return help("Which project should I continue? Mention it by name (e.g. \"continue on QtMesh-Agents\").")
			}
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
