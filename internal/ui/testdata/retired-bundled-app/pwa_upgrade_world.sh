#!/usr/bin/env bash
# Actual private updater/exec/PWA handoff; frozen same-source version stamps only.
set -eu
umask 077
AGENTNET_UPGRADE_SOURCE=${AGENTNET_UPGRADE_SOURCE:?frozen composed source required}
AGENTNET_PWA_WORLD=${AGENTNET_PWA_WORLD:?fresh disposable world required}
AGENTNET_SCREENSHOTS=${AGENTNET_SCREENSHOTS:?private evidence directory required}
AGENTNET_PWA_GO=${AGENTNET_PWA_GO:?qualified Go executable required}
export AGENTNET_UPGRADE_SOURCE AGENTNET_PWA_WORLD AGENTNET_SCREENSHOTS AGENTNET_PWA_GO AGENTNET_NOTIFY=off
mkdir "$AGENTNET_PWA_WORLD"
mkdir -p "$AGENTNET_SCREENSHOTS" "$AGENTNET_PWA_WORLD/install" "$AGENTNET_PWA_WORLD/bin" "$AGENTNET_PWA_WORLD/work"
free_port() { python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1])'; }
release_port=$(free_port)
backend_port=$(free_port)
browser_port=$(free_port)
printf '%s\n' "$release_port" > "$AGENTNET_PWA_WORLD/release-port"
printf '%s\n' "$browser_port" > "$AGENTNET_PWA_WORLD/mux-port"
openssl req -x509 -newkey rsa:2048 -nodes -keyout "$AGENTNET_PWA_WORLD/key.pem" -out "$AGENTNET_PWA_WORLD/cert.pem" -days 1 -subj /CN=127.0.0.1 -addext subjectAltName=IP:127.0.0.1 > "$AGENTNET_PWA_WORLD/cert.log" 2>&1
export SSL_CERT_FILE=$AGENTNET_PWA_WORLD/cert.pem
owned_pids=()
cleanup() {
  for pid in "${owned_pids[@]}"; do kill "$pid" 2>/dev/null || true; done
  for pid in "${owned_pids[@]}"; do wait "$pid" 2>/dev/null || true; done
}
trap cleanup EXIT
cd "$AGENTNET_UPGRADE_SOURCE"
"$AGENTNET_PWA_GO" build -buildvcs=false -ldflags "-X github.com/misunders2d/agentnet/internal/protocol.Version=v9.9.8 -X main.releaseBase=https://127.0.0.1:$release_port/releases" -o "$AGENTNET_PWA_WORLD/old-agentnet" ./cmd/agentnet
"$AGENTNET_PWA_GO" build -buildvcs=false -ldflags '-X github.com/misunders2d/agentnet/internal/protocol.Version=v9.9.9' -o "$AGENTNET_PWA_WORLD/new-agentnet" ./cmd/agentnet
cp "$AGENTNET_PWA_WORLD/old-agentnet" "$AGENTNET_PWA_WORLD/install/agentnet"
export AGENTNET_PWA_BINARY=$AGENTNET_PWA_WORLD/install/agentnet AGENTNET_PWA_ASSET_MANIFEST=$AGENTNET_UPGRADE_SOURCE/assets.sha256
cat > "$AGENTNET_PWA_WORLD/bin/codex" <<'PY'
#!/usr/bin/python3
import json, os, pathlib, sys, time
world = pathlib.Path(os.environ['AGENTNET_PWA_WORLD'])
prompt = sys.stdin.read()
with (world/'harness.jsonl').open('a') as out: out.write(json.dumps({'event':'start','prompt':prompt,'at':time.time()})+'\n')
if 'Upgrade running task' in prompt:
    (world/'running-started').touch()
    end = time.time()+180
    while not (world/'release-running').exists():
        if time.time()>end: raise SystemExit('synthetic hold timed out')
        time.sleep(.05)
answer = 'Synthetic upgrade task result.'
with (world/'harness.jsonl').open('a') as out: out.write(json.dumps({'event':'done','prompt':prompt,'at':time.time()})+'\n')
if '-o' in sys.argv: pathlib.Path(sys.argv[sys.argv.index('-o')+1]).write_text(answer)
if '--json' in sys.argv:
    print(json.dumps({'type':'thread.started','thread_id':'synthetic-upgrade-thread'}))
    print(json.dumps({'type':'item.completed','item':{'type':'agent_message','text':answer}}))
    print(json.dumps({'type':'turn.completed','usage':{}}))
else: print(answer)
PY
chmod 700 "$AGENTNET_PWA_WORLD/bin/codex"
export PATH=$AGENTNET_PWA_WORLD/bin:$PATH
python3 internal/ui/testdata/pwa_upgrade_release.py "$release_port" "$AGENTNET_PWA_WORLD/cert.pem" "$AGENTNET_PWA_WORLD/key.pem" "$AGENTNET_PWA_WORLD/new-agentnet" "$AGENTNET_PWA_WORLD/release-requests.jsonl" > "$AGENTNET_PWA_WORLD/release.log" 2>&1 & owned_pids+=($!)
"$AGENTNET_PWA_BINARY" hub serve --data "$AGENTNET_PWA_WORLD/hub" --listen "127.0.0.1:$backend_port" --platform-tls --web --public-url "https://127.0.0.1:$browser_port" > "$AGENTNET_PWA_WORLD/hub.log" 2>&1 & owned_pids+=($!)
"$AGENTNET_PWA_GO" build -o "$AGENTNET_PWA_WORLD/tls-proxy" internal/ui/testdata/human_navigation_tls_proxy.go
"$AGENTNET_PWA_WORLD/tls-proxy" "127.0.0.1:$browser_port" "127.0.0.1:$backend_port" "$AGENTNET_PWA_WORLD/cert.pem" "$AGENTNET_PWA_WORLD/key.pem" > "$AGENTNET_PWA_WORLD/mux.log" 2>&1 & owned_pids+=($!)
for attempt in {1..100}; do [ -f "$AGENTNET_PWA_WORLD/hub/bootstrap-invite.txt" ] && break; sleep .1; done
sleep .5
"$AGENTNET_PWA_BINARY" --home "$AGENTNET_PWA_WORLD/alice" join --agent laptop "$("$AGENTNET_PWA_BINARY" hub bootstrap-invite --raw --data "$AGENTNET_PWA_WORLD/hub")" > "$AGENTNET_PWA_WORLD/join.log"
"$AGENTNET_PWA_BINARY" --home "$AGENTNET_PWA_WORLD/bob" join --agent desk "$("$AGENTNET_PWA_BINARY" --home "$AGENTNET_PWA_WORLD/alice" admin invite --raw bob)" >> "$AGENTNET_PWA_WORLD/join.log"
"$AGENTNET_PWA_BINARY" --home "$AGENTNET_PWA_WORLD/alice" responder set --harness codex --dir "$AGENTNET_PWA_WORLD/work" > "$AGENTNET_PWA_WORLD/responder.log"
for account in alice bob; do
  ui_port=$(free_port)
  printf '%s\n' "$ui_port" > "$AGENTNET_PWA_WORLD/$account-port"
  "$AGENTNET_PWA_BINARY" --home "$AGENTNET_PWA_WORLD/$account" daemon --ui "127.0.0.1:$ui_port" > "$AGENTNET_PWA_WORLD/$account.log" 2>&1 & owned_pids+=($!)
  printf '%s\n' "$!" > "$AGENTNET_PWA_WORLD/$account-pid"
done
printf '%s\n' "${owned_pids[@]}" > "$AGENTNET_PWA_WORLD/owned-pids"
sleep 1
"$AGENTNET_PWA_BINARY" --home "$AGENTNET_PWA_WORLD/alice" person create Alice > "$AGENTNET_PWA_WORLD/person.log"
"$AGENTNET_PWA_BINARY" --home "$AGENTNET_PWA_WORLD/bob" person create Bob >> "$AGENTNET_PWA_WORLD/person.log"
node internal/ui/testdata/pwa_upgrade_journey.cjs
