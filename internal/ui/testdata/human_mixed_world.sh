#!/usr/bin/env bash
# Opt-in actual mixed native/browser human-guest (H5) acceptance with an
# explicitly supplied local binary. Disposable loopback world; no model,
# harness, real home or external network. Reuses human_navigation_world.sh's
# Hub/TLS-proxy setup. Production capability lists stay unchanged: the
# journey signs fixture-only caps records (see human_mixed_journey.cjs).
set -eu
umask 077
AGENTNET_HUMAN_BINARY=${AGENTNET_HUMAN_BINARY:?tested local binary required}
AGENTNET_HUMAN_GO=${AGENTNET_HUMAN_GO:-go}
AGENTNET_HUMAN_WORLD=$(mktemp -d /tmp/agentnet-human-mixed-world.XXXXXX)
export AGENTNET_HUMAN_BINARY AGENTNET_HUMAN_WORLD AGENTNET_NOTIFY=off
printf '%s\n' "$AGENTNET_HUMAN_WORLD"
free_port() { python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1])'; }
backend_port=$(free_port)
browser_port=$(free_port)
printf '%s\n' "$browser_port" > "$AGENTNET_HUMAN_WORLD/mux-port"
openssl req -x509 -newkey rsa:2048 -nodes -keyout "$AGENTNET_HUMAN_WORLD/key.pem" -out "$AGENTNET_HUMAN_WORLD/cert.pem" -days 1 -subj /CN=127.0.0.1 -addext subjectAltName=IP:127.0.0.1 > "$AGENTNET_HUMAN_WORLD/cert.log" 2>&1
export SSL_CERT_FILE=$AGENTNET_HUMAN_WORLD/cert.pem NODE_EXTRA_CA_CERTS=$AGENTNET_HUMAN_WORLD/cert.pem
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
for account in bob dana carol; do
  "$AGENTNET_HUMAN_BINARY" --home "$AGENTNET_HUMAN_WORLD/$account" join --agent desk "$("$AGENTNET_HUMAN_BINARY" --home "$AGENTNET_HUMAN_WORLD/alice" admin invite --raw "$account")" >> "$AGENTNET_HUMAN_WORLD/join.log"
done
# Assistant focus: Bob's own assistant is a deterministic stand-in for the
# claude harness (no model, no network) that records each prompt it gets.
bob_path=$PATH
if [ "${AGENTNET_HUMAN_FOCUS:-}" = assistant ]; then
  stub=$AGENTNET_HUMAN_WORLD/stubbin
  mkdir -p "$stub" "$AGENTNET_HUMAN_WORLD/bob-work"
  printf '#!/bin/sh\n{ echo "=== run"; cat; } >> "%s/bob-prompts.log"\nprintf '"'"'Synthetic assistant answer for this conversation.\\nreaction: 👀\\n'"'"'\n' "$AGENTNET_HUMAN_WORLD" > "$stub/claude"
  chmod 700 "$stub/claude"
  bob_path=$stub:/usr/bin:/bin
  [ "$(PATH=$bob_path command -v claude)" = "$stub/claude" ] || { echo "the stub is not the claude on Bob's PATH" >&2; exit 1; }
  PATH=$bob_path "$AGENTNET_HUMAN_BINARY" --home "$AGENTNET_HUMAN_WORLD/bob" responder set --harness claude --dir "$AGENTNET_HUMAN_WORLD/bob-work" > "$AGENTNET_HUMAN_WORLD/responder.log"
fi
for account in alice bob dana carol; do
  path=$PATH; [ "$account" = bob ] && path=$bob_path
  PATH=$path "$AGENTNET_HUMAN_BINARY" --home "$AGENTNET_HUMAN_WORLD/$account" daemon --ui 127.0.0.1:0 > "$AGENTNET_HUMAN_WORLD/$account.log" 2>&1 & owned_pids+=($!)
  printf '%s\n' "$!" > "$AGENTNET_HUMAN_WORLD/$account.pid"
done
printf '%s\n' "${owned_pids[@]}" > "$AGENTNET_HUMAN_WORLD/owned-pids"
sleep 1
for account in alice bob dana carol; do
  "$AGENTNET_HUMAN_BINARY" --home "$AGENTNET_HUMAN_WORLD/$account" person create "${account^}" >> "$AGENTNET_HUMAN_WORLD/person.log"
  "$AGENTNET_HUMAN_BINARY" --home "$AGENTNET_HUMAN_WORLD/$account" ui > "$AGENTNET_HUMAN_WORLD/$account-ui"
done
for account in alice carol; do
  "$AGENTNET_HUMAN_BINARY" --home "$AGENTNET_HUMAN_WORLD/$account" person link > "$AGENTNET_HUMAN_WORLD/link-$account"
done
node internal/ui/testdata/human_mixed_journey.cjs
