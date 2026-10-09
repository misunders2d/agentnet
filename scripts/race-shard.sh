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
	tests=$(go test -list '.*' "$package" | awk -v k="$k" -v n="$n" '/^Test/ { if (i++%n==k) print }')
	if [ -z "$tests" ]; then
		echo "shard $label of $n has no tests"
		return
	fi
	echo "shard $label of $n: $(echo "$tests" | wc -l) tests"
	go test -race -count=1 -timeout 600s -run "^($(echo "$tests" | paste -sd'|'))\$" "$package"
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
