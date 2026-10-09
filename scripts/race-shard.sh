#!/bin/bash
# One shard of the race suite, for CI's parallel race jobs. "others" is every
# package but internal/client; "client-K" is every COUNT-th test of
# internal/client starting at K (0-based), so that package's long race run
# spreads over parallel runners. Together the shards run every test once.
# Usage: scripts/race-shard.sh others|client-K [COUNT]
set -euo pipefail
cd "$(dirname "$0")/.."
shard=$1
count=${2:-12}
if ! [[ "$count" =~ ^[1-9][0-9]*$ ]] ||
   ! [[ "$shard" = others || "$shard" =~ ^client-[0-9]+$ ]]; then
	echo 'usage: race-shard.sh others|client-K [positive COUNT]' >&2
	exit 2
fi
if [ "$shard" = others ]; then
	exec go test -race -count=1 -timeout 600s $(go list ./... | grep -v '/internal/client$')
fi
k=${shard#client-}
if (( k >= count )); then
	echo "shard $shard is outside count $count" >&2
	exit 2
fi
tests=$(go test -list '.*' ./internal/client | grep '^Test' | awk -v k="$k" -v n="$count" '(NR-1)%n==k')
if [ -z "$tests" ]; then
	echo "shard $shard of $count has no tests"
	exit 0
fi
echo "shard $shard of $count: $(echo "$tests" | wc -l) tests"
exec go test -race -count=1 -timeout 600s -run "^($(echo "$tests" | paste -sd'|'))\$" ./internal/client
