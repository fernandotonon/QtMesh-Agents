# Security notes (MVP)

QtMesh Agents launches local coding agents that can execute shell commands and modify files in associated project directories.

## Guarantees

- The daemon does not bypass provider authentication, sandboxes, or confirmation prompts.
- Configuration files do not store API keys; providers use their own credential mechanisms.
- Structured logs should not include secrets; treat accidental leakage as a bug.
- Orchestrator merges (Phase 2+) require approval when `requireApprovalForMerge` is true.
- Execution timeouts and concurrency limits are enforced.

## Web workspace auth

When `daemon.httpEnabled` is true, the daemon listens on `daemon.httpListen` and requires a Bearer token (`daemon.httpAuthToken`) on every `/api` and `/ws` route. Failed auth attempts are rate-limited per client IP. Prefer Tailscale (or another private network) plus optional Tailscale Serve TLS; do not expose `0.0.0.0` to the public internet with only a shared token. Closing the browser never terminates agents — treat the token like a session secret (`qma web token --new` rotates it).

## Non-goals (MVP)

- Full OS sandboxing of agent subprocesses
- Multi-user remote access control / per-user ACLs on the web UI
- Cryptographic attestation of agent output

## Trust model

Repository instructions, agent stdout, and generated plans are **untrusted inputs**. Validate plans (DAG checks) and review diffs before merging.

Only run QtMesh Agents against repositories and agent CLIs you trust.
