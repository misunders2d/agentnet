#!/usr/bin/env bash
# Opt-in isolated person/profile journey, reusing the existing loopback TLS mux.
set -eu
umask 077
AGENTNET_PERSON_WORLD=$(mktemp -d /tmp/agentnet-person-live-world.XXXXXX)
export AGENTNET_PERSON_WORLD AGENTNET_NOTIFY=off
AGENTNET_PERSON_BINARY=${AGENTNET_PERSON_BINARY:?built local binary required}
AGENTNET_PERSON_MUX=${AGENTNET_PERSON_MUX:-/tmp/agentnet-typing-full-owned/mux.py}
export AGENTNET_PERSON_BINARY
free_port() { python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1])'; }
backend_port=$(free_port)
browser_port=$(free_port)
echo "$browser_port" > "$AGENTNET_PERSON_WORLD/mux-port"
openssl req -x509 -newkey rsa:2048 -nodes -keyout "$AGENTNET_PERSON_WORLD/key.pem" -out "$AGENTNET_PERSON_WORLD/cert.pem" -days 1 -subj /CN=127.0.0.1 -addext subjectAltName=IP:127.0.0.1 > "$AGENTNET_PERSON_WORLD/cert.log" 2>&1
export SSL_CERT_FILE=$AGENTNET_PERSON_WORLD/cert.pem
owned_pids=()
cleanup() { for pid in "${owned_pids[@]}"; do kill "$pid" 2>/dev/null || true; done; for pid in "${owned_pids[@]}"; do wait "$pid" 2>/dev/null || true; done; }
trap cleanup EXIT
"$AGENTNET_PERSON_BINARY" hub serve --data "$AGENTNET_PERSON_WORLD/hub" --listen "127.0.0.1:$backend_port" --platform-tls --web --public-url "https://127.0.0.1:$browser_port" > "$AGENTNET_PERSON_WORLD/hub.log" 2>&1 & owned_pids+=($!)
python3 "$AGENTNET_PERSON_MUX" "127.0.0.1:$browser_port" "127.0.0.1:$backend_port" "$AGENTNET_PERSON_WORLD/cert.pem" "$AGENTNET_PERSON_WORLD/key.pem" > "$AGENTNET_PERSON_WORLD/mux.log" 2>&1 & owned_pids+=($!)
for attempt in {1..100}; do [ -f "$AGENTNET_PERSON_WORLD/hub/bootstrap-invite.txt" ] && break; sleep .1; done
sleep .5
"$AGENTNET_PERSON_BINARY" --home "$AGENTNET_PERSON_WORLD/alice" join --agent laptop "$("$AGENTNET_PERSON_BINARY" hub bootstrap-invite --raw --data "$AGENTNET_PERSON_WORLD/hub")" > "$AGENTNET_PERSON_WORLD/join.log"
for person in bob carol; do
  "$AGENTNET_PERSON_BINARY" --home "$AGENTNET_PERSON_WORLD/$person" join --agent desk "$("$AGENTNET_PERSON_BINARY" --home "$AGENTNET_PERSON_WORLD/alice" admin invite --raw "$person")" >> "$AGENTNET_PERSON_WORLD/join.log"
done
for person in alice bob carol; do
  "$AGENTNET_PERSON_BINARY" --home "$AGENTNET_PERSON_WORLD/$person" daemon --ui 127.0.0.1:0 > "$AGENTNET_PERSON_WORLD/$person.log" 2>&1 & owned_pids+=($!)
done
sleep 1
"$AGENTNET_PERSON_BINARY" --home "$AGENTNET_PERSON_WORLD/alice" person create Alice > "$AGENTNET_PERSON_WORLD/person.log"
for person in bob carol; do "$AGENTNET_PERSON_BINARY" --home "$AGENTNET_PERSON_WORLD/$person" person create Bob >> "$AGENTNET_PERSON_WORLD/person.log"; done
"$AGENTNET_PERSON_BINARY" --home "$AGENTNET_PERSON_WORLD/alice" person link > "$AGENTNET_PERSON_WORLD/link"
for person in alice bob; do "$AGENTNET_PERSON_BINARY" --home "$AGENTNET_PERSON_WORLD/$person" ui > "$AGENTNET_PERSON_WORLD/$person-ui"; done
node "${AGENTNET_PERSON_JOURNEY:-internal/ui/testdata/person_live_journey.cjs}"
