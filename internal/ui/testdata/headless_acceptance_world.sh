#!/usr/bin/env bash
# Disposable headless clarification/operator UI acceptance; synthetic executor only.
set -eu
umask 077
AGENTNET_HUMAN_BINARY=${AGENTNET_HUMAN_BINARY:?frozen local binary required}
AGENTNET_HUMAN_GO=${AGENTNET_HUMAN_GO:-go}
AGENTNET_HUMAN_WORLD=$(mktemp -d /tmp/agentnet-headless-world.XXXXXX)
export AGENTNET_HUMAN_BINARY AGENTNET_HUMAN_WORLD AGENTNET_NOTIFY=off
printf '%s\n' "$AGENTNET_HUMAN_WORLD"
mkdir "$AGENTNET_HUMAN_WORLD/bin" "$AGENTNET_HUMAN_WORLD/work"
cat > "$AGENTNET_HUMAN_WORLD/bin/codex" <<'PY'
#!/usr/bin/python3
import json, os, sys
prompt = sys.stdin.read()
if 'Riga' in prompt: answer = 'Riga: bring a jacket. Synthetic correlated answer.'
elif 'ambiguous weather' in prompt: answer = 'Which city?'
else: answer = 'Synthetic headless task result.'
with open(os.environ['AGENTNET_HUMAN_WORLD']+'/harness.jsonl', 'a') as f:
    f.write(json.dumps({'args':sys.argv[1:], 'prompt':prompt, 'answer':answer})+'\n')
if '-o' in sys.argv:
    with open(sys.argv[sys.argv.index('-o')+1], 'w') as f: f.write(answer)
if '--json' in sys.argv:
    print(json.dumps({'type':'thread.started','thread_id':'synthetic-headless-thread'}))
    print(json.dumps({'type':'item.completed','item':{'type':'agent_message','text':answer}}))
    print(json.dumps({'type':'turn.completed','usage':{}}))
else: print(answer)
PY
chmod 700 "$AGENTNET_HUMAN_WORLD/bin/codex"
export PATH=$AGENTNET_HUMAN_WORLD/bin:$PATH
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
for account in bob outside; do
  "$AGENTNET_HUMAN_BINARY" --home "$AGENTNET_HUMAN_WORLD/$account" join --agent host "$("$AGENTNET_HUMAN_BINARY" --home "$AGENTNET_HUMAN_WORLD/alice" admin invite --raw "$account")" >> "$AGENTNET_HUMAN_WORLD/join.log"
done
"$AGENTNET_HUMAN_BINARY" --home "$AGENTNET_HUMAN_WORLD/bob" responder set --harness codex --dir "$AGENTNET_HUMAN_WORLD/work" > "$AGENTNET_HUMAN_WORLD/responder.log"
for account in alice bob outside; do
  "$AGENTNET_HUMAN_BINARY" --home "$AGENTNET_HUMAN_WORLD/$account" daemon --ui 127.0.0.1:0 > "$AGENTNET_HUMAN_WORLD/$account.log" 2>&1 & owned_pids+=($!); printf '%s\n' "$!" > "$AGENTNET_HUMAN_WORLD/$account-pid"
done
printf '%s\n' "${owned_pids[@]}" > "$AGENTNET_HUMAN_WORLD/owned-pids"
sleep 1
for account in alice bob outside; do
  if [ "$account" = bob ]; then "$AGENTNET_HUMAN_BINARY" --home "$AGENTNET_HUMAN_WORLD/$account" person service >> "$AGENTNET_HUMAN_WORLD/person.log"; else "$AGENTNET_HUMAN_BINARY" --home "$AGENTNET_HUMAN_WORLD/$account" person create "$account" >> "$AGENTNET_HUMAN_WORLD/person.log"; fi
  "$AGENTNET_HUMAN_BINARY" --home "$AGENTNET_HUMAN_WORLD/$account" ui > "$AGENTNET_HUMAN_WORLD/$account-ui"
done
"$AGENTNET_HUMAN_BINARY" --home "$AGENTNET_HUMAN_WORLD/alice" person link > "$AGENTNET_HUMAN_WORLD/link"
node internal/ui/testdata/headless_acceptance_journey.cjs
