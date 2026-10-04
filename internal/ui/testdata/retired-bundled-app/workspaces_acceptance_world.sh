#!/usr/bin/env bash
# Opt-in two independent disposable relays, one frozen binary. No models.
set -eu
umask 077
: "${AGENTNET_WORKSPACE_BINARY:?}" "${AGENTNET_WORKSPACE_WORLD:?}" "${AGENTNET_SCREENSHOTS:?}" "${AGENTNET_PLAYWRIGHT:?}"
export AGENTNET_NOTIFY=off
mkdir "$AGENTNET_WORKSPACE_WORLD"
mkdir "$AGENTNET_WORKSPACE_WORLD/cli-home"
free_port() { python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1])'; }
for realm in a b; do
  free_port > "$AGENTNET_WORKSPACE_WORLD/backend-$realm"
  free_port > "$AGENTNET_WORKSPACE_WORLD/mux-$realm"
done
openssl req -x509 -newkey rsa:2048 -nodes -keyout "$AGENTNET_WORKSPACE_WORLD/key.pem" -out "$AGENTNET_WORKSPACE_WORLD/cert.pem" -days 1 -subj /CN=127.0.0.1 -addext subjectAltName=IP:127.0.0.1 > "$AGENTNET_WORKSPACE_WORLD/cert.log" 2>&1
export SSL_CERT_FILE="$AGENTNET_WORKSPACE_WORLD/cert.pem"
owned=()
cleanup(){ for pid in "${owned[@]}"; do kill "$pid" 2>/dev/null || true; done; for pid in "${owned[@]}"; do wait "$pid" 2>/dev/null || true; done; }
trap cleanup EXIT
"${AGENTNET_WORKSPACE_GO:-go}" build -o "$AGENTNET_WORKSPACE_WORLD/tls-proxy" internal/ui/testdata/human_navigation_tls_proxy.go
for realm in a b; do
  backend=$(cat "$AGENTNET_WORKSPACE_WORLD/backend-$realm")
  port=$(cat "$AGENTNET_WORKSPACE_WORLD/mux-$realm")
  "$AGENTNET_WORKSPACE_BINARY" hub serve --data "$AGENTNET_WORKSPACE_WORLD/hub-$realm" --listen "127.0.0.1:$backend" --platform-tls --web --public-url "https://127.0.0.1:$port" --browser-origin "https://127.0.0.1:$(cat "$AGENTNET_WORKSPACE_WORLD/mux-a")" --browser-origin "https://127.0.0.1:$(cat "$AGENTNET_WORKSPACE_WORLD/mux-b")" > "$AGENTNET_WORKSPACE_WORLD/hub-$realm.log" 2>&1 & owned+=($!)
  printf '%s\n' "$!" > "$AGENTNET_WORKSPACE_WORLD/hub-$realm-pid"
  "$AGENTNET_WORKSPACE_WORLD/tls-proxy" "127.0.0.1:$port" "127.0.0.1:$backend" "$AGENTNET_WORKSPACE_WORLD/cert.pem" "$AGENTNET_WORKSPACE_WORLD/key.pem" > "$AGENTNET_WORKSPACE_WORLD/mux-$realm.log" 2>&1 & owned+=($!)
  for attempt in {1..100}; do [ -f "$AGENTNET_WORKSPACE_WORLD/hub-$realm/bootstrap-invite.txt" ] && break; sleep .1; done
  sleep .3
  account=alice-a; agent=laptop
  if [ "$realm" = b ]; then account=owner-b; agent=owner; fi
  "$AGENTNET_WORKSPACE_BINARY" --home "$AGENTNET_WORKSPACE_WORLD/$account" join --agent "$agent" "$("$AGENTNET_WORKSPACE_BINARY" hub bootstrap-invite --raw --data "$AGENTNET_WORKSPACE_WORLD/hub-$realm")" > "$AGENTNET_WORKSPACE_WORLD/join-$realm.log"
  "$AGENTNET_WORKSPACE_BINARY" --home "$AGENTNET_WORKSPACE_WORLD/bob-$realm" join --agent desk "$("$AGENTNET_WORKSPACE_BINARY" --home "$AGENTNET_WORKSPACE_WORLD/$account" admin invite --raw bob)" >> "$AGENTNET_WORKSPACE_WORLD/join-$realm.log"
  for user in "$account" "bob-$realm"; do
    "$AGENTNET_WORKSPACE_BINARY" --home "$AGENTNET_WORKSPACE_WORLD/$user" daemon --ui 127.0.0.1:0 > "$AGENTNET_WORKSPACE_WORLD/$user.log" 2>&1 & owned+=($!)
  done
  sleep .6
  "$AGENTNET_WORKSPACE_BINARY" --home "$AGENTNET_WORKSPACE_WORLD/bob-$realm" person create Bob > "$AGENTNET_WORKSPACE_WORLD/person-$realm.log"
  "$AGENTNET_WORKSPACE_BINARY" --home "$AGENTNET_WORKSPACE_WORLD/bob-$realm" ui > "$AGENTNET_WORKSPACE_WORLD/bob-$realm-ui"
done
"$AGENTNET_WORKSPACE_BINARY" --home "$AGENTNET_WORKSPACE_WORLD/alice-a" person create Alice > "$AGENTNET_WORKSPACE_WORLD/person-a.log"
"$AGENTNET_WORKSPACE_BINARY" --home "$AGENTNET_WORKSPACE_WORLD/alice-a" person link > "$AGENTNET_WORKSPACE_WORLD/link-a"
"$AGENTNET_WORKSPACE_BINARY" --home "$AGENTNET_WORKSPACE_WORLD/alice-a" ui > "$AGENTNET_WORKSPACE_WORLD/alice-a-ui"
for platform in daemon browser; do
 "$AGENTNET_WORKSPACE_BINARY" --home "$AGENTNET_WORKSPACE_WORLD/owner-b" admin invite --link admin > "$AGENTNET_WORKSPACE_WORLD/invite-b-$platform"
done
printf '%s\n' "${owned[@]}" > "$AGENTNET_WORKSPACE_WORLD/owned-pids"
node internal/ui/testdata/workspaces_acceptance_journey.cjs
