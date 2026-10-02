#!/usr/bin/env bash
# Opt-in installed-window acceptance using one coordinator-frozen binary.
# World/TLS setup follows human_navigation_world.sh and reuses its TLS proxy.
set -eu
umask 077
AGENTNET_PWA_BINARY=${AGENTNET_PWA_BINARY:?frozen local binary required}
AGENTNET_PWA_WORLD=${AGENTNET_PWA_WORLD:?fresh private world path required}
AGENTNET_SCREENSHOTS=${AGENTNET_SCREENSHOTS:?private evidence path required}
AGENTNET_PWA_ASSET_MANIFEST=${AGENTNET_PWA_ASSET_MANIFEST:?frozen asset manifest required}
AGENTNET_PWA_GO=${AGENTNET_PWA_GO:-go}
export AGENTNET_PWA_BINARY AGENTNET_PWA_WORLD AGENTNET_SCREENSHOTS AGENTNET_PWA_ASSET_MANIFEST AGENTNET_NOTIFY=off
mkdir "$AGENTNET_PWA_WORLD"
mkdir -p "$AGENTNET_SCREENSHOTS"
free_port() { python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1])'; }
backend_port=$(free_port)
browser_port=$(free_port)
printf '%s\n' "$browser_port" > "$AGENTNET_PWA_WORLD/mux-port"
openssl req -x509 -newkey rsa:2048 -nodes -keyout "$AGENTNET_PWA_WORLD/key.pem" -out "$AGENTNET_PWA_WORLD/cert.pem" -days 1 -subj /CN=127.0.0.1 -addext subjectAltName=IP:127.0.0.1 > "$AGENTNET_PWA_WORLD/cert.log" 2>&1
export SSL_CERT_FILE=$AGENTNET_PWA_WORLD/cert.pem
owned_pids=()
cleanup() {
  for pid in "${owned_pids[@]}"; do kill "$pid" 2>/dev/null || true; done
  for pid in "${owned_pids[@]}"; do wait "$pid" 2>/dev/null || true; done
}
trap cleanup EXIT
"$AGENTNET_PWA_BINARY" hub serve --data "$AGENTNET_PWA_WORLD/hub" --listen "127.0.0.1:$backend_port" --platform-tls --web --public-url "https://127.0.0.1:$browser_port" > "$AGENTNET_PWA_WORLD/hub.log" 2>&1 & owned_pids+=($!)
"$AGENTNET_PWA_GO" build -o "$AGENTNET_PWA_WORLD/tls-proxy" internal/ui/testdata/human_navigation_tls_proxy.go
"$AGENTNET_PWA_WORLD/tls-proxy" "127.0.0.1:$browser_port" "127.0.0.1:$backend_port" "$AGENTNET_PWA_WORLD/cert.pem" "$AGENTNET_PWA_WORLD/key.pem" > "$AGENTNET_PWA_WORLD/mux.log" 2>&1 & owned_pids+=($!)
for attempt in {1..100}; do [ -f "$AGENTNET_PWA_WORLD/hub/bootstrap-invite.txt" ] && break; sleep .1; done
sleep .5
"$AGENTNET_PWA_BINARY" --home "$AGENTNET_PWA_WORLD/alice" join --agent laptop "$("$AGENTNET_PWA_BINARY" hub bootstrap-invite --raw --data "$AGENTNET_PWA_WORLD/hub")" > "$AGENTNET_PWA_WORLD/join.log"
"$AGENTNET_PWA_BINARY" --home "$AGENTNET_PWA_WORLD/bob" join --agent desk "$("$AGENTNET_PWA_BINARY" --home "$AGENTNET_PWA_WORLD/alice" admin invite --raw bob)" >> "$AGENTNET_PWA_WORLD/join.log"
for account in alice bob; do
  "$AGENTNET_PWA_BINARY" --home "$AGENTNET_PWA_WORLD/$account" daemon --ui 127.0.0.1:0 > "$AGENTNET_PWA_WORLD/$account.log" 2>&1 & owned_pids+=($!)
done
printf '%s\n' "${owned_pids[@]}" > "$AGENTNET_PWA_WORLD/owned-pids"
sleep 1
"$AGENTNET_PWA_BINARY" --home "$AGENTNET_PWA_WORLD/alice" person create Alice > "$AGENTNET_PWA_WORLD/person.log"
"$AGENTNET_PWA_BINARY" --home "$AGENTNET_PWA_WORLD/bob" person create Bob >> "$AGENTNET_PWA_WORLD/person.log"
"$AGENTNET_PWA_BINARY" --home "$AGENTNET_PWA_WORLD/alice" person link > "$AGENTNET_PWA_WORLD/link"
node internal/ui/testdata/pwa_installed_journey.cjs
