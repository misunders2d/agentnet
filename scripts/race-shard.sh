#!/bin/bash
# One shard of the race suite, for CI's parallel race jobs. "others" runs all
# non-client packages, isolating internal/ui in two sequential test partitions.
# "ui-0"/"ui-1" rerun only those UI partitions. "client-K" is every COUNT-th
# client test starting at K (0-based). UI always uses two partitions; COUNT is
# client-only. The full others and client shards run every test once, with each
# race invocation bounded at 600 seconds per package.
# Usage: scripts/race-shard.sh others|client-K|ui-0|ui-1 [COUNT]
set -euo pipefail
cd "$(dirname "$0")/.."
shard=${1:-}
count=${2:-12}
if ! [[ "$count" =~ ^[1-9][0-9]*$ ]] ||
   ! [[ "$shard" = others || "$shard" =~ ^(client|ui)-[0-9]+$ ]]; then
	echo 'usage: race-shard.sh others|client-K|ui-0|ui-1 [positive client COUNT]' >&2
	exit 2
fi

run_partition() {
	local package=$1 k=$2 n=$3 label=$4 tests
	# Saved measurements on the final list overload client-5/6/7/10.
	# Keep twelve partitions and every test; move only fixtures to room.
	tests=$(go test -list '.*' "$package" | awk -v k="$k" -v n="$n" -v client="$([[ $package = ./internal/client ]] && echo 1 || echo 0)" '
		/^Test/ {
			part=i++%n
			if (client && n==12) {
				if ($0=="TestGroupInteractionNamedMemberAndVisitor") part=1
				if ($0=="TestGroupParticipationHistoryAfterHostReadmission") part=7
				if ($0=="TestGroupReplyReceiverMemberAndVisitorPIDNative") part=11
				if ($0=="TestOwnPersonTasksDirectAndGroup") part=6
				if ($0=="TestGroupParticipationFilesLateLinkedRequestAndNamedOutput") part=9
				if ($0=="TestGroupHistoryWitnessRetiredTaskDevice") part=4
				if ($0=="TestRoomReciprocalRootQuestions") part=5
				if ($0=="TestHistoryArchiveImportedTailRemainsBounded") part=10
				if ($0=="TestP6AgentQuestionTaskCorrelationAndOrigin") part=2
				if ($0=="TestRoomBusyAncestorCallback") part=1
				if ($0=="TestReceiverRemoteGroupNamedNativeMemberVisitor") part=0
				if ($0=="TestProactiveAgentMessageAdmission") part=2
			}
			if (part==k) print
		}')
	if [ -z "$tests" ]; then
		echo "shard $label of $n has no tests"
		return
	fi
	echo "shard $label of $n: $(echo "$tests" | wc -l) tests"
	go test -race -json -count=1 -timeout 600s -run "^($(echo "$tests" | paste -sd'|'))\$" "$package"
}

if [ "$shard" = others ]; then
	packages=$(go list ./... | awk '!/\/internal\/(client|ui)$/')
	go test -race -count=1 -timeout 600s $packages
	run_partition ./internal/ui 0 2 ui-0
	run_partition ./internal/ui 1 2 ui-1
	exit
fi
k=${shard#*-}
package=./internal/client
partitions=$count
if [[ "$shard" = ui-* ]]; then
	package=./internal/ui
	partitions=2
fi
if (( k >= partitions )); then
	echo "shard $shard is outside count $partitions" >&2
	exit 2
fi
run_partition "$package" "$k" "$partitions" "$shard"
