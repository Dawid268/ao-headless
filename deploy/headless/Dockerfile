# Headless AO daemon for self-hosted VPS / Portainer Swarm.
# Build from repo root:
#   docker build -f deploy/headless/Dockerfile -t ao-headless .
#
# Primary listener stays on 127.0.0.1 inside the container. Pair via the
# authenticated remote-host listener behind Traefik (or Tailscale/tunnel).
# Never publish the unauthenticated primary port to the public internet.
FROM golang:1.27.1-bookworm AS build
WORKDIR /src
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
RUN CGO_ENABLED=1 go build -o /out/ao ./cmd/ao

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates git curl \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/ao /usr/local/bin/ao
COPY deploy/headless/entrypoint.sh /usr/local/bin/ao-headless-entrypoint.sh
RUN chmod +x /usr/local/bin/ao-headless-entrypoint.sh
ENV AO_DATA_DIR=/ao-data
# Harness CLIs + logins must not live on the ephemeral container rootfs.
# entrypoint re-exports HOME/XDG_* under /ao-data/home on every start.
ENV HOME=/ao-data/home
ENV AO_CHAT_HOST_MAX_TOTAL=6
ENV AO_CHAT_HOST_MAX_ORCH=2
ENV AO_CHAT_HOST_MAX_WORKER=4
VOLUME ["/ao-data"]
# Authenticated remote / Connect Mobile listener (enabled by entrypoint).
# Matches mobilebridge.DefaultPort. Primary daemon stays on 127.0.0.1:3001.
EXPOSE 3011
ENTRYPOINT ["/usr/local/bin/ao-headless-entrypoint.sh"]
