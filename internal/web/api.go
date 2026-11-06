package web

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/fernandotonon/QtMesh-Agents/internal/orchestrator"
	"github.com/fernandotonon/QtMesh-Agents/internal/sessions"
	"github.com/fernandotonon/QtMesh-Agents/internal/storage"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func readJSON(r *http.Request, dst any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	return dec.Decode(dst)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "service": "qtmesh-agents-web", "listen": s.listen,
	})
}

func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	list, err := s.deps.Projects.List(r.Context())
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	out := make([]map[string]any, 0, len(list))
	for _, p := range list {
		out = append(out, map[string]any{
			"id": p.ID, "name": p.Name, "path": p.Path, "active": p.Active,
		})
	}
	writeJSON(w, 200, out)
}

func (s *Server) handleListBenches(w http.ResponseWriter, r *http.Request) {
	list, err := s.deps.Orch.ListBenches(r.Context())
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	out := make([]map[string]any, 0, len(list))
	for _, b := range list {
		members, _ := s.deps.Store.ListBenchMembers(r.Context(), b.ID)
		ids := make([]string, 0, len(members))
		memberRows := make([]map[string]any, 0, len(members))
		for _, m := range members {
			ids = append(ids, m.AgentID)
			memberRows = append(memberRows, map[string]any{
				"agent_id": m.AgentID, "role": m.Role,
			})
		}
		row := map[string]any{
			"id": b.ID, "project_id": b.ProjectID, "name": b.Name,
			"leader_agent_id": b.LeaderAgentID, "status": b.Status,
			"members": ids, "roster": memberRows,
		}
		if g, err := s.deps.Store.LatestBenchGoal(r.Context(), b.ProjectID); err == nil {
			row["goal_id"] = g.ID
			row["goal_status"] = g.Status
			row["goal_objective"] = g.Objective
			row["goal_iteration"] = g.Iteration
			if msgs, err := s.deps.Store.ListThreadMessages(r.Context(), g.ThreadID, 8); err == nil {
				preview := make([]map[string]any, 0, len(msgs))
				for _, m := range msgs {
					preview = append(preview, map[string]any{
						"role": m.Role, "agent_id": m.AgentID, "content": m.Content,
						"created_at": m.CreatedAt.UTC().Format(time.RFC3339),
					})
				}
				row["thread_preview"] = preview
			}
		}
		out = append(out, row)
	}
	writeJSON(w, 200, out)
}

func (s *Server) handleListRuns(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 30
	}
	runs, err := s.deps.Store.ListRuns(r.Context(), limit)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	pid := r.URL.Query().Get("project_id")
	out := make([]map[string]any, 0, len(runs))
	for _, run := range runs {
		if pid != "" && run.ProjectID != pid {
			continue
		}
		out = append(out, runJSON(run))
	}
	writeJSON(w, 200, out)
}

func (s *Server) handleGetRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	run, err := s.deps.Store.GetRun(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	tasks, err := s.deps.Store.ListTasksByRun(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	row := runJSON(run)
	trows := make([]map[string]any, 0, len(tasks))
	for _, t := range tasks {
		trows = append(trows, map[string]any{
			"id": t.ID, "description": t.Description, "category": t.Category,
			"status": t.Status, "assigned_agent": t.AssignedAgent, "error": t.Error,
			"worktree_path": t.WorktreePath, "branch": t.Branch,
			"dependencies": t.Dependencies, "acceptance": t.Acceptance, "retry_count": t.RetryCount,
			"result_json": t.ResultJSON, "diff_path": t.DiffPath, "updated_at": t.UpdatedAt,
		})
	}
	row["tasks"] = trows
	writeJSON(w, 200, row)
}

func runJSON(run storage.Run) map[string]any {
	row := map[string]any{
		"id": run.ID, "project_id": run.ProjectID, "bench_id": run.BenchID,
		"objective": run.Objective, "status": run.Status, "planner": run.Planner,
		"work_dir": run.WorkDir, "summary": run.Summary, "error": run.Error,
		"created_at": run.CreatedAt.UTC().Format(time.RFC3339),
		"updated_at": run.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if run.EndedAt != nil {
		row["ended_at"] = run.EndedAt.UTC().Format(time.RFC3339)
	}
	return row
}

func (s *Server) handleListGoals(w http.ResponseWriter, r *http.Request) {
	pid := r.URL.Query().Get("project_id")
	list, err := s.deps.Store.ListBenchGoals(r.Context(), pid, 20)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	out := make([]map[string]any, 0, len(list))
	for _, g := range list {
		out = append(out, goalJSON(g))
	}
	writeJSON(w, 200, out)
}

func (s *Server) handleGetGoal(w http.ResponseWriter, r *http.Request) {
	g, err := s.deps.Store.GetBenchGoal(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	writeJSON(w, 200, goalJSON(g))
}

func goalJSON(g storage.BenchGoal) map[string]any {
	return map[string]any{
		"id": g.ID, "bench_id": g.BenchID, "project_id": g.ProjectID, "thread_id": g.ThreadID,
		"objective": g.Objective, "status": g.Status, "iteration": g.Iteration,
		"run_id": g.RunID, "error": g.Error,
		"created_at": g.CreatedAt.UTC().Format(time.RFC3339),
		"updated_at": g.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

func (s *Server) handleStartGoal(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProjectID     string `json:"project_id"`
		Objective     string `json:"objective"`
		AgentOverride string `json:"agent_override"`
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	goalID, err := s.deps.Conductor.StartBenchGoal(r.Context(), req.ProjectID, req.Objective, req.AgentOverride)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	s.hub.Broadcast("goal_started", map[string]any{"goal_id": goalID, "project_id": req.ProjectID})
	writeJSON(w, 200, map[string]any{"goal_id": goalID})
}

func (s *Server) handleThreadMessages(w http.ResponseWriter, r *http.Request) {
	threadID := r.URL.Query().Get("thread_id")
	pid := r.URL.Query().Get("project_id")
	if threadID == "" && pid != "" {
		if g, err := s.deps.Store.LatestBenchGoal(r.Context(), pid); err == nil {
			threadID = g.ThreadID
		}
	}
	if threadID == "" {
		http.Error(w, "thread_id or project_id required", 400)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 40
	}
	msgs, err := s.deps.Store.ListThreadMessages(r.Context(), threadID, limit)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	out := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, map[string]any{
			"id": m.ID, "role": m.Role, "agent_id": m.AgentID, "content": m.Content,
			"created_at": m.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	writeJSON(w, 200, out)
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Message   string `json:"message"`
		ProjectID string `json:"project_id"`
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	res, err := s.deps.Conductor.ChatWith(r.Context(), req.Message, orchestrator.ChatOpts{ProjectID: req.ProjectID})
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	actions := make([]map[string]any, 0, len(res.Actions))
	for _, a := range res.Actions {
		actions = append(actions, map[string]any{
			"type": a.Type, "project_id": a.ProjectID, "objective": a.Objective, "run_id": a.RunID,
		})
	}
	s.hub.Broadcast("chat", map[string]any{"project_id": req.ProjectID})
	writeJSON(w, 200, map[string]any{"reply": res.Reply, "actions": actions})
}

func (s *Server) handleListAttention(w http.ResponseWriter, r *http.Request) {
	list, err := s.deps.Orch.ListOpenAttention(r.Context())
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	out := make([]map[string]any, 0, len(list))
	for _, a := range list {
		out = append(out, map[string]any{
			"id": a.ID, "project_id": a.ProjectID, "bench_id": a.BenchID,
			"run_id": a.RunID, "task_id": a.TaskID, "agent_id": a.AgentID, "goal_id": a.GoalID,
			"kind": a.Kind, "message": a.Message, "status": a.Status,
			"created_at": a.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	writeJSON(w, 200, out)
}

func (s *Server) handleResolveAttention(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		AgentOverride string `json:"agent_override"`
		Resume        *bool  `json:"resume"` // default true when goal-linked
	}
	if r.ContentLength > 0 {
		_ = readJSON(r, &req)
	}
	resume := true
	if req.Resume != nil {
		resume = *req.Resume
	}
	agent := strings.TrimSpace(req.AgentOverride)

	// When the client picks an agent (or asks to control resume), skip the
	// daemon auto-resume callback and resume explicitly with the override.
	skip := agent != "" || req.Resume != nil
	a, err := s.deps.Orch.ResolveAttentionWith(r.Context(), id, orchestrator.ResolveAttentionOpts{SkipResume: skip})
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if skip && resume && a.GoalID != "" && s.deps.Conductor != nil {
		if err := s.deps.Conductor.ResumeBenchGoal(r.Context(), a.GoalID, agent); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
	}
	s.hub.Broadcast("attention_resolved", map[string]any{"id": id, "agent_override": agent})
	writeJSON(w, 200, map[string]any{"ok": true, "goal_id": a.GoalID, "agent_override": agent})
}

func (s *Server) handleTaskReply(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RunID                string `json:"run_id"`
		TaskID               string `json:"task_id"`
		Message              string `json:"message"`
		ElevateSandbox       bool   `json:"elevate_sandbox"`
		InteractiveApprovals bool   `json:"interactive_approvals"`
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	err := s.deps.Orch.ReplyToTask(r.Context(), req.RunID, req.TaskID, req.Message, orchestrator.TaskReplyOpts{
		ElevateSandbox:       req.ElevateSandbox,
		InteractiveApprovals: req.InteractiveApprovals,
	})
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	s.hub.Broadcast("task_reply", map[string]any{"run_id": req.RunID, "task_id": req.TaskID})
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	live := s.deps.Sessions.ListLive()
	out := make([]map[string]any, 0, len(live))
	for _, ls := range live {
		out = append(out, map[string]any{
			"id": ls.Info.ID, "agent_id": ls.Info.AgentID, "project_id": ls.Info.ProjectID,
			"work_dir": ls.Info.WorkDir, "status": ls.Info.Status, "pid": ls.Info.PID,
			"created_at": ls.Info.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	writeJSON(w, 200, out)
}

func (s *Server) handleSpawnSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AgentID   string `json:"agent_id"`
		ProjectID string `json:"project_id"`
		WorkDir   string `json:"work_dir"`
		Prompt    string `json:"prompt"`
		Cols      uint16 `json:"cols"`
		Rows      uint16 `json:"rows"`
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if req.Cols == 0 {
		req.Cols = 120
	}
	if req.Rows == 0 {
		req.Rows = 32
	}
	if req.WorkDir == "" && req.ProjectID != "" {
		if list, err := s.deps.Projects.List(r.Context()); err == nil {
			for _, p := range list {
				if p.ID == req.ProjectID {
					req.WorkDir = p.Path
					break
				}
			}
		}
	}
	ls, err := s.deps.Sessions.Spawn(r.Context(), sessions.SpawnOpts{
		AgentID: req.AgentID, ProjectID: req.ProjectID, WorkDir: req.WorkDir,
		Cols: req.Cols, Rows: req.Rows, Prompt: req.Prompt,
	})
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	s.hub.Broadcast("session_started", map[string]any{"id": ls.Info.ID, "agent_id": ls.Info.AgentID})
	writeJSON(w, 200, map[string]any{"session_id": ls.Info.ID, "pid": ls.Info.PID})
}

func (s *Server) handleStopSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.deps.Sessions.Stop(id); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	s.hub.Broadcast("session_stopped", map[string]any{"id": id})
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) handleListAgents(w http.ResponseWriter, r *http.Request) {
	ids := []string{"claude", "codex", "cursor", "mock"}
	out := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		ad, ok := s.deps.Registry.Get(id)
		if !ok {
			continue
		}
		det, _ := ad.Detect(r.Context())
		out = append(out, map[string]any{"id": id, "detected": det, "vendor": ad.Vendor()})
	}
	writeJSON(w, 200, out)
}

func (s *Server) handleListProfiles(w http.ResponseWriter, r *http.Request) {
	list, err := s.deps.Store.ListAgentProfiles(r.Context())
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	out := make([]map[string]any, 0, len(list))
	for _, p := range list {
		out = append(out, profileJSON(p))
	}
	writeJSON(w, 200, out)
}

func (s *Server) handleUpsertProfile(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		AgentID   string `json:"agent_id"`
		Role      string `json:"role"`
		Traits    string `json:"traits"`
		Context   string `json:"context"`
		ProjectID string `json:"project_id"`
		WorkDir   string `json:"work_dir"`
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if req.Name == "" || req.AgentID == "" {
		http.Error(w, "name and agent_id required", 400)
		return
	}
	if req.ID == "" {
		req.ID = fmt.Sprintf("prof-%d", time.Now().UnixNano())
	}
	p := storage.AgentProfile{
		ID: req.ID, Name: req.Name, AgentID: req.AgentID, Role: req.Role,
		Traits: req.Traits, Context: req.Context, ProjectID: req.ProjectID, WorkDir: req.WorkDir,
	}
	if err := s.deps.Store.UpsertAgentProfile(r.Context(), p); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, 200, profileJSON(p))
}

func (s *Server) handleDeleteProfile(w http.ResponseWriter, r *http.Request) {
	if err := s.deps.Store.DeleteAgentProfile(r.Context(), r.PathValue("id")); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) handleSpawnProfile(w http.ResponseWriter, r *http.Request) {
	p, err := s.deps.Store.GetAgentProfile(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	prompt := strings.TrimSpace(p.Role + "\n" + p.Traits + "\n" + p.Context)
	ls, err := s.deps.Sessions.Spawn(r.Context(), sessions.SpawnOpts{
		AgentID: p.AgentID, ProjectID: p.ProjectID, WorkDir: p.WorkDir,
		Cols: 120, Rows: 32, Prompt: prompt,
	})
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	s.hub.Broadcast("session_started", map[string]any{"id": ls.Info.ID, "profile_id": p.ID})
	writeJSON(w, 200, map[string]any{"session_id": ls.Info.ID, "pid": ls.Info.PID})
}

func profileJSON(p storage.AgentProfile) map[string]any {
	return map[string]any{
		"id": p.ID, "name": p.Name, "agent_id": p.AgentID, "role": p.Role,
		"traits": p.Traits, "context": p.Context, "project_id": p.ProjectID, "work_dir": p.WorkDir,
		"created_at": p.CreatedAt.UTC().Format(time.RFC3339),
		"updated_at": p.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

func (s *Server) handleEnsureBench(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProjectID string   `json:"project_id"`
		Workers   []string `json:"workers"`
		Leader    string   `json:"leader"`
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if req.ProjectID == "" {
		http.Error(w, "project_id required", 400)
		return
	}
	name := req.ProjectID
	if list, err := s.deps.Projects.List(r.Context()); err == nil {
		for _, p := range list {
			if p.ID == req.ProjectID {
				name = p.Name
				break
			}
		}
	}
	b, err := s.deps.Orch.EnsureBench(r.Context(), req.ProjectID, name, req.Workers)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if lid := strings.TrimSpace(req.Leader); lid != "" && lid != b.LeaderAgentID {
		if err := s.deps.Orch.SetBenchLeader(r.Context(), b.ID, lid); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		b.LeaderAgentID = lid
	}
	for _, wkr := range req.Workers {
		_ = s.deps.Orch.AddBenchWorker(r.Context(), b.ID, wkr)
	}
	s.hub.Broadcast("bench", map[string]any{"id": b.ID, "project_id": b.ProjectID})
	writeJSON(w, 200, map[string]any{
		"id": b.ID, "project_id": b.ProjectID, "name": b.Name, "leader_agent_id": b.LeaderAgentID, "status": b.Status,
	})
}

func (s *Server) handleSetBenchLeader(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AgentID string `json:"agent_id"`
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if err := s.deps.Orch.SetBenchLeader(r.Context(), r.PathValue("id"), strings.TrimSpace(req.AgentID)); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	s.hub.Broadcast("bench_leader", map[string]any{"bench_id": r.PathValue("id"), "agent_id": req.AgentID})
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) handleAddBenchWorker(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AgentID string `json:"agent_id"`
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if err := s.deps.Orch.AddBenchWorker(r.Context(), r.PathValue("id"), strings.TrimSpace(req.AgentID)); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	s.hub.Broadcast("bench_worker", map[string]any{"bench_id": r.PathValue("id"), "agent_id": req.AgentID, "op": "add"})
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) handleRemoveBenchWorker(w http.ResponseWriter, r *http.Request) {
	benchID := r.PathValue("id")
	agentID := r.PathValue("agent")
	if err := s.deps.Orch.RemoveBenchWorker(r.Context(), benchID, agentID); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	s.hub.Broadcast("bench_worker", map[string]any{"bench_id": benchID, "agent_id": agentID, "op": "remove"})
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) handleResumeGoal(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AgentOverride string `json:"agent_override"`
	}
	_ = readJSON(r, &req)
	goalID := r.PathValue("id")
	if err := s.deps.Conductor.ResumeBenchGoal(r.Context(), goalID, strings.TrimSpace(req.AgentOverride)); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	s.hub.Broadcast("goal_resumed", map[string]any{"goal_id": goalID, "agent_override": req.AgentOverride})
	writeJSON(w, 200, map[string]any{"ok": true, "goal_id": goalID})
}

func (s *Server) handleGetOrchestrator(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{
		"orch_agent":  s.deps.Conductor.AdapterID(),
		"force_agent": s.deps.Conductor.ForceAgent(),
	})
}

func (s *Server) handleSetOrchestrator(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OrchAgent  *string `json:"orch_agent"`  // conversational brain; "" clears to heuristic
		ForceAgent *string `json:"force_agent"` // worker override for goals/runs; "" clears
	}
	if err := readJSON(r, &req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if req.OrchAgent != nil {
		id := strings.TrimSpace(*req.OrchAgent)
		if id == "" || id == "heuristic" {
			s.deps.Conductor.SetAdapter(nil)
		} else {
			ad, ok := s.deps.Registry.Get(id)
			if !ok {
				http.Error(w, "unknown agent "+id, 400)
				return
			}
			if det, _ := ad.Detect(r.Context()); !det {
				http.Error(w, "agent "+id+" not detected on PATH", 400)
				return
			}
			s.deps.Conductor.SetAdapter(ad)
		}
	}
	if req.ForceAgent != nil {
		s.deps.Conductor.SetForceAgent(strings.TrimSpace(*req.ForceAgent))
	}
	s.hub.Broadcast("orchestrator", map[string]any{
		"orch_agent": s.deps.Conductor.AdapterID(), "force_agent": s.deps.Conductor.ForceAgent(),
	})
	writeJSON(w, 200, map[string]any{
		"ok": true, "orch_agent": s.deps.Conductor.AdapterID(), "force_agent": s.deps.Conductor.ForceAgent(),
	})
}
