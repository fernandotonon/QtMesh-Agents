# Implementation Plan

## Phase 1 — Working terminal environment (complete)

Manual multi-agent sessions via daemon + TUI/CLI. See prior checklist.

## Phase 2 — Autonomous orchestration (complete)

| # | Deliverable | Status |
|---|---|---|
| 1 | Configurable top-level orchestrator | Done |
| 2 | Natural-language task submission (`qma run`) | Done |
| 3 | Structured task planning (heuristic + optional provider) | Done |
| 4 | DAG validation | Done |
| 5 | Dependency-aware scheduling | Done |
| 6 | Parallel execution | Done |
| 7 | Agent delegation + routing | Done |
| 8 | Git worktree isolation | Done |
| 9 | Execution monitoring (`run status/list/diff/cancel`) | Done |
| 10 | Task status reporting + persisted runs | Done |

**Acceptance:** One objective → decomposed tasks → multi-provider (or mock) execution → inspectable diffs. Merges are never automatic when `requireApprovalForMerge` is true.

### Usage

```bash
qma project add /path/to/repo && qma project use <name>
qma run --dry-run "add caching and improve tests"
qma run -f "add caching and improve tests"          # real agents via router
qma run --agent mock -f "safe local smoke"          # CI / dry execution
qma run status <run-id> --follow
qma run diff <run-id>
qma run list
```

## Phase 3 — Validation and routing

- Cross-agent review, verification, merge conflict handling
- Human approval workflow UI
- Retry / fallback polish, historical metrics

## Phase 4 — Distribution and polish

- First-run wizard, GoReleaser, install scripts, Windows transport polish
