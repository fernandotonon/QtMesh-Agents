# QtMesh Agents Architecture

**Product:** QtMesh Agents (`qma`)  
**Tagline:** One terminal. Multiple AI agents. Unified orchestration.  
**Module:** `github.com/fernandotonon/QtMesh-Agents`

## Overview

QtMesh Agents is a terminal-first orchestrator for multi-vendor AI coding agents. A persistent background daemon owns agent processes and PTYs. CLI and TUI clients connect over local IPC and never own agent lifetimes.

```text
┌─────────────┐   ┌─────────────┐   ┌──────────────┐
│  qma (CLI)  │   │  qma (TUI)  │   │ Browser SPA  │
└──────┬──────┘   └──────┬──────┘   └──────┬───────┘
       │  Unix socket         │             │ HTTP/WS + Bearer
       └──────────┬───────────┘             │
                  └────────────┬────────────┘
                               ▼
                      ┌────────────────┐
                      │  qma daemon    │
                      │  sessions/PTY  │
                      │  orchestrator  │
                      │  SQLite store  │
                      │  web gateway   │
                      └───────┬────────┘
                              │
                 ┌────────────┼────────────┐
                 ▼            ▼            ▼
          Claude CLI   Codex CLI   Cursor Agent
```

Web workspace details: [web-workspace.md](web-workspace.md).

## Design principles

1. **Daemon owns processes.** Closing the TUI never kills agents.
2. **Vendor-independent adapters.** Optional capability interfaces; no forced feature parity.
3. **Structured plans.** Orchestration uses validated DAGs, not free-form text.
4. **Isolation by default.** Autonomous tasks prefer Git worktrees.
5. **Human gates.** Merges and destructive ops require approval.
6. **Small interfaces.** Defined close to consumers; DI via constructors.

## Project benches

Hierarchy:

```text
Top orchestrator (conductor)
  └── Project bench (one per registered project)
        ├── Leader agent (plans / prioritizes)
        └── Worker agents (implement / test / review)
```

- Starting a run calls `EnsureBench` — seats a leader (`routing.leadershipFallback`,
  default Claude → Cursor → Codex) and detected workers. If the seated leader hits
  quota/token exhaustion, the next available entry is reseated automatically.
- Plans begin with a `lead` (leadership) task, then discover → impl → verify → integrate.
- Codex workers run with `approval_policy=never` (unattended). Default sandbox is
  `danger-full-access` so local GPU tools (qtmesh/TRELLIS/Metal) work; `workspace-write`
  seatbelt blocks OpenGL (`CGLDescribeRenderer` / exit 139) and used to stall on fake
  “needs graphics session” prompts. If a worker still hits a GPU sandbox block, the
  executor auto-retries once with elevated sandbox instead of asking the human.
  (`--approve-for-me` alone leaves `approval: on-request`, which hangs when Codex is configured
  with `approvals_reviewer=user` — waiting for ChatGPT app confirmation we never surface.)
- Agent stdout is streamed; permission/auth prompts raise a ⚠ mid-run (not only after timeout).
- When an agent still cannot proceed, the task becomes `awaiting_input`, the run `needs_attention`,
  and an **attention mark** (⚠) is raised for the human (`qma attention list` / TUI).
- Simple chat (status, attention, projects, thanks) is answered by the orchestrator without a full run.

## Routing defaults

Category preferences select Cursor for implementation, Codex for frontend and testing,
and Claude for architecture and review. Preferences take priority over the shared
fallback order (`codex`, `claude`, `cursor`), which remains unchanged to preserve
behavior for other categories when their preferred agent is unavailable.
Quota/token exhaustion (e.g. Claude "credit balance is too low") marks that vendor
exhausted for the daemon process and retries via `routing.fallback`; leadership
reseats along `routing.leadershipFallback` (`claude`, `cursor`, `codex`).

After changing `~/.config/qtmesh-agents/config.yaml`, the daemon may need a restart
or reload for live routing to use the new preferences on new runs.

## Packages

| Package | Responsibility |
|---|---|
| `cmd/qma` | Entrypoint |
| `internal/cli` | Cobra command tree |
| `internal/tui` | Bubble Tea client UI |
| `internal/daemon` | Process owner, queues, recovery |
| `internal/ipc` | Local transport (Unix sockets; Windows named pipes) |
| `internal/agents` | Adapter interfaces + registry |
| `internal/agents/*` | Claude, Codex, Cursor, mock adapters |
| `internal/sessions` | Interactive session lifecycle + buffers |
| `internal/pty` | PTY spawn, I/O, resize |
| `internal/orchestrator` | Top-level planning + run execution |
| `internal/scheduler` | DAG-aware concurrent task scheduling |
| `internal/router` | Agent selection policy |
| `internal/execution` | Headless task execution |
| `internal/worktree` | Git worktree isolation |
| `internal/review` | Static diff review (secrets/conflicts/size); wired from verify + integrate |
| `internal/projects` | Multi-repo project registry |
| `internal/storage` | SQLite persistence + migrations |
| `internal/config` | YAML config load/validate |
| `internal/events` | Structured execution events |
| `pkg/version` | Build version metadata |

## Daemon

Responsibilities:

- Single-instance lock via socket ownership
- Spawn / attach / detach / stop sessions
- Stream terminal output to attached clients
- Persist session metadata and recover after restart
- Auto-start when a client needs it

State recovery after daemon restart:

- Running PIDs are re-checked; live processes may be reattached when possible
- Otherwise sessions are marked `interrupted`
- Orchestration tasks resume from persisted status

## IPC

Protocol: length-prefixed JSON messages over a stream connection.

Message kinds include: `ping`, `status`, `spawn`, `attach`, `detach`, `input`, `resize`, `stop`, `list_sessions`, `list_projects`, `event`, `output`.

Unix: `$XDG_RUNTIME_DIR/qtmesh-agents/daemon.sock` (fallback `~/.qtmesh-agents/daemon.sock`).  
Windows: native local named pipes behind the same Listen/Dial API, with dial timeouts and no TCP marker files.

## Agent adapters

```go
type AgentAdapter interface {
    ID() string
    Vendor() string
    Detect(ctx context.Context) (bool, error)
    Capabilities(ctx context.Context) (AgentCapabilities, error)
    StartSession(ctx context.Context, opts SessionOptions) (AgentSession, error)
    Execute(ctx context.Context, task AgentTask) (Execution, error)
}
```

Optional: `InteractiveAgent`, `CancellableAgent`, `UsageReporter`, `SessionRestorer`.

## Sessions

Interactive sessions use a PTY. Clients attach/detach without terminating the process. Output is buffered (ring) and optionally logged to disk. Headless executions share lifecycle metadata but skip PTY attach semantics.

## Persistence

SQLite (WAL) stores projects, sessions, tasks, dependencies, events, reviews, worktrees. Large terminal logs live under the data directory; SQLite keeps paths and metadata.

## Security (MVP)

- Workspace path boundaries for project association
- Timeouts and concurrency caps
- Approval required for merge / destructive git ops
- Secrets redacted from logs
- Provider sandboxes are not bypassed
- Untrusted: repo instructions, agent output, generated plans

## Extension points (future)

Web dashboard, remote workers, MCP, GitHub/GitLab, Gemini/OpenCode/Ollama adapters, cost-aware routing, QtMeshEditor integration.

### Distribution and first run

`qma init` retains existing configuration (or writes defaults), detects agent CLIs,
and offers to register the current directory on a TTY. Non-TTY input and
`--non-interactive` skip prompts. Setup prints next-step commands.
`scripts/install.sh` installs release archives after SHA-256 verification or a
local binary into `~/.local/bin`. GoReleaser embeds version, commit, and build
date; `make release-snapshot` creates local archives without publishing.
