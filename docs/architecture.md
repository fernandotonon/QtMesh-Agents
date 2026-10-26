# QtMesh Agents Architecture

**Product:** QtMesh Agents (`qma`)  
**Tagline:** One terminal. Multiple AI agents. Unified orchestration.  
**Module:** `github.com/fernandotonon/QtMesh-Agents`

## Overview

QtMesh Agents is a terminal-first orchestrator for multi-vendor AI coding agents. A persistent background daemon owns agent processes and PTYs. CLI and TUI clients connect over local IPC and never own agent lifetimes.

```text
┌─────────────┐   ┌─────────────┐
│  qma (CLI)  │   │  qma (TUI)  │
└──────┬──────┘   └──────┬──────┘
       │  Unix socket / named pipe
       └──────────┬──────┘
                  ▼
         ┌────────────────┐
         │  qma daemon    │
         │  sessions/PTY  │
         │  orchestrator  │
         │  SQLite store  │
         └───────┬────────┘
                 │
    ┌────────────┼────────────┐
    ▼            ▼            ▼
 Claude CLI   Codex CLI   Cursor Agent
```

## Design principles

1. **Daemon owns processes.** Closing the TUI never kills agents.
2. **Vendor-independent adapters.** Optional capability interfaces; no forced feature parity.
3. **Structured plans.** Orchestration uses validated DAGs, not free-form text.
4. **Isolation by default.** Autonomous tasks prefer Git worktrees.
5. **Human gates.** Merges and destructive ops require approval.
6. **Small interfaces.** Defined close to consumers; DI via constructors.

## Packages

| Package | Responsibility |
|---|---|
| `cmd/qma` | Entrypoint |
| `internal/cli` | Cobra command tree |
| `internal/tui` | Bubble Tea client UI |
| `internal/daemon` | Process owner, queues, recovery |
| `internal/ipc` | Local transport (Unix sockets; Windows abstraction) |
| `internal/agents` | Adapter interfaces + registry |
| `internal/agents/*` | Claude, Codex, Cursor, mock adapters |
| `internal/sessions` | Interactive session lifecycle + buffers |
| `internal/pty` | PTY spawn, I/O, resize |
| `internal/orchestrator` | Top-level planning + run execution |
| `internal/scheduler` | DAG-aware concurrent task scheduling |
| `internal/router` | Agent selection policy |
| `internal/execution` | Headless task execution |
| `internal/worktree` | Git worktree isolation |
| `internal/review` | Change review / approval (Phase 3) |
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
Windows: named-pipe abstraction behind the same `Transport` interface.

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
