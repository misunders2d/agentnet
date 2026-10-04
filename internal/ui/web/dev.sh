#!/bin/sh
# Development build of the Comic skin package into OUT (default: a private
# temp dir), never into ../static: dev.sh OUT. OUT then holds a complete
# package (skin.json, entry.mjs, style.css, document.css, m/) as build.sh
# makes it, unminified with inline source maps. Dependencies are installed
# once per lockfile into a cache outside the repository. Use build.sh for
# the checked-in output; dev-proxy.mjs serves OUT in front of a daemon.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
out=${1:?usage: dev.sh OUT}
mkdir -p "$out"
sum=$(sha256sum "$here/package-lock.json" | cut -c1-16)
cache=${AGENTNET_WEB_CACHE:-/tmp/claude-1000/webdev}/$sum
if [ ! -d "$cache/node_modules" ]; then
	mkdir -p "$cache"
	cp "$here/package.json" "$here/package-lock.json" "$cache/"
	(cd "$cache" && npm ci --ignore-scripts --no-audit --no-fund >/dev/null)
fi
# A link (gitignored) lets esbuild and Tailwind resolve packages from src/.
ln -sfn "$cache/node_modules" "$here/node_modules"
cd "$here"
rm -rf "$out/m" "$out/entry.mjs" "$out/style.css" "$out/document.css" "$out/skin.json"
./node_modules/.bin/esbuild src/main.tsx --bundle --format=esm --platform=browser --target=es2022 \
	--jsx=automatic --define:process.env.NODE_ENV='"development"' --sourcemap=inline --log-level=warning --outfile="$out/entry.mjs"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
./node_modules/.bin/tailwindcss -i src/styles.css -o "$work/tailwind.css" >/dev/null 2>&1
cp -R "$here/../static/skins/comic/m" "$out/m"
node "$here/package.mjs" "$work/tailwind.css" src/document.css "$out"
