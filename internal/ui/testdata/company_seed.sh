#!/usr/bin/env bash
# Seeds a company world (company_world.sh) with an ordinary week of work:
# two DMs, a group, and approvals. Run it as AGENTNET_COMPANY_RUN, or by
# hand against a running world: company_seed.sh WORLD BINARY.
# Writes <world>/seed.json with the conversation ids.
set -eu
W=${1:-$AGENTNET_COMPANY_WORLD}
B=${2:-$AGENTNET_COMPANY_BINARY}
# Every command runs outside any harness session of the caller, so origin
# return never applies to the seed.
an(){ who=$1; shift; env -i PATH="$W/bin:/usr/bin:/bin" HOME="$W/cli-home" SSL_CERT_FILE="$W/cert.pem" AGENTNET_NOTIFY=off "$B" --home "$W/$who" "$@"; }
person(){ an "$1" person | head -1 | cut -d' ' -f1; }
conv(){ grep -Eo '[0-9a-f]{64}' | head -1; }
vitalii=$(person vitalii); bohdan=$(person bohdan); anna=$(person anna)

dm_v=$(an sergey dm new vitalii/desk | conv)
dm_b=$(an sergey dm new bohdan/desk | conv)
dm_a=$(an anna dm new admin/laptop | conv)
an sergey dm send "$dm_v" "Hey Vitalii — Harbor Inn in Savannah needs 400 cases of the white bath set by Friday." > /dev/null
sleep 1
an vitalii dm send "$dm_v" "Friday is tight. Let me check what we have on the floor." > /dev/null
sleep 1
an sergey dm send "$dm_v" "Thanks. If freight is the problem we can pay for expedited, but Bohdan has to OK anything over budget." > /dev/null
an sergey dm send "$dm_b" 'Heads up: we may need expedited freight for Savannah, around $340 over the order budget.' > /dev/null
sleep 1
an bohdan dm send "$dm_b" "Send me the quote when you have it 👍" > /dev/null
an anna dm send "$dm_a" "Can I promise Harbor Inn the Friday delivery on today's call?" > /dev/null

# The group: the creator invites one person at a time (each invitation is
# bound to the group's state when it is made).
group=$(an sergey group create "Savannah rush order" | python3 -c 'import json,sys; print(json.load(sys.stdin)["state"]["conv"])')
join(){ # person-id account
	inv=$(an sergey group invite "$group" "$1" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
	for _ in {1..100}; do an "$2" group invitations | grep -q "$inv" && break; sleep .2; done
	an "$2" group accept "$inv" > /dev/null
	for _ in {1..150}; do an sergey group invitations | python3 -c 'import json,sys; sys.exit(0 if any(i["id"]=="'"$inv"'" and i["status"]=="published" for i in json.load(sys.stdin)) else 1)' && break; sleep .2; done
}
join "$vitalii" vitalii
join "$bohdan" bohdan
sleep 2
an sergey dm send "$group" "Group for the Harbor Inn rush order. Vitalii: stock, Bohdan: freight budget." > /dev/null
sleep 1
an vitalii dm send "$group" "On it. Counting the floor now." > /dev/null

# Who may ask whom: Vitalii's assistant answers Sergey's questions.
an vitalii approve admin/laptop > /dev/null
python3 - "$W" "$dm_v" "$dm_b" "$dm_a" "$group" <<'PY'
import json, sys
w, dm_v, dm_b, dm_a, group = sys.argv[1:]
json.dump({"dm_vitalii": dm_v, "dm_bohdan": dm_b, "dm_anna": dm_a, "group_savannah": group}, open(w + "/seed.json", "w"), indent=1)
PY
echo "seeded: $W/seed.json"
