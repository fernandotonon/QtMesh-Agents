import { useCallback, useEffect, useMemo, useState } from "react";
import ReactMarkdown from "react-markdown";
import {
  api,
  getToken,
  setToken,
  wsURL,
  type Attention,
  type Bench,
  type OrchSettings,
  type Profile,
  type Project,
  type Session,
  type ThreadMsg,
} from "./api";
import { WorkView } from "./WorkView";
import { TermPane } from "./Terminal";

type Tab = "work" | "chat" | "thread" | "attention" | "team" | "profiles";

type ChatLine = { role: "user" | "assistant"; content: string };

export default function App() {
  const [token, setTok] = useState(getToken());
  const [connected, setConnected] = useState(false);
  const [error, setError] = useState("");
  const [projects, setProjects] = useState<Project[]>([]);
  const [benches, setBenches] = useState<Bench[]>([]);
  const [attention, setAttention] = useState<Attention[]>([]);
  const [sessions, setSessions] = useState<Session[]>([]);
  const [profiles, setProfiles] = useState<Profile[]>([]);
  const [agents, setAgents] = useState<{ id: string; detected: boolean }[]>([]);
  const [orch, setOrch] = useState<OrchSettings>({ orch_agent: "", force_agent: "" });
  const [projectId, setProjectId] = useState("");
  const [tab, setTab] = useState<Tab>("chat");
  const [chat, setChat] = useState<ChatLine[]>([]);
  const [draft, setDraft] = useState("");
  const [busy, setBusy] = useState(false);
  const [thread, setThread] = useState<ThreadMsg[]>([]);
  const [reply, setReply] = useState("");
  const [resumeAgent, setResumeAgent] = useState("cursor");
  const [addWorker, setAddWorker] = useState("codex");
  const [activeSession, setActiveSession] = useState("");
  const [spawnAgent, setSpawnAgent] = useState("codex");
  const [profDraft, setProfDraft] = useState({
    name: "",
    agent_id: "codex",
    role: "",
    traits: "",
    context: "",
  });

  const refresh = useCallback(async () => {
    if (!getToken()) return;
    try {
      const [p, b, a, s, pr, ag, o] = await Promise.all([
        api<Project[]>("/api/projects"),
        api<Bench[]>("/api/benches"),
        api<Attention[]>("/api/attention"),
        api<Session[]>("/api/sessions"),
        api<Profile[]>("/api/profiles"),
        api<{ id: string; detected: boolean }[]>("/api/agents"),
        api<OrchSettings>("/api/orchestrator"),
      ]);
      setProjects(p);
      setBenches(b);
      setAttention(a);
      setSessions(s);
      setProfiles(pr);
      setAgents(ag);
      setOrch(o);
      setConnected(true);
      setError("");
      if (!projectId && p.length) {
        const active = p.find((x) => x.active) || p[0];
        setProjectId(active.id);
      }
      if (!activeSession && s.length) setActiveSession(s[0].id);
    } catch (e) {
      setConnected(false);
      setError(e instanceof Error ? e.message : String(e));
    }
  }, [projectId, activeSession]);

  const loadThread = useCallback(async (pid: string) => {
    if (!pid) {
      setThread([]);
      return;
    }
    try {
      const msgs = await api<ThreadMsg[]>(
        `/api/threads?project_id=${encodeURIComponent(pid)}&limit=60`,
      );
      setThread(msgs);
    } catch {
      setThread([]);
    }
  }, []);

  useEffect(() => {
    if (!token) return;
    void refresh();
    const t = setInterval(() => void refresh(), 8000);
    return () => clearInterval(t);
  }, [token, refresh]);

  useEffect(() => {
    if (projectId) void loadThread(projectId);
  }, [projectId, loadThread]);

  useEffect(() => {
    if (!token) return;
    let ws: WebSocket | null = null;
    let closed = false;
    const connect = () => {
      ws = new WebSocket(wsURL("/ws/events"));
      ws.onopen = () => setConnected(true);
      ws.onmessage = () => {
        void refresh();
        if (projectId) void loadThread(projectId);
      };
      ws.onclose = () => {
        setConnected(false);
        if (!closed) setTimeout(connect, 2500);
      };
    };
    connect();
    return () => {
      closed = true;
      ws?.close();
    };
  }, [token, refresh, loadThread, projectId]);

  const projectBenches = useMemo(
    () => benches.filter((b) => !projectId || b.project_id === projectId),
    [benches, projectId],
  );

  if (!token) {
    return (
      <div className="token-gate">
        <h1>QtMesh Agents</h1>
        <p>Enter the daemon HTTP auth token (from <code>qma web token</code>).</p>
        <input
          type="password"
          placeholder="Bearer token"
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              const v = (e.target as HTMLInputElement).value;
              setToken(v);
              setTok(v);
            }
          }}
        />
        <div className="row">
          <button
            className="primary"
            onClick={() => {
              const el = document.querySelector<HTMLInputElement>(".token-gate input");
              const v = el?.value || "";
              setToken(v);
              setTok(v);
            }}
          >
            Continue
          </button>
        </div>
      </div>
    );
  }

  async function sendChat() {
    const text = draft.trim();
    if (!text || busy) return;
    setBusy(true);
    setChat((c) => [...c, { role: "user", content: text }]);
    setDraft("");
    try {
      const res = await api<{ reply: string }>("/api/chat", {
        method: "POST",
        body: JSON.stringify({ message: text, project_id: projectId }),
      });
      setChat((c) => [...c, { role: "assistant", content: res.reply }]);
      await refresh();
      await loadThread(projectId);
    } catch (e) {
      setChat((c) => [
        ...c,
        { role: "assistant", content: e instanceof Error ? e.message : String(e) },
      ]);
    } finally {
      setBusy(false);
    }
  }

  async function resolveAttn(id: string, withAgent = false) {
    await api(`/api/attention/${id}/resolve`, {
      method: "POST",
      body: JSON.stringify(
        withAgent
          ? { agent_override: resumeAgent || orch.force_agent || "", resume: true }
          : { resume: false },
      ),
    });
    await refresh();
  }

  async function replyTask(a: Attention) {
    if (!reply.trim()) return;
    await api("/api/tasks/reply", {
      method: "POST",
      body: JSON.stringify({
        run_id: a.run_id,
        task_id: a.task_id,
        message: reply,
      }),
    });
    setReply("");
    await refresh();
  }

  async function setOrchAgent(field: "orch_agent" | "force_agent", value: string) {
    const body =
      field === "orch_agent" ? { orch_agent: value } : { force_agent: value };
    const next = await api<OrchSettings & { ok: boolean }>("/api/orchestrator", {
      method: "POST",
      body: JSON.stringify(body),
    });
    setOrch({ orch_agent: next.orch_agent, force_agent: next.force_agent });
  }

  const selectedBench = useMemo(
    () => benches.find((b) => b.project_id === projectId) || null,
    [benches, projectId],
  );

  async function ensureBench() {
    if (!projectId) return;
    await api("/api/benches", {
      method: "POST",
      body: JSON.stringify({ project_id: projectId }),
    });
    await refresh();
  }

  async function setLeader(agentId: string) {
    if (!selectedBench) return;
    await api(`/api/benches/${selectedBench.id}/leader`, {
      method: "POST",
      body: JSON.stringify({ agent_id: agentId }),
    });
    await refresh();
  }

  async function addBenchWorker(agentId: string) {
    if (!selectedBench || !agentId) return;
    await api(`/api/benches/${selectedBench.id}/workers`, {
      method: "POST",
      body: JSON.stringify({ agent_id: agentId }),
    });
    await refresh();
  }

  async function removeBenchWorker(agentId: string) {
    if (!selectedBench) return;
    await api(`/api/benches/${selectedBench.id}/workers/${encodeURIComponent(agentId)}`, {
      method: "DELETE",
    });
    await refresh();
  }

  async function spawn() {
    const res = await api<{ session_id: string }>("/api/sessions", {
      method: "POST",
      body: JSON.stringify({ agent_id: spawnAgent, project_id: projectId }),
    });
    setActiveSession(res.session_id);
    await refresh();
  }

  async function stopSession(id: string) {
    await api(`/api/sessions/${id}/stop`, { method: "POST" });
    await refresh();
  }

  async function saveProfile() {
    if (!profDraft.name) return;
    await api("/api/profiles", {
      method: "POST",
      body: JSON.stringify({ ...profDraft, project_id: projectId }),
    });
    setProfDraft({ name: "", agent_id: "codex", role: "", traits: "", context: "" });
    await refresh();
  }

  async function spawnProfile(id: string) {
    const res = await api<{ session_id: string }>(`/api/profiles/${id}/spawn`, {
      method: "POST",
    });
    setActiveSession(res.session_id);
    await refresh();
  }

  return (
    <div className="app">
      <header className="topbar">
        <div className="brand">QtMesh Agents</div>
        <span className={`badge ${connected ? "ok" : "err"}`}>
          {connected ? "connected" : "offline"}
        </span>
        <span className={`badge ${attention.length ? "warn" : ""}`}>
          Needs you · {attention.length}
        </span>
        <select
          style={{ width: "auto", minWidth: 160 }}
          value={projectId}
          onChange={(e) => setProjectId(e.target.value)}
        >
          <option value="">All projects</option>
          {projects.map((p) => (
            <option key={p.id} value={p.id}>
              {p.name}
            </option>
          ))}
        </select>
        <label className="muted" style={{ fontSize: 12 }}>
          Orch brain
          <select
            style={{ width: "auto", minWidth: 110, marginLeft: 6 }}
            value={orch.orch_agent || ""}
            onChange={(e) => void setOrchAgent("orch_agent", e.target.value)}
          >
            <option value="">heuristic</option>
            {agents.map((a) => (
              <option key={a.id} value={a.id} disabled={!a.detected}>
                {a.id}
              </option>
            ))}
          </select>
        </label>
        <label className="muted" style={{ fontSize: 12 }}>
          Force worker
          <select
            style={{ width: "auto", minWidth: 110, marginLeft: 6 }}
            value={orch.force_agent || ""}
            onChange={(e) => void setOrchAgent("force_agent", e.target.value)}
          >
            <option value="">auto-route</option>
            {agents.map((a) => (
              <option key={a.id} value={a.id} disabled={!a.detected}>
                {a.id}
              </option>
            ))}
          </select>
        </label>
        <div className="spacer" />
        <button
          onClick={() => {
            setToken("");
            setTok("");
          }}
        >
          Clear token
        </button>
      </header>

      {error && (
        <div style={{ padding: "0.5rem 1rem" }} className="err">
          {error}
        </div>
      )}

      <div className="layout">
        <aside className="sidebar">
          <div className="panel-head">Board</div>
          <div className="panel-body">
            {projectBenches.length === 0 && (
              <div className="empty">No benches yet — chat to start a goal.</div>
            )}
            {projectBenches.map((b) => (
              <button
                key={b.id}
                className={`nav-item ${b.project_id === projectId ? "active" : ""}`}
                onClick={() => {
                  setProjectId(b.project_id);
                  setTab("thread");
                }}
              >
                <strong>{b.name || b.project_id}</strong>
                <span className="meta">
                  leader {b.leader_agent_id || "—"} · {b.goal_status || b.status}
                  {b.goal_iteration != null ? ` · iter ${b.goal_iteration}` : ""}
                </span>
                {b.goal_objective && (
                  <span className="meta">{b.goal_objective.slice(0, 120)}</span>
                )}
              </button>
            ))}
          </div>
        </aside>

        <main className="main">
          <div className="tabs">
            {(
              [
                ["chat", "Chat"],
                ["thread", "Bench thread"],
                ["work", "Work"],
                ["attention", `Needs you (${attention.length})`],
                ["team", "Team"],
                ["profiles", "Profiles"],
              ] as [Tab, string][]
            ).map(([id, label]) => (
              <button
                key={id}
                className={`tab ${tab === id ? "active" : ""}`}
                onClick={() => setTab(id)}
              >
                {label}
              </button>
            ))}
          </div>

          {tab === "work" && <WorkView projectId={projectId} />}

          {tab === "chat" && (
            <>
              <div className="panel-body chat-log">
                {chat.length === 0 && (
                  <div className="empty">
                    Talk to the orchestrator. Copy/paste and selection work here.
                  </div>
                )}
                {chat.map((m, i) => (
                  <div key={i} className="msg">
                    <div className="who">{m.role}</div>
                    <div className="body">
                      <ReactMarkdown>{m.content}</ReactMarkdown>
                    </div>
                  </div>
                ))}
              </div>
              <div className="composer">
                <textarea
                  value={draft}
                  onChange={(e) => setDraft(e.target.value)}
                  placeholder="Message the orchestrator…"
                  onKeyDown={(e) => {
                    if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
                      e.preventDefault();
                      void sendChat();
                    }
                  }}
                />
                <button className="primary" disabled={busy} onClick={() => void sendChat()}>
                  Send
                </button>
              </div>
            </>
          )}

          {tab === "thread" && (
            <div className="panel-body chat-log">
              {thread.length === 0 && <div className="empty">No thread messages.</div>}
              {thread.map((m) => (
                <div key={m.id} className="msg">
                  <div className="who">
                    {m.role}
                    {m.agent_id ? ` · ${m.agent_id}` : ""} · {m.created_at}
                  </div>
                  <div className="body">
                    <ReactMarkdown>{m.content}</ReactMarkdown>
                  </div>
                </div>
              ))}
            </div>
          )}

          {tab === "attention" && (
            <div className="panel-body">
              {attention.length === 0 && <div className="empty">Inbox clear.</div>}
              {attention.map((a) => (
                <div key={a.id} className="attn">
                  <div className="kind">{a.kind}</div>
                  <div>{a.message}</div>
                  <div className="muted" style={{ fontSize: 12, marginTop: 4 }}>
                    {a.agent_id} · run {a.run_id} · task {a.task_id}
                    {a.goal_id ? ` · goal ${a.goal_id}` : ""}
                  </div>
                  <div className="row">
                    <input
                      value={reply}
                      onChange={(e) => setReply(e.target.value)}
                      placeholder="Reply to agent…"
                    />
                    <button
                      className="primary"
                      onClick={() => void replyTask(a)}
                      disabled={!a.task_id}
                    >
                      Reply
                    </button>
                  </div>
                  <div className="row">
                    <select
                      style={{ width: "auto" }}
                      value={resumeAgent}
                      onChange={(e) => setResumeAgent(e.target.value)}
                    >
                      {agents.map((ag) => (
                        <option key={ag.id} value={ag.id}>
                          {ag.id}
                        </option>
                      ))}
                    </select>
                    <button
                      className="primary"
                      onClick={() => void resolveAttn(a.id, true)}
                      title="Resolve and resume goal forcing the selected agent"
                    >
                      Resume with agent
                    </button>
                    <button onClick={() => void resolveAttn(a.id, false)}>Resolve only</button>
                  </div>
                </div>
              ))}
            </div>
          )}

          {tab === "team" && (
            <div className="panel-body">
              <div className="attn">
                <strong>Orchestrator</strong>
                <p className="muted">
                  Brain answers chat / plans goals. Force worker seats every unit onto one
                  vendor (like <code>qma run --agent</code>).
                </p>
                <div className="row">
                  <label>
                    Brain
                    <select
                      value={orch.orch_agent || ""}
                      onChange={(e) => void setOrchAgent("orch_agent", e.target.value)}
                    >
                      <option value="">heuristic</option>
                      {agents.map((a) => (
                        <option key={a.id} value={a.id} disabled={!a.detected}>
                          {a.id}
                          {!a.detected ? " (missing)" : ""}
                        </option>
                      ))}
                    </select>
                  </label>
                  <label>
                    Force worker
                    <select
                      value={orch.force_agent || ""}
                      onChange={(e) => void setOrchAgent("force_agent", e.target.value)}
                    >
                      <option value="">auto-route</option>
                      {agents.map((a) => (
                        <option key={a.id} value={a.id} disabled={!a.detected}>
                          {a.id}
                        </option>
                      ))}
                    </select>
                  </label>
                </div>
              </div>

              {!projectId && (
                <div className="empty">Select a project in the top bar to edit its bench.</div>
              )}
              {projectId && !selectedBench && (
                <div className="attn">
                  <p>No bench for this project yet.</p>
                  <button className="primary" onClick={() => void ensureBench()}>
                    Open bench
                  </button>
                </div>
              )}
              {selectedBench && (
                <div className="attn">
                  <strong>{selectedBench.name}</strong>
                  <div className="muted" style={{ marginBottom: "0.75rem" }}>
                    {selectedBench.id} · {selectedBench.status}
                  </div>
                  <label>
                    Leader
                    <select
                      value={selectedBench.leader_agent_id || ""}
                      onChange={(e) => void setLeader(e.target.value)}
                    >
                      {!selectedBench.leader_agent_id && <option value="">—</option>}
                      {agents.map((a) => (
                        <option key={a.id} value={a.id} disabled={!a.detected}>
                          {a.id}
                        </option>
                      ))}
                    </select>
                  </label>
                  <div style={{ marginTop: "0.75rem" }}>
                    <div className="muted" style={{ marginBottom: 6 }}>
                      Workers
                    </div>
                    {(selectedBench.roster || []).filter((m) => m.role !== "leader" || m.agent_id !== selectedBench.leader_agent_id).length === 0 &&
                      (selectedBench.members || []).length === 0 && (
                        <div className="empty">No workers seated.</div>
                      )}
                    <ul style={{ listStyle: "none", padding: 0, margin: 0 }}>
                      {(selectedBench.roster && selectedBench.roster.length
                        ? selectedBench.roster
                        : (selectedBench.members || []).map((id) => ({
                            agent_id: id,
                            role: id === selectedBench.leader_agent_id ? "leader" : "worker",
                          }))
                      ).map((m) => (
                        <li
                          key={m.agent_id}
                          className="row"
                          style={{ justifyContent: "space-between", marginBottom: 6 }}
                        >
                          <span>
                            <strong>{m.agent_id}</strong>{" "}
                            <span className="muted">({m.role})</span>
                          </span>
                          {m.agent_id !== selectedBench.leader_agent_id && (
                            <button
                              className="danger"
                              onClick={() => void removeBenchWorker(m.agent_id)}
                            >
                              Remove
                            </button>
                          )}
                        </li>
                      ))}
                    </ul>
                    <div className="row" style={{ marginTop: 8 }}>
                      <select
                        style={{ width: "auto" }}
                        value={addWorker}
                        onChange={(e) => setAddWorker(e.target.value)}
                      >
                        {agents.map((a) => (
                          <option key={a.id} value={a.id} disabled={!a.detected}>
                            {a.id}
                          </option>
                        ))}
                      </select>
                      <button className="primary" onClick={() => void addBenchWorker(addWorker)}>
                        Add worker
                      </button>
                    </div>
                  </div>
                </div>
              )}
            </div>
          )}

          {tab === "profiles" && (
            <div className="panel-body">
              <div className="form-grid" style={{ marginBottom: "1rem" }}>
                <input
                  placeholder="Profile name"
                  value={profDraft.name}
                  onChange={(e) => setProfDraft({ ...profDraft, name: e.target.value })}
                />
                <select
                  value={profDraft.agent_id}
                  onChange={(e) => setProfDraft({ ...profDraft, agent_id: e.target.value })}
                >
                  {agents.map((a) => (
                    <option key={a.id} value={a.id}>
                      {a.id}
                      {a.detected ? "" : " (missing)"}
                    </option>
                  ))}
                </select>
                <input
                  placeholder="Role"
                  value={profDraft.role}
                  onChange={(e) => setProfDraft({ ...profDraft, role: e.target.value })}
                />
                <textarea
                  placeholder="Traits"
                  value={profDraft.traits}
                  onChange={(e) => setProfDraft({ ...profDraft, traits: e.target.value })}
                />
                <textarea
                  placeholder="Context"
                  value={profDraft.context}
                  onChange={(e) => setProfDraft({ ...profDraft, context: e.target.value })}
                />
                <button className="primary" onClick={() => void saveProfile()}>
                  Save profile
                </button>
              </div>
              {profiles.map((p) => (
                <div key={p.id} className="attn">
                  <strong>{p.name}</strong>
                  <div className="muted">
                    {p.agent_id} · {p.role}
                  </div>
                  <div className="row">
                    <button className="primary" onClick={() => void spawnProfile(p.id)}>
                      Spawn
                    </button>
                    <button
                      className="danger"
                      onClick={() =>
                        void api(`/api/profiles/${p.id}`, { method: "DELETE" }).then(refresh)
                      }
                    >
                      Delete
                    </button>
                  </div>
                </div>
              ))}
            </div>
          )}
        </main>

        <aside className="dock">
          <div className="panel-head">Terminals</div>
          <div className="term-tabs">
            <select
              style={{ width: "auto" }}
              value={spawnAgent}
              onChange={(e) => setSpawnAgent(e.target.value)}
            >
              {agents.map((a) => (
                <option key={a.id} value={a.id}>
                  {a.id}
                </option>
              ))}
            </select>
            <button className="primary" onClick={() => void spawn()}>
              Spawn
            </button>
            {sessions.map((s) => (
              <button
                key={s.id}
                className={`tab ${activeSession === s.id ? "active" : ""}`}
                onClick={() => setActiveSession(s.id)}
              >
                {s.agent_id}:{s.id.slice(-6)}
              </button>
            ))}
            {activeSession && (
              <button className="danger" onClick={() => void stopSession(activeSession)}>
                Stop
              </button>
            )}
          </div>
          {sessions.length === 0 && (
            <div className="empty" style={{ padding: "0.75rem" }}>
              Spawn an agent to attach a live PTY. Closing this tab does not kill sessions.
            </div>
          )}
          {sessions.map((s) => (
            <TermPane key={s.id} sessionId={s.id} active={s.id === activeSession} />
          ))}
        </aside>
      </div>
    </div>
  );
}
