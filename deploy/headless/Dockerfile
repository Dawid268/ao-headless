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
ARG TARGETARCH
# Pinned with backend/internal/service/systeminstall/archive.go.
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates git curl \
    && arch="${TARGETARCH:-amd64}" \
    && case "$arch" in \
         amd64) gh_sha=bb766f710eef8ede859c18578c72c327597cd4c8a85b06001b1f3843c6019386; glab_sha=9c9ac4c2b74ec8709dec4819a4041050f3680c6aaa18f0ed30434081f4fe9bef ;; \
         arm64) gh_sha=7862c86c72f43df3a2d93ddde6f473285b4e2af61b494849846827e513ef6484; glab_sha=1f2d0a885b28d08de341a597b2eb7204ce03d4e67b008efecab9951e4b014f0c ;; \
         *) echo "unsupported architecture: $arch" >&2; exit 1 ;; \
       esac \
    && tmp=$(mktemp -d) \
    && curl -fsSL "https://github.com/cli/cli/releases/download/v2.102.0/gh_2.102.0_linux_${arch}.tar.gz" -o "$tmp/gh.tgz" \
    && echo "$gh_sha  $tmp/gh.tgz" | sha256sum -c - \
    && tar -xzf "$tmp/gh.tgz" -C "$tmp" \
    && install -m 0755 "$(find "$tmp" -type f -name gh | head -n 1)" /usr/local/bin/gh \
    && curl -fsSL "https://gitlab.com/api/v4/projects/gitlab-org%2Fcli/packages/generic/glab/1.123.0/glab_1.123.0_linux_${arch}.tar.gz" -o "$tmp/glab.tgz" \
    && echo "$glab_sha  $tmp/glab.tgz" | sha256sum -c - \
    && tar -xzf "$tmp/glab.tgz" -C "$tmp" \
    && install -m 0755 "$(find "$tmp" -type f -name glab | head -n 1)" /usr/local/bin/glab \
    && rm -rf "$tmp" /var/lib/apt/lists/*
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
