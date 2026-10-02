#!/usr/bin/env bash
# Isolated native/relay-browser human groups; no harness/model/user profiles.
set -eu
umask 077
: "${AGENTNET_GROUP_BINARY:?}" "${AGENTNET_GROUP_WORLD:?}" "${AGENTNET_GROUP_EVIDENCE:?}" "${AGENTNET_GROUP_GO:?}"
export AGENTNET_NOTIFY=off
mkdir "$AGENTNET_GROUP_WORLD"
mkdir "$AGENTNET_GROUP_WORLD/cli-home"
export HOME="$AGENTNET_GROUP_WORLD/cli-home"
if [ "${AGENTNET_GROUP_ASSISTANTS:-0}" = 1 ]; then
 mkdir "$AGENTNET_GROUP_WORLD/bin" "$AGENTNET_GROUP_WORLD/work-bob" "$AGENTNET_GROUP_WORLD/work-dana"
 cat > "$AGENTNET_GROUP_WORLD/bin/codex" <<'PY'
#!/usr/bin/python3
import hashlib,json,os,re,sys,time
prompt=sys.stdin.read();record={'args':sys.argv[1:],'prompt':prompt,'files':[]}
for name,count,wanted,filename in re.findall(r'Selected file "([^"]+)" \((\d+) bytes, SHA256 ([0-9a-f]+)\).*available at "([^"]+)"',prompt):
 assert filename.startswith(os.environ['AGENTNET_GROUP_WORLD']+'/')
 with open(filename,'rb') as f:data=f.read()
 assert len(data)==int(count) and hashlib.sha256(data).hexdigest()==wanted
 record['files'].append({'name':name,'size':len(data),'sha256':wanted,'bytes':list(data)})
with open(os.environ['AGENTNET_GROUP_WORLD']+'/harness.jsonl','a') as f:f.write(json.dumps(record)+'\n')
time.sleep(1)
answer='Synthetic group report: exact selected context and files checked.'
if '-o' in sys.argv:
 with open(sys.argv[sys.argv.index('-o')+1],'w') as f:f.write(answer)
if '--json' in sys.argv:
 print(json.dumps({'type':'thread.started','thread_id':'synthetic-group-thread'}))
 print(json.dumps({'type':'item.completed','item':{'type':'agent_message','text':answer}}))
 print(json.dumps({'type':'turn.completed','usage':{}}))
else:print(answer)
PY
 chmod 700 "$AGENTNET_GROUP_WORLD/bin/codex"
 export PATH="$AGENTNET_GROUP_WORLD/bin:$PATH"
fi
free_port(){ python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1])'; }
backend=$(free_port);port=$(free_port);other_backend=$(free_port);other_port=$(free_port);printf '%s\n' "$port" > "$AGENTNET_GROUP_WORLD/mux-port"
openssl req -x509 -newkey rsa:2048 -nodes -keyout "$AGENTNET_GROUP_WORLD/key.pem" -out "$AGENTNET_GROUP_WORLD/cert.pem" -days 1 -subj /CN=127.0.0.1 -addext subjectAltName=IP:127.0.0.1 > "$AGENTNET_GROUP_WORLD/cert.log" 2>&1
export SSL_CERT_FILE="$AGENTNET_GROUP_WORLD/cert.pem"
owned=()
cleanup(){ for pid in "${owned[@]}"; do kill "$pid" 2>/dev/null || true; done;for pid in "${owned[@]}";do wait "$pid" 2>/dev/null || true;done; }
trap cleanup EXIT
"$AGENTNET_GROUP_GO" -C "$PWD" build -o "$AGENTNET_GROUP_WORLD/tls-proxy" internal/ui/testdata/human_navigation_tls_proxy.go
"$AGENTNET_GROUP_BINARY" hub serve --data "$AGENTNET_GROUP_WORLD/hub" --listen "127.0.0.1:$backend" --platform-tls --web --public-url "https://127.0.0.1:$port" --browser-origin "https://127.0.0.1:$other_port" > "$AGENTNET_GROUP_WORLD/hub.log" 2>&1 & owned+=($!)
"$AGENTNET_GROUP_WORLD/tls-proxy" "127.0.0.1:$port" "127.0.0.1:$backend" "$AGENTNET_GROUP_WORLD/cert.pem" "$AGENTNET_GROUP_WORLD/key.pem" > "$AGENTNET_GROUP_WORLD/mux.log" 2>&1 & owned+=($!)
for attempt in {1..100};do [ -f "$AGENTNET_GROUP_WORLD/hub/bootstrap-invite.txt" ] && break;sleep .1;done
sleep .4
"$AGENTNET_GROUP_BINARY" --home "$AGENTNET_GROUP_WORLD/alice" join --agent laptop "$("$AGENTNET_GROUP_BINARY" hub bootstrap-invite --raw --data "$AGENTNET_GROUP_WORLD/hub")" > "$AGENTNET_GROUP_WORLD/join.log"
for account in bob dana;do
 "$AGENTNET_GROUP_BINARY" --home "$AGENTNET_GROUP_WORLD/$account" join --agent desk "$("$AGENTNET_GROUP_BINARY" --home "$AGENTNET_GROUP_WORLD/alice" admin invite --raw "$account")" >> "$AGENTNET_GROUP_WORLD/join.log"
done
for account in alice bob dana;do
 "$AGENTNET_GROUP_BINARY" --home "$AGENTNET_GROUP_WORLD/$account" daemon --ui 127.0.0.1:0 > "$AGENTNET_GROUP_WORLD/$account.log" 2>&1 & owned+=($!)
done
"$AGENTNET_GROUP_BINARY" hub serve --data "$AGENTNET_GROUP_WORLD/other-hub" --listen "127.0.0.1:$other_backend" --platform-tls --web --public-url "https://127.0.0.1:$other_port" --browser-origin "https://127.0.0.1:$port" > "$AGENTNET_GROUP_WORLD/other-hub.log" 2>&1 & owned+=($!)
"$AGENTNET_GROUP_WORLD/tls-proxy" "127.0.0.1:$other_port" "127.0.0.1:$other_backend" "$AGENTNET_GROUP_WORLD/cert.pem" "$AGENTNET_GROUP_WORLD/key.pem" > "$AGENTNET_GROUP_WORLD/other-mux.log" 2>&1 & owned+=($!)
for attempt in {1..100};do [ -f "$AGENTNET_GROUP_WORLD/other-hub/bootstrap-invite.txt" ] && break;sleep .1;done
sleep .3
"$AGENTNET_GROUP_BINARY" --home "$AGENTNET_GROUP_WORLD/other-owner" join --agent owner "$("$AGENTNET_GROUP_BINARY" hub bootstrap-invite --raw --data "$AGENTNET_GROUP_WORLD/other-hub")" > "$AGENTNET_GROUP_WORLD/other-join.log"
for platform in browser daemon;do "$AGENTNET_GROUP_BINARY" --home "$AGENTNET_GROUP_WORLD/other-owner" admin invite --link admin > "$AGENTNET_GROUP_WORLD/other-invite-$platform";done
printf '%s\n' "$other_port" > "$AGENTNET_GROUP_WORLD/other-port"
printf '%s\n' "${owned[@]}" > "$AGENTNET_GROUP_WORLD/owned-pids"
sleep 1
for account in alice bob dana;do
 "$AGENTNET_GROUP_BINARY" --home "$AGENTNET_GROUP_WORLD/$account" person create "${account^}" >> "$AGENTNET_GROUP_WORLD/person.log"
 "$AGENTNET_GROUP_BINARY" --home "$AGENTNET_GROUP_WORLD/$account" ui > "$AGENTNET_GROUP_WORLD/$account-ui"
done
"$AGENTNET_GROUP_BINARY" --home "$AGENTNET_GROUP_WORLD/alice" person link > "$AGENTNET_GROUP_WORLD/link"
node "${AGENTNET_GROUP_JOURNEY:-internal/ui/testdata/group_web_journey.cjs}"
