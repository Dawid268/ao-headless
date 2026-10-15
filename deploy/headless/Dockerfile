# Headless AO daemon for self-hosted VPS / Portainer.
# Build from repo root:
#   docker build -f deploy/headless/Dockerfile -t ao-headless .
#
# Primary listener stays on 127.0.0.1 inside the container. Pair via the
# authenticated remote-host listener (or reach it over Tailscale). Never publish
# the unauthenticated primary port to the public internet.
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
ENV AO_DATA_DIR=/ao-data
ENV AO_CHAT_HOST_MAX_TOTAL=6
ENV AO_CHAT_HOST_MAX_ORCH=2
ENV AO_CHAT_HOST_MAX_WORKER=4
VOLUME ["/ao-data"]
# Authenticated remote listener (enable after boot). Primary stays loopback.
EXPOSE 8787
ENTRYPOINT ["ao", "daemon"]
