# Capacity: warm-host admission + headless remote

This branch adds two complementary capabilities:

1. **Warm-host admission** — the daemon caps concurrent live `ao chat-host`
   processes (Codex/ACP stacks) using `MemAvailable` hysteresis and
   orchestrator/worker sub-caps so many board sessions do not OOM one machine.
2. **Headless remote host** — Docker/Portainer packaging so heavy projects can
   run on a VPS while the laptop GUI supervises over the authenticated remote
   listener (Tailscale or Cloudflare). Primary loopback stays `127.0.0.1`.

## Warm budget

Env overlays (optional):

| Variable | Default (32 GiB profile) |
|----------|---------------------------|
| `AO_CHAT_HOST_MAX_TOTAL` | 6 |
| `AO_CHAT_HOST_MAX_ORCH` | 2 |
| `AO_CHAT_HOST_MAX_WORKER` | 4 |

Under memory pressure (available &lt; ~6 GiB) caps shrink further. Spawns that
cannot acquire a slot fail closed with capacity exhausted and are enqueued for
a later drain after hosts die / hibernate.

Inventory is **live `chat-hosts/*/host.json` PIDs**, not SQLite session rows.
Adopt of an existing host does not consume a new slot; a failed spawn that
leaves a live PID keeps the slot charged.

Exited (non-terminated) Chat sessions are dropped from the persistent-host
keep-set and reaped on the 30s sweep so stale Codex processes do not pin RAM.

## Headless Docker

See [`deploy/headless/`](../deploy/headless/). After `compose up`:

```bash
docker compose -f deploy/headless/compose.yml exec ao ao remote-host enable --tunnel-only
docker compose -f deploy/headless/compose.yml exec ao ao remote-host status
```

Pair from the desktop (Developer mode → Settings → Remote hosts). Use Tailscale
only as a path to the **authenticated** remote endpoint — never expose the
unauthenticated primary daemon.

## Project seed (git-less → VPS)

`project.SeedArchive` unpacks a gzipped tar onto the **host** with deny filters
(`node_modules`, `.env*`, …), writes a `.gitignore`, then the existing
`InitializeRepository` + `Add` path registers the project. There is no live
per-file tunnel: sessions and worktrees stay host-local after the one-shot seed.

HTTP/CLI upload surface for seed is follow-up wiring on top of `SeedArchive`.
