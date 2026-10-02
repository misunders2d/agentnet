#!/usr/bin/env bash
# Real native providers/relay, synthetic configured executors; no user runtime.
set -eu
umask 077
: "${AGENTNET_HUMAN_BINARY:?}" "${AGENTNET_HUMAN_WORLD:?}" "${AGENTNET_SCREENSHOTS:?}"
export AGENTNET_NOTIFY=off
mkdir "$AGENTNET_HUMAN_WORLD"
mkdir "$AGENTNET_HUMAN_WORLD/bin" "$AGENTNET_HUMAN_WORLD/work-A" "$AGENTNET_HUMAN_WORLD/work-B" "$AGENTNET_HUMAN_WORLD/work-C" "$AGENTNET_HUMAN_WORLD/cli-home"
export HOME="$AGENTNET_HUMAN_WORLD/cli-home"
cat > "$AGENTNET_HUMAN_WORLD/bin/codex" <<'PYCODE'
#!/usr/bin/python3
import hashlib,json,os,re,sys
prompt=sys.stdin.read()
record={'cwd':os.getcwd(),'args':sys.argv[1:],'prompt':prompt,'files':[]}
for name,count,wanted,filename in re.findall(r'Verified attachment "([^"\n]+)" \((\d+) bytes, SHA256 ([0-9a-f]+)\) is at "([^"\n]+)"',prompt):
    assert filename.startswith(os.environ['AGENTNET_HUMAN_WORLD']+'/alice/opened/.agentnet-apx-')
    data=open(filename,'rb').read()
    assert len(data)==int(count) and hashlib.sha256(data).hexdigest()==wanted
    record['files'].append({'name':name,'bytes':list(data),'sha256':wanted})
answer='SYNTHETIC REMOTE VERIFIED ANSWER' if os.getcwd().endswith('work-B') else 'SYNTHETIC LOCAL CONTINUATION COMPLETE'
with open(os.environ['AGENTNET_HUMAN_WORLD']+'/harness.jsonl','a') as f:f.write(json.dumps(record)+'\n')
if '-o' in sys.argv:open(sys.argv[sys.argv.index('-o')+1],'w').write(answer)
if '--json' in sys.argv:
    print(json.dumps({'type':'thread.started','thread_id':'synthetic-'+os.path.basename(os.getcwd())}))
    print(json.dumps({'type':'item.completed','item':{'type':'agent_message','text':answer}}))
    print(json.dumps({'type':'turn.completed','usage':{}}))
else:print(answer)
PYCODE
chmod 700 "$AGENTNET_HUMAN_WORLD/bin/codex"
export PATH="$AGENTNET_HUMAN_WORLD/bin:$PATH"
free_port(){ python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1])'; }
openssl req -x509 -newkey rsa:2048 -nodes -keyout "$AGENTNET_HUMAN_WORLD/key.pem" -out "$AGENTNET_HUMAN_WORLD/cert.pem" -days 1 -subj /CN=127.0.0.1 -addext subjectAltName=IP:127.0.0.1 > "$AGENTNET_HUMAN_WORLD/cert.log" 2>&1
export SSL_CERT_FILE="$AGENTNET_HUMAN_WORLD/cert.pem"
owned=()
cleanup(){ for pid in "${owned[@]}"; do kill "$pid" 2>/dev/null || true; done; for pid in "${owned[@]}"; do wait "$pid" 2>/dev/null || true; done; }
trap cleanup EXIT
"${AGENTNET_HUMAN_GO:-go}" build -o "$AGENTNET_HUMAN_WORLD/tls-proxy" internal/ui/testdata/human_navigation_tls_proxy.go
for realm in a b; do
 backend=$(free_port); port=$(free_port); printf '%s\n' "$port" > "$AGENTNET_HUMAN_WORLD/mux-$realm"
 "$AGENTNET_HUMAN_BINARY" hub serve --data "$AGENTNET_HUMAN_WORLD/hub-$realm" --listen "127.0.0.1:$backend" --platform-tls --web --public-url "https://127.0.0.1:$port" > "$AGENTNET_HUMAN_WORLD/hub-$realm.log" 2>&1 & owned+=($!)
 "$AGENTNET_HUMAN_WORLD/tls-proxy" "127.0.0.1:$port" "127.0.0.1:$backend" "$AGENTNET_HUMAN_WORLD/cert.pem" "$AGENTNET_HUMAN_WORLD/key.pem" > "$AGENTNET_HUMAN_WORLD/mux-$realm.log" 2>&1 & owned+=($!)
 for attempt in {1..100}; do [ -f "$AGENTNET_HUMAN_WORLD/hub-$realm/bootstrap-invite.txt" ] && break; sleep .1; done
 sleep .3
 "$AGENTNET_HUMAN_BINARY" hub bootstrap-invite --raw --data "$AGENTNET_HUMAN_WORLD/hub-$realm" > "$AGENTNET_HUMAN_WORLD/invite-$realm"
done
"$AGENTNET_HUMAN_BINARY" --home "$AGENTNET_HUMAN_WORLD/alice" join --agent laptop "$(cat "$AGENTNET_HUMAN_WORLD/invite-a")" > "$AGENTNET_HUMAN_WORLD/join.log"
"$AGENTNET_HUMAN_BINARY" --home "$AGENTNET_HUMAN_WORLD/bob" join --agent host "$("$AGENTNET_HUMAN_BINARY" --home "$AGENTNET_HUMAN_WORLD/alice" admin invite --raw bob)" >> "$AGENTNET_HUMAN_WORLD/join.log"
"$AGENTNET_HUMAN_BINARY" --home "$AGENTNET_HUMAN_WORLD/alice" responder set --harness codex --dir "$AGENTNET_HUMAN_WORLD/work-C" > "$AGENTNET_HUMAN_WORLD/default.log"
for account in alice bob; do
 "$AGENTNET_HUMAN_BINARY" --home "$AGENTNET_HUMAN_WORLD/$account" daemon --ui 127.0.0.1:0 > "$AGENTNET_HUMAN_WORLD/$account.log" 2>&1 & owned+=($!)
done
printf '%s\n' "${owned[@]}" > "$AGENTNET_HUMAN_WORLD/owned-pids"
sleep 1
"$AGENTNET_HUMAN_BINARY" --home "$AGENTNET_HUMAN_WORLD/bob" person service > "$AGENTNET_HUMAN_WORLD/person.log"
"$AGENTNET_HUMAN_BINARY" --home "$AGENTNET_HUMAN_WORLD/bob" approve admin/laptop > "$AGENTNET_HUMAN_WORLD/approval.log"
"$AGENTNET_HUMAN_BINARY" --home "$AGENTNET_HUMAN_WORLD/bob" send --wait 0 admin/laptop 'Synthetic remote executor conversation' > "$AGENTNET_HUMAN_WORLD/seed.log"
for account in alice bob; do "$AGENTNET_HUMAN_BINARY" --home "$AGENTNET_HUMAN_WORLD/$account" ui > "$AGENTNET_HUMAN_WORLD/$account-ui"; done
node "${AGENTNET_RECEIVER_JOURNEY:-internal/ui/testdata/receiver_ui_journey.cjs}"
