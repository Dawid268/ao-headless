# AO headless on Portainer / Swarm / Traefik

Target host shape (example OVH VPS): Docker Swarm single-node, Traefik in
`portainer-edge-gateway` on overlay `traefik-public`, Portainer UI deploys
stacks, private GHCR pull credentials already configured.

**Do not** point Traefik or a public port at AO’s primary daemon (`127.0.0.1:3001`).
Only the **authenticated remote listener (3011)** is routed.

## Why not `http://57.129.41.248/`?

On this edge, ports **80/443 are Traefik**. Routers are selected by
`Host(...)`. A bare IP URL either hits the wrong stack or never matches AO’s
router. Pairing from the laptop must use a **dedicated DNS name**, e.g.:

```text
ao.YOURDOMAIN.pl  →  A  57.129.41.248
```

Then the Remote hosts URL is `https://ao.YOURDOMAIN.pl` (port 443 via Traefik).

## 1. Image via GitHub Actions → GHCR (recommended)

Workflow: [`.github/workflows/ao-headless-image.yml`](../../.github/workflows/ao-headless-image.yml).

On push to `main` / `feat/capacity-admission-remote` (paths under `backend/` or
`deploy/headless/`), or `workflow_dispatch`, CI builds
`deploy/headless/Dockerfile` and pushes:

```text
ghcr.io/dawid268/ao-headless:sha-<short>
ghcr.io/dawid268/ao-headless:main          # only from main
ghcr.io/dawid268/ao-headless:feat-capacity-admission-remote  # branch tag
```

**Portainer** needs a registry credential for `ghcr.io` (PAT with
`read:packages`, or a fine-grained token that can pull that package). Link the
package to the repo and grant the Portainer pull identity access if the package
is private.

Manual one-off (optional):

```bash
docker build -f deploy/headless/Dockerfile -t ghcr.io/dawid268/ao-headless:main .
docker push ghcr.io/dawid268/ao-headless:main
```

## 2. Create Swarm stack in Portainer

**Resource profile in [`stack.yml`](./stack.yml):** limits **3 CPU / 6 GiB RAM**;
warm-host caps default to 3 total / 1 orch / 2 workers. Disk: plan **≥20 GiB**
free on the node for volume `ao_data` (Swarm `local` does not hard-cap size —
monitor `df` on the Docker root or bind-mount a dedicated path, e.g. replace the
volume with `/var/lib/ao-headless:/ao-data` after `mkdir -p /var/lib/ao-headless`).

1. Portainer → **Stacks** → **Add stack** (Swarm / endpoint id=1).
2. Name: `ao-headless` (or similar; **do not** reuse names of existing stacks).
3. Paste [`stack.yml`](./stack.yml).
4. Environment variables:

| Key | Example |
|-----|---------|
| `AO_DOMAIN` | `ao.subscribeit.pl` |
| `AO_IMAGE` | `ghcr.io/dawid268/ao-headless:sha-….` (pinned) or `:main` |
| `TZ` | `Europe/Warsaw` |

**Deploy flow:** Actions publishes image → Portainer stack env `AO_IMAGE` →
Update stack with **re-pull image**. Do not build on the VPS.

5. Deploy with **re-pull image** when updating tags.
6. Wait 1–2 minutes for Traefik router registration (404 briefly is normal).

### Checklist right after DNS

```text
1. dig +short AO_DOMAIN          → 57.129.41.248
2. Build/push AO_IMAGE (or use existing GHCR tag Portainer can pull)
3. Deploy stack with AO_DOMAIN + AO_IMAGE
4. curl -sS https://AO_DOMAIN/api/v1/identity   → JSON hostId
5. Logs → Password
6. Local AO → Remote hosts → https://AO_DOMAIN + password
```

## 3. Get pairing password

Portainer → stack → service `ao` → **Logs**. Entrypoint prints:

```text
Remote host enabled
Host ID: ...
Address: http://...:3011
Password: ...
```

Ignore the in-container `Address:` for laptop pairing — use the public HTTPS
host instead:

```text
https://ao.YOURDOMAIN.pl
```

Password is the printed `Password:`.

Re-print without rotating:

```bash
docker exec $(docker ps -q -f name=ao-headless_ao) ao remote-host status
```

## 4. Connect local AO desktop

1. Local AO: **Settings → General → Developer mode** on.
2. **Settings → Remote hosts → Add host**.
3. Address: `https://ao.YOURDOMAIN.pl`
4. Password: from the container logs / `remote-host status`.
5. Host should go **online** (desktop probes `/api/v1/identity` then `/healthz`
   with the bearer).

## 5. Post-deploy checks

```bash
docker service ls | grep ao-headless
docker service ps ao-headless_ao --no-trunc | head -5
curl -sS https://ao.YOURDOMAIN.pl/api/v1/identity
# expect JSON with hostId + apiVersion:1
docker service inspect ao-headless_ao --format '{{json .Endpoint.Ports}}'
# expect empty / no host-published 80/443
```

Monitoring (`monitoring.subscribeit.pl`): new service group, CPU/RAM vs limits,
probe on `Host(ao.YOURDOMAIN.pl)` with path `/api/v1/identity`.

## Hard rules on this VPS

| Do | Don’t |
|----|--------|
| `deploy.labels` Traefik routers | Top-level `labels:` (ignored by Swarm) |
| Join `traefik-public` | Publish 80/443 or primary :3001 |
| Route to **3011** only | `network_mode: host`, `privileged`, docker.sock |
| Middleware defined on **this** service | `@file` middlewares from other stacks |
| Named volume `ao_data` | Writable state only in the container FS |
| Update via image tag + stack update | Edit running containers by hand |

## Compose vs Swarm

[`compose.yml`](./compose.yml) is for plain Compose / lab. Production on this
VPS uses [`stack.yml`](./stack.yml).
