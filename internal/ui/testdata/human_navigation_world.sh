#!/usr/bin/env bash
# Opt-in navigation acceptance with an explicitly supplied frozen binary.
set -eu
umask 077
AGENTNET_HUMAN_BINARY=${AGENTNET_HUMAN_BINARY:?tested local binary required}
AGENTNET_HUMAN_GO=${AGENTNET_HUMAN_GO:-go}
AGENTNET_HUMAN_WORLD=$(mktemp -d /tmp/agentnet-human-navigation-world.XXXXXX)
export AGENTNET_HUMAN_BINARY AGENTNET_HUMAN_WORLD AGENTNET_NOTIFY=off
printf '%s\n' "$AGENTNET_HUMAN_WORLD"
free_port() { python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1])'; }
backend_port=$(free_port)
browser_port=$(free_port)
printf '%s\n' "$browser_port" > "$AGENTNET_HUMAN_WORLD/mux-port"
openssl req -x509 -newkey rsa:2048 -nodes -keyout "$AGENTNET_HUMAN_WORLD/key.pem" -out "$AGENTNET_HUMAN_WORLD/cert.pem" -days 1 -subj /CN=127.0.0.1 -addext subjectAltName=IP:127.0.0.1 > "$AGENTNET_HUMAN_WORLD/cert.log" 2>&1
export SSL_CERT_FILE=$AGENTNET_HUMAN_WORLD/cert.pem
owned_pids=()
cleanup() {
  for pid in "${owned_pids[@]}"; do kill "$pid" 2>/dev/null || true; done
  for pid in "${owned_pids[@]}"; do wait "$pid" 2>/dev/null || true; done
}
trap cleanup EXIT
"$AGENTNET_HUMAN_BINARY" hub serve --data "$AGENTNET_HUMAN_WORLD/hub" --listen "127.0.0.1:$backend_port" --platform-tls --web --public-url "https://127.0.0.1:$browser_port" > "$AGENTNET_HUMAN_WORLD/hub.log" 2>&1 & owned_pids+=($!)
"$AGENTNET_HUMAN_GO" build -o "$AGENTNET_HUMAN_WORLD/tls-proxy" internal/ui/testdata/human_navigation_tls_proxy.go
"$AGENTNET_HUMAN_WORLD/tls-proxy" "127.0.0.1:$browser_port" "127.0.0.1:$backend_port" "$AGENTNET_HUMAN_WORLD/cert.pem" "$AGENTNET_HUMAN_WORLD/key.pem" > "$AGENTNET_HUMAN_WORLD/mux.log" 2>&1 & owned_pids+=($!)
for attempt in {1..100}; do [ -f "$AGENTNET_HUMAN_WORLD/hub/bootstrap-invite.txt" ] && break; sleep .1; done
sleep .5
"$AGENTNET_HUMAN_BINARY" --home "$AGENTNET_HUMAN_WORLD/alice" join --agent laptop "$("$AGENTNET_HUMAN_BINARY" hub bootstrap-invite --raw --data "$AGENTNET_HUMAN_WORLD/hub")" > "$AGENTNET_HUMAN_WORLD/join.log"
for account in bob dana service; do
  "$AGENTNET_HUMAN_BINARY" --home "$AGENTNET_HUMAN_WORLD/$account" join --agent desk "$("$AGENTNET_HUMAN_BINARY" --home "$AGENTNET_HUMAN_WORLD/alice" admin invite --raw "$account")" >> "$AGENTNET_HUMAN_WORLD/join.log"
done
for account in alice bob dana service; do
  "$AGENTNET_HUMAN_BINARY" --home "$AGENTNET_HUMAN_WORLD/$account" daemon --ui 127.0.0.1:0 > "$AGENTNET_HUMAN_WORLD/$account.log" 2>&1 & owned_pids+=($!)
done
printf '%s\n' "${owned_pids[@]}" > "$AGENTNET_HUMAN_WORLD/owned-pids"
sleep 1
"$AGENTNET_HUMAN_BINARY" --home "$AGENTNET_HUMAN_WORLD/alice" person create Alice > "$AGENTNET_HUMAN_WORLD/person.log"
"$AGENTNET_HUMAN_BINARY" --home "$AGENTNET_HUMAN_WORLD/bob" person create 'Bob Collaboration With An Exceptionally Long Display Name' >> "$AGENTNET_HUMAN_WORLD/person.log"
"$AGENTNET_HUMAN_BINARY" --home "$AGENTNET_HUMAN_WORLD/dana" person create 'Dana Design Operations' >> "$AGENTNET_HUMAN_WORLD/person.log"
"$AGENTNET_HUMAN_BINARY" --home "$AGENTNET_HUMAN_WORLD/service" person service >> "$AGENTNET_HUMAN_WORLD/person.log"
"$AGENTNET_HUMAN_BINARY" --home "$AGENTNET_HUMAN_WORLD/alice" person link > "$AGENTNET_HUMAN_WORLD/link"
"$AGENTNET_HUMAN_BINARY" --home "$AGENTNET_HUMAN_WORLD/alice" ui > "$AGENTNET_HUMAN_WORLD/alice-ui"
node internal/ui/testdata/human_navigation_journey.cjs
