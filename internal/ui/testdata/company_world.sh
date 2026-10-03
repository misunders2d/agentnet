#!/usr/bin/env bash
# A disposable company for the messenger: one Hub behind a local TLS proxy,
# people on their own installations, a linked phone, two assistants run by
# stand-in harnesses (no model, no user profile), and a second Hub as a
# second workspace. Nothing here touches a real home, Hub or harness.
#
#   AGENTNET_COMPANY_BINARY  the agentnet binary under test
#   AGENTNET_COMPANY_WORLD   a new directory for the whole world (created)
#   AGENTNET_COMPANY_GO      go toolchain (builds the TLS proxy)
#   AGENTNET_COMPANY_SEED    1: seed an ordinary week of work (company_seed.sh)
#   AGENTNET_COMPANY_RUN     optional command run once the world is up;
#                            without it the world stays up until interrupted
#
# Writes <world>/urls.json: each installation's page address (with its
# token: keep the world private) and address.
set -eu
umask 077
: "${AGENTNET_COMPANY_BINARY:?}" "${AGENTNET_COMPANY_WORLD:?}" "${AGENTNET_COMPANY_GO:?}"
W=$AGENTNET_COMPANY_WORLD
A=$AGENTNET_COMPANY_BINARY
export AGENTNET_NOTIFY=off
mkdir "$W" "$W/cli-home" "$W/bin" "$W/work"
export HOME="$W/cli-home"

# Stand-in harnesses. Each records its prompt and answers from the request
# text, so journeys can check exactly what an assistant was given. A file
# named in the request is echoed back when it is readable.
cat > "$W/bin/standin" <<'PY'
#!/usr/bin/python3
import json, os, re, sys, time
name = os.path.basename(sys.argv[0])
prompt = sys.stdin.read() if not sys.stdin.isatty() else ""
if "--" in sys.argv and not prompt:
    prompt = sys.argv[sys.argv.index("--") + 1]
log = os.path.join(os.environ["AGENTNET_COMPANY_WORLD"], "harness.jsonl")
with open(log, "a") as f:
    f.write(json.dumps({"harness": name, "args": sys.argv[1:], "prompt": prompt, "cwd": os.getcwd()}) + "\n")
m = re.search(r"\n## (Question|Task|Follow-up)[^\n]*\n(.*)\Z", prompt, re.S)
asked = (m.group(2).strip() if m else prompt.strip()).splitlines()[-1] if prompt.strip() else ""
time.sleep(float(os.environ.get("STANDIN_DELAY", "1.5")))
if "decide" in asked.lower() or "approve" in asked.lower():
    answer = "AGENTNET: NEEDS-HUMAN\nThis needs your decision: " + asked  # worker.go needsHumanMarker
elif "table" in asked.lower():
    answer = "| SKU | Sold (30d) | On hand |\n|---|---:|---:|\n| Bath set, white | 612 | 1,180 |\n| Hand towel | 240 | 410 |\n\nAfter 400 cases: **780 left** (about 5 weeks)."
else:
    answer = "(" + name + " stand-in) I looked at: " + asked[:200]
if "-o" in sys.argv:
    with open(sys.argv[sys.argv.index("-o") + 1], "w") as f:
        f.write(answer)
if "--json" in sys.argv:
    print(json.dumps({"type": "thread.started", "thread_id": "standin-" + name}))
    print(json.dumps({"type": "item.completed", "item": {"type": "agent_message", "text": answer}}))
    print(json.dumps({"type": "turn.completed", "usage": {}}))
elif "-o" not in sys.argv:
    print(answer)
PY
chmod 700 "$W/bin/standin"
for h in codex claude pi; do ln -s standin "$W/bin/$h"; done
export PATH="$W/bin:$PATH"

free_port(){ python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1])'; }
backend=$(free_port); port=$(free_port); other_backend=$(free_port); other_port=$(free_port)
openssl req -x509 -newkey rsa:2048 -nodes -keyout "$W/key.pem" -out "$W/cert.pem" -days 2 -subj /CN=127.0.0.1 -addext subjectAltName=IP:127.0.0.1 > "$W/cert.log" 2>&1
export SSL_CERT_FILE="$W/cert.pem"
owned=()
cleanup(){ for pid in "${owned[@]}"; do kill "$pid" 2>/dev/null || true; done; for pid in "${owned[@]}"; do wait "$pid" 2>/dev/null || true; done; }
trap cleanup EXIT
"$AGENTNET_COMPANY_GO" -C "$(cd "$(dirname "$0")/../../.." && pwd)" build -o "$W/tls-proxy" internal/ui/testdata/human_navigation_tls_proxy.go
hub(){ # data backend public other
	"$A" hub serve --data "$W/$1" --listen "127.0.0.1:$2" --platform-tls --web --public-url "https://127.0.0.1:$3" --browser-origin "https://127.0.0.1:$4" > "$W/$1.log" 2>&1 & owned+=($!)
	"$W/tls-proxy" "127.0.0.1:$3" "127.0.0.1:$2" "$W/cert.pem" "$W/key.pem" > "$W/$1-proxy.log" 2>&1 & owned+=($!)
	for _ in {1..100}; do [ -f "$W/$1/bootstrap-invite.txt" ] && break; sleep .1; done
	sleep .3
}
hub hub "$backend" "$port" "$other_port"
hub other-hub "$other_backend" "$other_port" "$port"
printf '%s\n' "$port" > "$W/hub-port"; printf '%s\n' "$other_port" > "$W/other-hub-port"

# Riverbend Goods: Sergey (admin; laptop + phone), Vitalii (warehouse),
# Bohdan (finance), Anna (sales; no assistant).
"$A" --home "$W/sergey" join --agent laptop "$("$A" hub bootstrap-invite --raw --data "$W/hub")" > "$W/join.log"
for who in vitalii bohdan anna; do
	"$A" --home "$W/$who" join --agent desk "$("$A" --home "$W/sergey" admin invite --raw "$who")" >> "$W/join.log"
done
start(){ "$A" --home "$W/$1" daemon --ui 127.0.0.1:0 > "$W/$1.log" 2>&1 & owned+=($!); }
for who in sergey vitalii bohdan anna; do start "$who"; done
sleep 1
for who in sergey vitalii bohdan anna; do "$A" --home "$W/$who" person create "${who^}" >> "$W/person.log"; done
# Sergey's phone joins as one more device of Sergey.
"$A" --home "$W/sergey" person link > "$W/link-code"
code=$(grep -Eo 'agentnet-[a-z0-9-]+:[^ ]+' "$W/link-code" | head -1)
"$A" --home "$W/sergey-phone" join --agent phone "$code" >> "$W/join.log" 2>&1 &
linker=$!
for _ in {1..100}; do id=$("$A" --home "$W/sergey" person links 2>/dev/null | grep -Eo '^[0-9a-f]{8,}' | head -1 || true); [ -n "$id" ] && break; sleep .1; done
"$A" --home "$W/sergey" person approve "$id" >> "$W/person.log"
wait "$linker"
start sergey-phone
# Assistants: Sergey's laptop runs Zen (codex stand-in); Vitalii's desk
# runs Stocky (claude stand-in).
mkdir -p "$W/work/zen" "$W/work/stocky"
"$A" --home "$W/sergey" responder set --harness codex --dir "$W/work/zen" > /dev/null
"$A" --home "$W/vitalii" responder set --harness claude --dir "$W/work/stocky" > /dev/null
# A second workspace on the other Hub, for switching between organizations.
"$A" --home "$W/elsewhere" join --agent owner "$("$A" hub bootstrap-invite --raw --data "$W/other-hub")" > "$W/other-join.log"
"$A" --home "$W/elsewhere" admin invite --link sergey > "$W/other-invite"
sleep 1
python3 - "$W" "$A" <<'PY'
import json, subprocess, sys
w, a = sys.argv[1], sys.argv[2]
out = {}
for who in ["sergey", "sergey-phone", "vitalii", "bohdan", "anna"]:
    page = subprocess.run([a, "--home", f"{w}/{who}", "ui"], capture_output=True, text=True).stdout.strip()
    me = subprocess.run([a, "--home", f"{w}/{who}", "whoami"], capture_output=True, text=True).stdout.split("\n")[0].strip()
    out[who] = {"page": page.removeprefix("Open: "), "address": me}
json.dump(out, open(f"{w}/urls.json", "w"), indent=1)
PY
printf '%s\n' "${owned[@]}" > "$W/owned-pids"
if [ "${AGENTNET_COMPANY_SEED:-0}" = 1 ]; then
	"$(dirname "$0")/company_seed.sh" "$W" "$A"
fi
if [ -n "${AGENTNET_COMPANY_RUN:-}" ]; then
	sh -c "$AGENTNET_COMPANY_RUN"
else
	echo "company world up: $W/urls.json (Ctrl+C stops it)"
	wait
fi
