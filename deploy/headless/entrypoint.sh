#!/bin/sh
# Start the loopback daemon, then ensure the authenticated remote listener is
# on (LAN bind inside the overlay — Traefik reaches it; never publish primary).
#
# All durable state must live under AO_DATA_DIR (the named volume). Harness
# CLI installs (Codex, Cursor, …) and their login homes default to $HOME —
# without redirecting HOME onto the volume, a Portainer image rebuild wipes
# them even when ao.db survives.
set -eu

AO_BIN="${AO_BIN:-ao}"
PRIMARY_PORT="${AO_PORT:-3001}"
AO_DATA_DIR="${AO_DATA_DIR:-/ao-data}"
AO_HOME="${AO_HOME:-${AO_DATA_DIR}/home}"

export AO_DATA_DIR
export HOME="$AO_HOME"
export XDG_CONFIG_HOME="${XDG_CONFIG_HOME:-$AO_HOME/.config}"
export XDG_DATA_HOME="${XDG_DATA_HOME:-$AO_HOME/.local/share}"
export XDG_CACHE_HOME="${XDG_CACHE_HOME:-$AO_HOME/.cache}"
export XDG_STATE_HOME="${XDG_STATE_HOME:-$AO_HOME/.local/state}"
export NPM_CONFIG_PREFIX="${NPM_CONFIG_PREFIX:-$AO_HOME/.npm-global}"
# Official installers drop shims in ~/.local/bin; npm -g uses NPM_CONFIG_PREFIX.
export PATH="$AO_HOME/.local/bin:$NPM_CONFIG_PREFIX/bin:/usr/local/bin:/usr/bin:/bin"

mkdir -p \
	"$AO_DATA_DIR" \
	"$AO_HOME" \
	"$AO_HOME/.local/bin" \
	"$NPM_CONFIG_PREFIX/bin" \
	"$XDG_CONFIG_HOME" \
	"$XDG_DATA_HOME" \
	"$XDG_CACHE_HOME" \
	"$XDG_STATE_HOME"

"$AO_BIN" daemon &
daemon_pid=$!

cleanup() {
	kill "$daemon_pid" 2>/dev/null || true
	wait "$daemon_pid" 2>/dev/null || true
}
trap cleanup INT TERM

i=0
while [ "$i" -lt 90 ]; do
	if curl -fsS --max-time 2 "http://127.0.0.1:${PRIMARY_PORT}/healthz" >/dev/null 2>&1; then
		break
	fi
	i=$((i + 1))
	sleep 1
done
if ! curl -fsS --max-time 2 "http://127.0.0.1:${PRIMARY_PORT}/healthz" >/dev/null 2>&1; then
	echo "ao daemon did not become healthy on 127.0.0.1:${PRIMARY_PORT}" >&2
	cleanup
	exit 1
fi

# Boot restore may already have re-armed an enabled listener from AO_DATA_DIR.
# LAN enable binds 0.0.0.0:3011 inside the overlay so Traefik can reach it.
status="$("$AO_BIN" remote-host status 2>/dev/null || true)"
case "$status" in
*Remote\ host\ disabled*|"")
	"$AO_BIN" remote-host enable
	;;
esac
"$AO_BIN" remote-host status || true

wait "$daemon_pid"
