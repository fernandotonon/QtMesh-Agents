# Delegation reliability review

Reviewed the current working tree, including the existing uncommitted web workspace and bench orchestration work. This review focuses on the execution path rather than vendor CLI quality. No paid provider sessions were started.

## Findings addressed

| Severity | Finding | Result |
| --- | --- | --- |
| Critical | Failed patch application fell back to copying whole files over the project; earlier patches could already be applied when a later patch failed. | Apply the complete patch with one atomic Git apply operation. Conflicts stop integration and preserve the project and index. |
| Critical | Graphics and permission heuristics could fabricate a human reply and grant full sandbox access. | Removed automatic elevation and fabricated approvals. Explicit grants survive streamed output updates. |
| High | Verification ran from a fresh project copy without implementation artifacts. | Materialize dependency patches before starting a worker. Forward dependency reports in the prompt. |
| High | Diffs against HEAD included unrelated user edits and lost changes committed by agents. | Capture the initial synced tree in worktree Git metadata. Capture cumulative binary-capable patches against that snapshot, including committed agent changes. Preserve source deletions. |
| High | Scheduler workers mutated shared task objects while the scheduling loop read them. Queued goroutines could start after cancellation. | Single event loop owns DAG state; bounded workers use private copies and return results through a channel. Validate dependency structure before executing. |
| High | Retries created new worktrees and lost the previous attempt's working context. | Reuse the task workspace and include previous errors/output in retry prompts. Missing workspaces fail explicitly. |
| High | Approval was not bound to the artifacts prepared for review. | Require the prepared patch digest to match at apply time and rerun static review. Reject negative approval wording. |
| Medium | Integration could apply both an ancestor patch and its cumulative descendant. | Select the workspace artifact frontier, omitting ancestors already included in descendant workspaces. |
| Medium | Mentioning tests in an implementation objective misrouted the implementation worker. | Keep implementation routing separate from verification; match frontend terms as words. |
| Medium | Orchestrated task output was missing from the browser workflow. | Add a Work tab with run status, task dependencies, criteria, retries, output, errors, and workspace/artifact paths. Poll without overlapping requests; ignore stale responses after navigation. |

## Using the updated flow

Delegate through the existing orchestrator chat or goal workflow. The Work tab shows execution at the task level; expand only the tasks you want to inspect. Existing interactive sessions remain available in the terminal pane. Headless orchestrated workers expose persisted output rather than pretending to have an interactive PTY.

Implementation artifacts are prepared in isolated worktrees. Dependent workers receive those changes before execution. Integration presents the resulting patch for approval and applies it only if the artifacts still match. Conflicts require repair; they never trigger whole-file replacement. User files and the project index are not used as a scratch integration workspace.

Start new runs after updating the daemon. Older worktrees without a baseline cannot reliably distinguish user changes from agent changes and now fail diff capture explicitly. Existing captured artifacts can still be inspected; old pending integration approvals without a digest must be prepared again in a new run. Restart the daemon to load rebuilt Go code and embedded browser assets.

## Remaining trust limits

- `succeeded` still means the execution pipeline completed. An agent's zero exit code and a static diff scan do not prove acceptance criteria. The browser exposes the criteria and reported evidence for inspection. A next architectural step is a typed verification record with command, exit code, output artifact, and exact tested tree, collected independently of agent prose.
- The deterministic local leader writes a generic brief. It is not an adaptive planning/review agent. The existing bench and conductor loops still need realistic provider evaluations for ambiguous requests, failed verification, and scope changes.
- Worktrees isolate files, not credentials or arbitrary host commands. Provider sandbox configuration remains part of the trust boundary. Discovery/review tasks that run in the main checkout are not enforced read-only by the orchestrator.
- Sibling cumulative workspaces with shared modified ancestors or overlapping changes may conflict. The system fails safely instead of guessing how to combine them; a dedicated integration workspace with an explicit merge/retest cycle would improve recovery.
- Each task snapshots the project when its workspace starts. Concurrent user edits can produce incompatible baselines; integration stops on conflicting patches. A single immutable run-wide snapshot would give stronger reproducibility.
- Run recovery still relies on in-memory execution ownership plus persisted statuses. Durable execution leases and idempotent state transitions are needed for stronger restart and multi-client guarantees.
- Validation uses local Git repositories and the mock adapter. It does not establish reliability for live Claude, Cursor, or Codex sessions, quota changes, or their approval UX.

## Regression coverage

Tests cover invalid DAGs, parallel workers, dependency failures, human pauses, cancellation without starting queued work, user-state baselines, committed agent changes, source deletions, implementation-to-verification handoff, duplicate ancestor suppression, atomic conflict handling, changed artifacts after approval, negative approval wording, and preservation of explicit human controls. Run the full Go suite, the race detector for scheduler/orchestrator/worktree, the browser build, and TypeScript checking before release.
