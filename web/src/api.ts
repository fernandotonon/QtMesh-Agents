const TOKEN_KEY = "qma_token";

export function getToken(): string {
  const q = new URLSearchParams(location.search).get("token");
  if (q) {
    localStorage.setItem(TOKEN_KEY, q);
    const u = new URL(location.href);
    u.searchParams.delete("token");
    history.replaceState({}, "", u.pathname + u.search + u.hash);
  }
  return localStorage.getItem(TOKEN_KEY) || "";
}

export function setToken(t: string) {
  localStorage.setItem(TOKEN_KEY, t.trim());
}

export async function api<T = unknown>(
  path: string,
  init: RequestInit = {},
): Promise<T> {
  const token = getToken();
  const headers = new Headers(init.headers);
  if (token) headers.set("Authorization", `Bearer ${token}`);
  if (init.body && !headers.has("Content-Type")) {
    headers.set("Content-Type", "application/json");
  }
  const res = await fetch(path, { ...init, headers });
  if (!res.ok) {
    const text = await res.text();
    throw new Error(text || res.statusText);
  }
  if (res.status === 204) return undefined as T;
  return res.json() as Promise<T>;
}

export function wsURL(path: string): string {
  const proto = location.protocol === "https:" ? "wss:" : "ws:";
  const token = encodeURIComponent(getToken());
  const sep = path.includes("?") ? "&" : "?";
  return `${proto}//${location.host}${path}${sep}token=${token}`;
}

export type Project = { id: string; name: string; path: string; active: boolean };
export type BenchMember = { agent_id: string; role: string };

export type Bench = {
  id: string;
  project_id: string;
  name: string;
  leader_agent_id: string;
  status: string;
  members: string[];
  roster?: BenchMember[];
  goal_id?: string;
  goal_status?: string;
  goal_objective?: string;
  goal_iteration?: number;
  thread_preview?: { role: string; agent_id: string; content: string; created_at: string }[];
};

export type OrchSettings = {
  orch_agent: string;
  force_agent: string;
};
export type Attention = {
  id: string;
  project_id: string;
  bench_id: string;
  run_id: string;
  task_id: string;
  agent_id: string;
  goal_id: string;
  kind: string;
  message: string;
  status: string;
  created_at: string;
};
export type Session = {
  id: string;
  agent_id: string;
  project_id: string;
  work_dir: string;
  status: string;
  pid: number;
};
export type Profile = {
  id: string;
  name: string;
  agent_id: string;
  role: string;
  traits: string;
  context: string;
  project_id: string;
  work_dir: string;
};
export type ThreadMsg = {
  id: string;
  role: string;
  agent_id: string;
  content: string;
  created_at: string;
};
