#!/bin/bash
# One disjoint native client partition, on its own Windows CI runner. Derive
# the platform's runnable parent names, just as race-shard.sh does; selecting
# an exact parent also runs all of its subtests. Never raise the 1800s bound.
# Usage: bash scripts/native-client-shard.sh PARTITION [COUNT]
set -euo pipefail
cd "$(dirname "$0")/.."
partition=${1:-}
count=${2:-6}
if ! [[ "$partition" =~ ^(0|[1-9][0-9]*)$ && "$count" =~ ^[1-9][0-9]*$ ]] ||
   (( partition >= count )); then
	echo 'usage: native-client-shard.sh PARTITION [positive COUNT]; 0 <= PARTITION < COUNT' >&2
	exit 2
fi

# Go lists names in the same order on every runner for this source/platform.
# i % count assigns each Test, Example and Fuzz seed parent exactly once;
# benchmarks are not part of a normal go test invocation.
tests=$(go test -list '.*' ./internal/client | awk -v k="$partition" -v n="$count" '
	/^(Test|Example|Fuzz)/ {
		if (i++%n==k) print
	}')
if [ -z "$tests" ]; then
	echo "native client partition $partition of $count has no tests" >&2
	exit 1
fi
echo "native client partition $partition of $count: $(echo "$tests" | wc -l) parents"
printf '%s\n' "$tests"
go test -json -count="${AGENTNET_TEST_COUNT:-1}" -timeout 1800s -run "^($(echo "$tests" | paste -sd'|'))\$" ./internal/client
