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
| `AO_CHAT_HOST_PRESSURE_TOTAL` | 3 |
| `AO_CHAT_HOST_PRESSURE_ORCH` | 1 |
| `AO_CHAT_HOST_PRESSURE_WORKER` | 2 |
| `AO_CHAT_HOST_ENTER_AVAILABLE_MIB` | ~6144 (6 GiB) |
| `AO_CHAT_HOST_EXIT_AVAILABLE_MIB` | ~9216 (9 GiB) |

Under memory pressure (available &lt; ~6 GiB) caps shrink further. Spawns that
cannot acquire a slot fail closed with capacity exhausted and are enqueued for
a later drain after hosts die / hibernate. Cold `ResumeAgent` soft-queues
(`queued: true`) *before* clearing `HibernatedAt`, and a successful soft-admit
**reserves** the warm slot so concurrent spawns cannot steal it. Only the caller
that newly reserved rolls the slot back via `ReleaseIfNoPID` on resume failure —
an already-held host (Drain peer / warm adopt) must not steal that reservation.
`reservationGrace` is 90s so the 30s dead-PID sweep cannot clear an in-flight
cold wake before the 60s drain wake timeout.

When the fair wake queue is blocked at capacity, pressure reclaim first
hibernates eligible Chat workers, then shuts down one review/side/background
host per sweep (they occupy `MaxWorker`/`MaxTotal` but cannot `HibernateChat`).
Orchestrators are never auto-stopped; a queued orchestrator waits for an orch
slot (`MaxOrch`) or a freed total slot after aux reclaim.

Inventory is **live `chat-hosts/*/host.json` PIDs**, not SQLite session rows.
Adopt of an existing host does not consume a new slot; a failed spawn that
leaves a live PID keeps the slot charged.

Exited (non-terminated) Chat sessions are dropped from the persistent-host
keep-set and reaped on the 30s sweep so stale Codex processes do not pin RAM.

## VPS install (recommended)

Canonical path: [`self-hosted-remote.md`](./self-hosted-remote.md). On a fresh
Ubuntu 24.04 x64 VM (SSH as a sudo user), one command installs AO + user service
+ Cloudflare quick tunnel and prints pairing details:

```bash
# Nightly (pair with a nightly desktop until stable has account linking):
bash -c 'set -o pipefail; sudo apt-get update && sudo apt-get install -y curl && curl -fsSL https://raw.githubusercontent.com/Untrivial-ai/agent-orchestrator/main/scripts/bootstrap-self-hosted.sh | bash -s -- --nightly'
```

Then on the VM:

```bash
~/.local/bin/ao remote-host status
```

On the laptop: Developer mode → Settings → Remote hosts → add `Address:` +
`Password:`. Do **not** expose the unauthenticated primary listener
(`127.0.0.1`). For a trusted LAN/VPN only, append `--lan` to the bootstrap args.

## Headless on Portainer / Swarm / Traefik (this VPS shape)

When the host already runs Traefik on 80/443 + overlay `traefik-public` (see
[`deploy/headless/PORTAINER.md`](../deploy/headless/PORTAINER.md)):

1. DNS: `AO_DOMAIN` A/AAAA → VPS (e.g. `ao.yourdomain.pl`). **Not**
   `http://VPS_IP/` — Traefik routes by `Host(...)`.
2. Build/push `ao-headless` to GHCR; Portainer pulls it.
3. Deploy Swarm stack from [`deploy/headless/stack.yml`](../deploy/headless/stack.yml)
   with env `AO_DOMAIN` + `AO_IMAGE`. Labels live under `deploy.labels`; service
   joins `traefik-public`; Traefik forwards HTTPS → container **3011** only.
4. Copy `Password:` from service logs; on the laptop pair
   `https://$AO_DOMAIN` under Developer mode → Remote hosts.

## Headless Compose (lab / no Traefik)

```bash
docker compose -f deploy/headless/compose.yml up -d --build
docker compose -f deploy/headless/compose.yml exec ao ao remote-host enable --tunnel-only
docker compose -f deploy/headless/compose.yml exec ao ao remote-host status
```

Prefer Tailscale/Cloudflare to the **authenticated** port `3011` — never
publish the primary daemon port.

## Project seed (git-less → VPS)

`project.SeedArchive` unpacks a gzipped tar onto the **host** with deny filters
(`node_modules`, `.env*`, …), writes a `.gitignore`, then the existing
`InitializeRepository` + `Add` path registers the project. There is no live
per-file tunnel: sessions and worktrees stay host-local after the one-shot seed.

### Seed upload

```bash
# On the remote host (or via paired CLI pointing at that daemon):
ao project seed --dest /var/lib/ao/projects/my-app --archive ./my-app.tar.gz
```

`POST /api/v1/projects/seed?destDir=...` accepts a gzipped tar body, unpacks with
deny filters, initializes git, and registers the project.

### Preview publish

```bash
ao preview publish --port 4200
```

Opens the port in the desktop browser panel. When a LAN/Tailscale endpoint is
advertised by `ao remote-host status`, prints `http://<host>:<port>`. Cloudflare
quick tunnels front only the authenticated AO listener — they are **not** used
as the published app URL.
