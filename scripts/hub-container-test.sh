#!/usr/bin/env bash
# Container acceptance test for the Hub image. Builds the image from this
# checkout, then, on a private Docker network with no published ports:
# bootstrap -> invite -> join two agents -> send a 15 MB file while the
# recipient is offline -> replace the Hub container -> recipient downloads the
# exact file -> stop the Hub, back up through stdout, restore into a fresh
# volume, and check the restored Hub serves the old clients.
#
# Everything it creates is named agentnet-mvp-<id>-* and removed on exit
# (only those exact names: never prune). With AGENTNET_TEST_IMAGE set, it
# tests that already-built image (CI builds it from the repository
# Dockerfile) and leaves it in place. Otherwise the Go build runs in a named
# builder container with CPU/memory/process limits and the repository
# Dockerfile assembles the same runtime image from that binary
# (BUILD_STAGE=prebuilt). Base images pulled (golang, distroless) are left in
# place. Needs docker and sha256sum. Usage: scripts/hub-container-test.sh
set -euo pipefail

id=${AGENTNET_TEST_ID:-$(date +%s)-$$}
p=agentnet-mvp-$id
img=${AGENTNET_TEST_IMAGE:-$p-img}
net=$p-net
limits=(--cpus 1 --memory 512m --pids-limit 256)
build_limits=(--cpus 2 --memory 1g --pids-limit 512)
tmp=$(mktemp -d)
containers=() volumes=()

cleanup() {
	status=$?
	for c in "${containers[@]}"; do docker rm -f "$c" >/dev/null 2>&1 || true; done
	for v in "${volumes[@]}"; do docker volume rm "$v" >/dev/null 2>&1 || true; done
	docker network rm "$net" >/dev/null 2>&1 || true
	[ -n "${AGENTNET_TEST_IMAGE:-}" ] || docker image rm "$img" >/dev/null 2>&1 || true
	rm -rf "$tmp"
	echo "cleanup done for $p (exit $status)"
}
trap cleanup EXIT

step() { echo "== $*"; }
vol() { volumes+=("$p-$1"); docker volume create "$p-$1" >/dev/null; }
# agent HOME ARGS...: run the CLI once for the agent whose volume is HOME.
agent() {
	local home=$1; shift
	docker run --rm "${limits[@]}" --network "$net" -v "$p-$home:/data" "$img" --home /data/home "$@"
}
hub() { # hub NAME VOLUME: start a Hub container reachable as "hub"
	containers+=("$p-$1")
	docker run -d --name "$p-$1" "${limits[@]}" --network "$net" --network-alias hub \
		-e AGENTNET_PUBLIC_URL=https://hub:8443 -v "$p-$2:/data" "$img" >/dev/null
	for _ in $(seq 50); do
		docker logs "$p-$1" 2>&1 | grep -q "hub listening" && return 0
		sleep 0.2
	done
	docker logs "$p-$1"; return 1
}
wait_for() { # wait_for DESCRIPTION COMMAND...
	local what=$1; shift
	for _ in $(seq 100); do "$@" >/dev/null 2>&1 && return 0; sleep 0.3; done
	echo "timed out waiting for $what"; return 1
}
copy_out() { # copy_out VOLUME PATH DEST: read a file out of a volume
	local c=$p-copy-$RANDOM
	containers+=("$c") # tracked before creation, so a failed copy is still cleaned up
	docker create --name "$c" -v "$p-$1:/data" "$img" >/dev/null
	docker cp "$c:$2" "$3"
	docker rm "$c" >/dev/null
}

if [ -z "${AGENTNET_TEST_IMAGE:-}" ]; then
	step "build agentnet in a limited builder container"
	mkdir -p "$tmp/ctx"
	containers+=("$p-build")
	docker run --rm --name "$p-build" "${build_limits[@]}" --user "$(id -u):$(id -g)" \
		-v "$PWD:/src:ro" -v "$tmp/ctx:/out" -w /src \
		-e HOME=/tmp -e GOCACHE=/tmp/gocache -e GOMODCACHE=/tmp/gomod -e GOFLAGS=-mod=readonly -e CGO_ENABLED=0 \
		golang:1.26 go build -trimpath -ldflags "-s -w -X github.com/misunders2d/agentnet/internal/protocol.Version=container-test" \
		-o /out/agentnet ./cmd/agentnet
	step "assemble $img with the repository Dockerfile"
	docker build --quiet --build-arg BUILD_STAGE=prebuilt -f Dockerfile --tag "$img" "$tmp/ctx" >/dev/null
fi
docker network create --internal "$net" >/dev/null
vol hubdata; vol alice; vol bob; vol restored

step "bootstrap and enroll"
hub hub1 hubdata
code=$(docker exec "$p-hub1" agentnet hub bootstrap-invite)
agent alice join --agent laptop "$code"
invite=$(agent alice admin invite bob)
agent bob join --agent desk "$invite"

step "send the image's own 15 MB binary while bob is offline"
out=$(agent alice send --file /usr/local/bin/agentnet bob/desk "file while offline")
msg=${out%% *}
echo "$out"

step "replace the Hub container, same volume"
docker rm -f "$p-hub1" >/dev/null
hub hub2 hubdata

step "bob comes online and downloads"
containers+=("$p-bob")
docker run -d --name "$p-bob" "${limits[@]}" --network "$net" -v "$p-bob:/data" "$img" --home /data/home daemon >/dev/null
wait_for "delivery to bob" sh -c "docker exec $p-bob agentnet --home /data/home inbox --json | grep -q $msg"
docker exec "$p-bob" agentnet --home /data/home download --dir /data "$msg"
copy_out bob /data/agentnet "$tmp/received"
copy_out hubdata /usr/local/bin/agentnet "$tmp/original"
sent=$(sha256sum <"$tmp/original" | cut -d' ' -f1)
got=$(sha256sum <"$tmp/received" | cut -d' ' -f1)
[ "$sent" = "$got" ] || { echo "file differs: $sent != $got"; exit 1; }
echo "file identical ($got)"
wait_for "delivered receipt" sh -c "docker run --rm ${limits[*]} --network $net -v $p-alice:/data $img --home /data/home status $msg | grep -q delivered"

step "stop the Hub, back up through stdout, restore into a fresh volume"
docker stop "$p-hub2" >/dev/null
docker run --rm "${limits[@]}" -v "$p-hubdata:/data" "$img" hub backup --out - >"$tmp/hub.tgz"
docker run --rm -i "${limits[@]}" -v "$p-restored:/data" "$img" hub restore --from - --data /data <"$tmp/hub.tgz"
docker rm -f "$p-hub2" >/dev/null
hub hub3 restored
doc=$(agent alice doctor || true) # the alice daemon is not running here
echo "$doc"
grep -q "ok   hub" <<<"$doc" && grep -q "ok   membership" <<<"$doc"
out=$(agent alice send bob/desk "after restore")
wait_for "message after restore" sh -c "docker exec $p-bob agentnet --home /data/home inbox --json | grep -q '${out%% *}'"
docker stop "$p-hub3" >/dev/null
docker run --rm "${limits[@]}" -v "$p-restored:/data" "$img" hub storage

step "PASS"
