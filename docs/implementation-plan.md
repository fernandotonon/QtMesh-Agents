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

## Phase 3 — Validation and routing — Done

- [x] Cross-agent static review (`internal/review.ReviewDiff`) wired into verify/integrate; `reviews` table; conflict/secret blocking
- [x] Human merge approval (`merge_approval` attention) — Approve apply to main / Reject leave worktree; `qma review list|show`
- [x] Retry / agent fallback on Detect-fail & empty exit -1; `run_metrics` + `qma metrics`; attention fingerprint cap
- [x] Reliability: stop worker integration-approval spam; clearer empty exit -1; deterministic integrate (no LLM merge questions)

## Phase 4 — Distribution and polish — Done

- [x] First-run `qma init` wizard with agent detection, optional project registration, and non-interactive defaults
- [x] Release/local binary installer with checksum verification
- [x] GoReleaser version metadata, archives, checksums, release notes, and local snapshot target
- [x] Windows named-pipe transport; Unix sockets on other platforms
