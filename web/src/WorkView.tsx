import { useEffect, useState } from "react";
import { api } from "./api";

type Task = {
  id: string; description: string; status: string; assigned_agent: string;
  dependencies: string[] | null; acceptance: string[] | null; retry_count: number;
  error: string; result_json: string; worktree_path: string; diff_path: string;
};
type Run = { id: string; objective: string; status: string; summary: string; error: string; tasks?: Task[] };

function output(raw: string): string {
  try {
    const data = JSON.parse(raw);
    return data.output || data.brief || JSON.stringify(data, null, 2);
  } catch { return raw || "No output yet."; }
}

export function WorkView({ projectId }: { projectId: string }) {
  const [runs, setRuns] = useState<Run[]>([]);
  const [selected, setSelected] = useState("");
  const [run, setRun] = useState<Run | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    let active = true;
    let timer: ReturnType<typeof setTimeout>;
    setRun(null);
    setRuns([]);
    async function refresh() {
      try {
        const list = await api<Run[]>(`/api/runs?project_id=${encodeURIComponent(projectId)}`);
        if (!active) return;
        setRuns(list);
        const id = list.some(r => r.id === selected) ? selected : list[0]?.id;
        const detail = id ? await api<Run>(`/api/runs/${encodeURIComponent(id)}`) : null;
        if (!active) return;
        setRun(detail);
        setError("");
      } catch (e) {
        if (active) setError(e instanceof Error ? e.message : String(e));
      } finally {
        if (active) timer = setTimeout(refresh, 2000);
      }
    }
    void refresh();
    return () => { active = false; clearTimeout(timer); };
  }, [projectId, selected]);

  return <div className="panel-body work-view">
    <h2>Delegated work</h2>
    <p className="muted">Follow the result here. Expand any task to inspect its output and workspace.</p>
    {error && <p role="alert" className="err">{error}</p>}
    <label>Run <select value={run?.id || ""} onChange={e => setSelected(e.target.value)}>
      {!runs.length && <option value="">No runs yet</option>}
      {runs.map(r => <option key={r.id} value={r.id}>{r.status} · {r.objective}</option>)}
    </select></label>
    {run && <>
      <h3>{run.objective}</h3>
      <p><span className="badge">{run.status}</span> {run.summary}</p>
      {run.error && <p className="err">{run.error}</p>}
      {(run.tasks || []).map(task => <details className="work-task" key={task.id}>
        <summary><span className="badge">{task.status}</span> {task.description}
          <span className="meta">{task.assigned_agent || "unassigned"} · retries {task.retry_count}</span>
        </summary>
        <p className="muted">Task: {task.id}</p>
        {!!task.dependencies?.length && <p>Depends on: {task.dependencies.join(", ")}</p>}
        {!!task.acceptance?.length && <ul>{task.acceptance.map((a, i) => <li key={i}>{a}</li>)}</ul>}
        {task.error && <p className="err">{task.error}</p>}
        {task.worktree_path && <p>Workspace: <code>{task.worktree_path}</code></p>}
        {task.diff_path && <p>Captured patch: <code>{task.diff_path}</code></p>}
        <pre className="work-output">{output(task.result_json)}</pre>
      </details>)}
    </>}
  </div>;
}
