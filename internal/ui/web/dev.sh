#!/bin/sh
# Development build of the messenger into OUT (default: a private temp dir),
# never into ../static: dev.sh OUT. Dependencies are installed once per
# lockfile into a cache outside the repository. Use build.sh for the
# checked-in output.
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
./node_modules/.bin/esbuild src/main.tsx --bundle --format=esm --platform=browser --target=es2022 \
	--jsx=automatic --define:process.env.NODE_ENV='"development"' --sourcemap=inline --log-level=warning --outfile="$out/messenger.mjs"
./node_modules/.bin/tailwindcss -i src/styles.css -o "$out/messenger.css" >/dev/null 2>&1
rm -rf "$out/m" && cp -R "$here/../static/m" "$out/m"
