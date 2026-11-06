# QtMesh Agents

**One terminal. Multiple AI agents. Unified orchestration.**

QtMesh Agents (`qma`) is an open-source, terminal-first control center for launching, managing, and orchestrating AI coding agents from multiple vendors (Claude Code, OpenAI Codex, Cursor Agent, and more).

It runs as a lightweight Go daemon that owns agent processes and PTYs. The CLI and Bubble Tea TUI are clients — closing the UI never kills your agents.

## Features (Phase 1)

- Unified CLI: `qma` (alias `qtmesh-agents`)
- Persistent background daemon with Unix domain socket IPC
- **Web workspace** (browser): benches/goals/chat/Needs you + live agent terminals — `qma web`
- Interactive sessions with PTY attach/detach
- Adapters for Claude Code, Codex, Cursor Agent, plus a mock agent for CI
- SQLite persistence for projects, sessions, and events
- Bubble Tea TUI for projects, agents, sessions, and orchestrator notes
- Project registry (`qma project add|list|use`)
- Status, history, logs, and `agents doctor`

Phase 2 adds a conversational multi-project orchestrator:

```bash
qma project add ~/projects/QtMeshEditor
qma project add ~/projects/Data-Core-Clash
qma chat   # talk across all projects — no need to select one
# or:
qma chat "In QtMeshEditor, add hair materials; in Data Core Clash, fix matchmaking"
qma run status <run-id> --follow
qma run diff <run-id>
```

See [docs/implementation-plan.md](docs/implementation-plan.md).

## Install

Install the latest release (macOS/Linux):

```bash
curl -fsSL https://raw.githubusercontent.com/fernandotonon/QtMesh-Agents/main/scripts/install.sh | bash
# Pin a release or install a local build:
bash scripts/install.sh --version v0.1.0
bash scripts/install.sh --binary ./bin/qma
```

The default destination is `~/.local/bin`; add it to PATH if prompted. Override
with `--dir` or `QMA_INSTALL_DIR`. Downloads are checked against release SHA-256
checksums. Windows users can extract `qma.exe` from the release zip onto PATH.

Build from source:

```bash
git clone https://github.com/fernandotonon/QtMesh-Agents.git
cd QtMesh-Agents
make install   # installs qma to $(go env GOPATH)/bin
# or
make build     # writes ./bin/qma
```

Requires the Go toolchain specified in `go.mod`.

For local release archives, run `make release-snapshot` (GoReleaser v2), or
`goreleaser release --snapshot --clean`; neither publishes nor requires pushing tags.

## Quick start

```bash
# Set up config, detect agents, and optionally register this directory
qma init
# Scripts / CI: qma init --non-interactive (also automatic when stdin is not a TTY)

# Start / inspect the daemon (also auto-starts on demand)
qma daemon start
qma daemon status

# Register a project
qma project add ~/projects/MyApp
qma project use MyApp

# Spawn real agents (must be on PATH)
qma spawn claude
qma spawn codex
qma sessions
qma attach <session-id>   # raw terminal; detach with the client exit

# Or open the TUI
qma

# Web workspace (daemon HTTP — Bearer token; Tailscale-friendly bind)
qma daemon start
qma web                 # opens http://…/?token=…
qma web token           # show / regenerate auth token
```

See [docs/web-workspace.md](docs/web-workspace.md) for bind, Tailscale Serve, and restart notes.

Inside the TUI:

| Key / command | Action |
|---|---|
| `spawn claude` | Start Claude and attach |
| `attach <id>` | Attach to a running session |
| `stop <id>` | Terminate a session |
| `detach` | Detach without killing |
| `Tab` | Toggle orchestrator / agent view |
| `q` / `Ctrl+C` | Quit TUI (agents keep running) |

## Configuration

Default path: `~/.config/qtmesh-agents/config.yaml`

See [examples/config.yaml](examples/config.yaml). Data and logs live under `~/.local/share/qtmesh-agents/`.

## Architecture

```text
CLI / TUI  ──Unix socket──►  daemon (sessions, PTY, SQLite, HTTP/WS)
Browser    ──Bearer HTTP──►        │
                                   │
                      Claude / Codex / Cursor / mock
```

Details: [docs/architecture.md](docs/architecture.md) · [docs/web-workspace.md](docs/web-workspace.md)

## Development

```bash
make web          # Vite SPA → internal/web/dist (embedded)
make build-go     # binary only (uses existing dist)
make test
make vet
go test ./...
```

After `make install`, restart the daemon so it loads the new binary (`qma daemon stop && qma daemon start`).

CI uses the mock adapter so no paid provider credentials are required.

Optional live checks (when CLIs are installed):

```bash
qma agents doctor
```

## Security (MVP)

Coding agents can run shell commands and modify files. QtMesh Agents:

- Does not bypass provider sandboxes or auth prompts
- Requires human approval for merges (orchestrator Phase 2+)
- Applies timeouts and concurrency limits
- Treats agent output and generated plans as untrusted

You are responsible for reviewing agent changes before merging.

## License

MIT — see [LICENSE](LICENSE).
