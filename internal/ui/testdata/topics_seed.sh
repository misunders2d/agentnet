#!/usr/bin/env bash
# Seeds a company world (company_world.sh) with many topics between
# Sergey's laptop and Vitalii's assistant (Stocky): 165 topics, 70 of them
# from ten days ago, in every state a topic has (docs/plans/TOPICS.md).
#
# Old history goes through supported paths only: real sends, asks and the
# assistant's real answers, stored while the test-only clock seam moves the
# stored times back (client testclock.go). That seam exists only in a
# binary built with -tags agentnet_testclock, and only reads the file named
# by AGENTNET_TEST_CLOCK_FILE, which must be exported when the world starts
# so its daemons see it too:
#
#   go build -tags agentnet_testclock -o "$BIN" ./cmd/agentnet
#   export AGENTNET_TEST_CLOCK_FILE=$(mktemp) STANDIN_DELAY=0.1
#   AGENTNET_COMPANY_BINARY=$BIN AGENTNET_COMPANY_RUN="topics_seed.sh" company_world.sh
#   (or by hand against a running world: topics_seed.sh WORLD BINARY)
#
# Writes <world>/topics.json with ids of a few topics a journey opens.
set -eu
W=${1:-$AGENTNET_COMPANY_WORLD}
B=${2:-$AGENTNET_COMPANY_BINARY}
: "${AGENTNET_TEST_CLOCK_FILE:?export AGENTNET_TEST_CLOCK_FILE (a binary built with -tags agentnet_testclock reads it)}"
an(){ who=$1; shift; env -i PATH="$W/bin:/usr/bin:/bin" HOME="$W/cli-home" SSL_CERT_FILE="$W/cert.pem" AGENTNET_NOTIFY=off \
	AGENTNET_TEST_CLOCK_FILE="$AGENTNET_TEST_CLOCK_FILE" "$B" --home "$W/$who" "$@"; }
first(){ cut -d' ' -f1; }
stocky=vitalii/desk
laptop=$(an sergey whoami | head -1 | cut -d' ' -f1)
# answered N: wait until Sergey holds N answers from Stocky.
answered(){
	for _ in $(seq 1 600); do
		n=$(an sergey inbox --peek --json | python3 -c 'import json,sys; print(sum(1 for m in (json.load(sys.stdin) or []) if m.get("kind")=="answer" and m.get("from")=="'"$stocky"'"))')
		[ "$n" -ge "$1" ] && return 0
		sleep .3
	done
	echo "only $n of $1 answers arrived" >&2; return 1
}

# Ten days ago: questions Stocky answered (done, then archived), plain
# notes (archived), and questions Stocky said need a person (they still
# wait, so they are never archived).
echo $((-10 * 86400)) > "$AGENTNET_TEST_CLOCK_FILE"
items=("pallets of bath towels in aisle 4" "open orders for Harbor Inn" "returns from the Atlanta store" "stock of white bath sets"
	"damaged cartons from Monday's truck" "hand towels on the mezzanine" "the cycle count for zone B" "SKU 1182 on hand" "the Memphis transfer" "label printer supplies")
for i in $(seq 0 49); do an sergey ask --wait 0 "$stocky" "How many ${items[$((i % 10))]} as of week $((i / 10 + 30))?" > /dev/null; done
for i in $(seq 0 14); do an sergey send --wait 0 "$stocky" "Note for the record: ${items[$((i % 10))]} checked, nothing to do (round $i)." > /dev/null; done
old_wait=()
for i in $(seq 0 4); do old_wait+=("$(an sergey ask --wait 0 "$stocky" "Please decide whether we write off ${items[$i]}" | first)"); done
answered 50
sleep 2
an sergey inbox > /dev/null # Sergey read them back then (listing the inbox marks it read)

# Now: answered questions (done by the agent), follow-ups in one topic,
# notes, tasks waiting for Vitalii's OK, questions Vitalii's agent asks
# Sergey (held for him: needs you) and messages from Vitalii (unread).
echo 0 > "$AGENTNET_TEST_CLOCK_FILE"
for i in $(seq 0 39); do an sergey ask --wait 0 "$stocky" "Where are the ${items[$((i % 10))]} now? (check $i)" > /dev/null; done
for i in $(seq 0 29); do an sergey send --wait 0 "$stocky" "FYI: ${items[$((i % 10))]} moved to staging, batch $i." > /dev/null; done
for i in $(seq 0 9); do an sergey task --wait 0 "$stocky" "Reserve ${items[$i]} for the Savannah rush order" > /dev/null; done
table=$(an sergey ask --wait 0 "$stocky" "Can you give me a table of bath set stock?" | first)
answered 91
followup=$(an sergey inbox --peek --json | python3 -c 'import json,sys; print(next(m["id"] for m in (json.load(sys.stdin) or []) if m.get("reply_to")=="'"$table"'"))')
an sergey ask --wait 0 --reply-to "$followup" "$stocky" "And how long does that last at 80 cases a week?" > /dev/null
answered 92
for i in $(seq 0 7); do an vitalii ask --wait 0 "$laptop" "May I re-slot ${items[$i]} to free aisle $((i + 2))?" > /dev/null; done
for i in $(seq 0 5); do an vitalii send --wait 0 "$laptop" "Heads up: ${items[$((i + 2))]} arrive early tomorrow ($i)." > /dev/null; done
sleep 3
python3 - "$W" "$table" "${old_wait[0]}" <<'PY'
import json, sys
w, table, old_wait = sys.argv[1:]
json.dump({"table": table, "old_waiting": old_wait}, open(w + "/topics.json", "w"), indent=1)
PY
echo "seeded topics: $W/topics.json"
