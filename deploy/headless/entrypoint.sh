#!/bin/sh
# Start the loopback daemon, then ensure the authenticated remote listener is
# on (LAN bind inside the overlay — Traefik reaches it; never publish primary).
set -eu

AO_BIN="${AO_BIN:-ao}"
PRIMARY_PORT="${AO_PORT:-3001}"

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
