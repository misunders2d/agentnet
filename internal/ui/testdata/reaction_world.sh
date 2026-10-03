#!/usr/bin/env bash
# Opt-in actual assistant-reaction journey over a real loopback Hub, real
# native daemons and Alice's real linked browser Engine, with an explicitly
# supplied local binary. Disposable world; no model, provider or external
# network: Charlie's responder is the claude harness resolved on a PATH that
# holds only a deterministic stub (and /usr/bin:/bin), which prints one
# answer and its structured final reaction line. Production capability
# lists stay unchanged: the journey signs fixture-only agr1 caps records
# (see reaction_journey.cjs). Reuses human_mixed_world.sh's Hub/TLS setup.
set -eu
umask 077
AGENTNET_REACTION_BINARY=${AGENTNET_REACTION_BINARY:?tested local binary required}
AGENTNET_REACTION_GO=${AGENTNET_REACTION_GO:-go}
AGENTNET_REACTION_WORLD=$(mktemp -d "${TMPDIR:-/tmp}/agentnet-reaction-world.XXXXXX")
export AGENTNET_REACTION_BINARY AGENTNET_REACTION_WORLD AGENTNET_NOTIFY=off
printf '%s\n' "$AGENTNET_REACTION_WORLD"
free_port() { python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1])'; }
backend_port=$(free_port)
browser_port=$(free_port)
printf '%s\n' "$browser_port" > "$AGENTNET_REACTION_WORLD/mux-port"
openssl req -x509 -newkey rsa:2048 -nodes -keyout "$AGENTNET_REACTION_WORLD/key.pem" -out "$AGENTNET_REACTION_WORLD/cert.pem" -days 1 -subj /CN=127.0.0.1 -addext subjectAltName=IP:127.0.0.1 > "$AGENTNET_REACTION_WORLD/cert.log" 2>&1
export SSL_CERT_FILE=$AGENTNET_REACTION_WORLD/cert.pem NODE_EXTRA_CA_CERTS=$AGENTNET_REACTION_WORLD/cert.pem
# The stub harness: reads the prompt, answers, and offers one reaction.
stub=$AGENTNET_REACTION_WORLD/stubbin
mkdir -p "$stub"
cat > "$stub/claude" <<'STUB'
#!/bin/sh
# Deterministic journey stand-in for the claude harness: no model, no network.
cat > /dev/null
printf 'Checked it.\nreaction: 🎉\n'
STUB
chmod 700 "$stub/claude"
charlie_path=$stub:/usr/bin:/bin
[ "$(PATH=$charlie_path command -v claude)" = "$stub/claude" ] || { echo "the stub is not the claude on Charlie's PATH" >&2; exit 1; }
owned_pids=()
cleanup() {
  for pid in "${owned_pids[@]}"; do kill "$pid" 2>/dev/null || true; done
  for pid in "${owned_pids[@]}"; do wait "$pid" 2>/dev/null || true; done
}
trap cleanup EXIT
"$AGENTNET_REACTION_BINARY" hub serve --data "$AGENTNET_REACTION_WORLD/hub" --listen "127.0.0.1:$backend_port" --platform-tls --web --public-url "https://127.0.0.1:$browser_port" > "$AGENTNET_REACTION_WORLD/hub.log" 2>&1 & owned_pids+=($!)
"$AGENTNET_REACTION_GO" build -o "$AGENTNET_REACTION_WORLD/tls-proxy" internal/ui/testdata/human_navigation_tls_proxy.go
"$AGENTNET_REACTION_WORLD/tls-proxy" "127.0.0.1:$browser_port" "127.0.0.1:$backend_port" "$AGENTNET_REACTION_WORLD/cert.pem" "$AGENTNET_REACTION_WORLD/key.pem" > "$AGENTNET_REACTION_WORLD/mux.log" 2>&1 & owned_pids+=($!)
for attempt in {1..100}; do [ -f "$AGENTNET_REACTION_WORLD/hub/bootstrap-invite.txt" ] && break; sleep .1; done
sleep .5
"$AGENTNET_REACTION_BINARY" --home "$AGENTNET_REACTION_WORLD/alice" join --agent laptop "$("$AGENTNET_REACTION_BINARY" hub bootstrap-invite --raw --data "$AGENTNET_REACTION_WORLD/hub")" > "$AGENTNET_REACTION_WORLD/join.log"
for account in bob charlie; do
  agent=desk; [ "$account" = charlie ] && agent=host
  "$AGENTNET_REACTION_BINARY" --home "$AGENTNET_REACTION_WORLD/$account" join --agent "$agent" "$("$AGENTNET_REACTION_BINARY" --home "$AGENTNET_REACTION_WORLD/alice" admin invite --raw "$account")" >> "$AGENTNET_REACTION_WORLD/join.log"
done
mkdir -p "$AGENTNET_REACTION_WORLD/charlie-work"
PATH=$charlie_path "$AGENTNET_REACTION_BINARY" --home "$AGENTNET_REACTION_WORLD/charlie" responder set --harness claude --dir "$AGENTNET_REACTION_WORLD/charlie-work" > "$AGENTNET_REACTION_WORLD/responder.log"
for account in alice bob charlie; do
  if [ "$account" = charlie ]; then
    PATH=$charlie_path "$AGENTNET_REACTION_BINARY" --home "$AGENTNET_REACTION_WORLD/$account" daemon --ui 127.0.0.1:0 > "$AGENTNET_REACTION_WORLD/$account.log" 2>&1 & owned_pids+=($!)
  else
    "$AGENTNET_REACTION_BINARY" --home "$AGENTNET_REACTION_WORLD/$account" daemon --ui 127.0.0.1:0 > "$AGENTNET_REACTION_WORLD/$account.log" 2>&1 & owned_pids+=($!)
  fi
done
printf '%s\n' "${owned_pids[@]}" > "$AGENTNET_REACTION_WORLD/owned-pids"
sleep 1
for account in alice bob charlie; do
  "$AGENTNET_REACTION_BINARY" --home "$AGENTNET_REACTION_WORLD/$account" person create "${account^}" >> "$AGENTNET_REACTION_WORLD/person.log"
  "$AGENTNET_REACTION_BINARY" --home "$AGENTNET_REACTION_WORLD/$account" ui > "$AGENTNET_REACTION_WORLD/$account-ui"
done
"$AGENTNET_REACTION_BINARY" --home "$AGENTNET_REACTION_WORLD/alice" person link > "$AGENTNET_REACTION_WORLD/link-alice"
node internal/ui/testdata/reaction_journey.cjs
