# Web workspace

The daemon serves a browser workspace (REST + WebSocket) alongside the Unix-socket IPC used by the CLI/TUI. Closing the browser never kills agent sessions — same ownership model as the TUI.

## Enable

In `~/.config/qtmesh-agents/config.yaml` (see `examples/config.yaml`):

```yaml
daemon:
  httpEnabled: true
  httpListen: "0.0.0.0:7432"   # or 127.0.0.1:7432 / Tailscale 100.x:7432
  httpAuthToken: ""            # empty => auto-generate on first start
```

Restart after install or config changes:

```bash
make install                   # builds SPA + qma; reminds you to restart
qma daemon stop && qma daemon start
qma web                        # prints URL?token=… and opens the browser
qma web token                  # show token
qma web token --new            # regenerate (then restart daemon)
```

Every `/api/*` and `/ws/*` route requires `Authorization: Bearer <token>` (or `?token=` for WebSockets / first open).

## Tailscale

1. Bind to your Tailscale IP or `0.0.0.0` so peers can reach the port.
2. Optional TLS front with Tailscale Serve:

```bash
tailscale serve --bg 7432
# or HTTPS to a specific path — see Tailscale Serve docs
```

3. Open `https://<magicdns>/` (or the printed `http://100.x:7432/?token=…`) from another machine on your tailnet.

Do not expose `0.0.0.0` to the public internet without TLS and a strong token. Auth failures are rate-limited per client IP.

## Layout

- **Left:** project benches / goal status
- **Center:** orchestrator chat, bench thread, Needs you inbox (selectable markdown)
- **Right:** live PTY panes (`xterm.js`) — spawn Claude/Codex/Cursor or attach existing sessions; spawn-from-profile

## Dev SPA

```bash
cd web && npm install && npm run dev   # proxies /api and /ws to :7432
# production embed:
make web                               # writes internal/web/dist (go:embed)
```
