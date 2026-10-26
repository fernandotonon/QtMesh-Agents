# Security notes (MVP)

QtMesh Agents launches local coding agents that can execute shell commands and modify files in associated project directories.

## Guarantees

- The daemon does not bypass provider authentication, sandboxes, or confirmation prompts.
- Configuration files do not store API keys; providers use their own credential mechanisms.
- Structured logs should not include secrets; treat accidental leakage as a bug.
- Orchestrator merges (Phase 2+) require approval when `requireApprovalForMerge` is true.
- Execution timeouts and concurrency limits are enforced.

## Non-goals (MVP)

- Full OS sandboxing of agent subprocesses
- Multi-user remote access control
- Cryptographic attestation of agent output

## Trust model

Repository instructions, agent stdout, and generated plans are **untrusted inputs**. Validate plans (DAG checks) and review diffs before merging.

Only run QtMesh Agents against repositories and agent CLIs you trust.
