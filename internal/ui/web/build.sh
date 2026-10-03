#!/bin/sh
# Rebuilds the default messenger interface: ../static/messenger.mjs (the
# React app, one ES module) and ../static/messenger.css (Tailwind, compiled
# to one static file), from src/ and the exact package graph in
# package-lock.json. Also writes LICENSES.txt (every bundled package's
# license) and SHA256SUMS (the outputs, checked by the Go tests).
#
# Needs node, npm and access to the npm registry. npm ci checks every
# package against the lockfile's integrity hashes and runs no install
# scripts. Nothing is fetched when AgentNet runs: the output is checked in
# and embedded in the binary. The build is deterministic, so running this
# again must leave `git diff` empty.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
out="$here/../static"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
cp -R "$here/package.json" "$here/package-lock.json" "$here/tsconfig.json" "$here/src" "$work/"
cd "$work"
npm ci --ignore-scripts --no-audit --no-fund >/dev/null
./node_modules/.bin/tsc -p tsconfig.json
./node_modules/.bin/esbuild src/main.tsx --bundle --format=esm --platform=browser --target=es2022 \
	--jsx=automatic --minify --legal-comments=eof --define:process.env.NODE_ENV='"production"' \
	--log-level=warning --outfile="$out/messenger.mjs"
./node_modules/.bin/tailwindcss -i src/styles.css -o "$out/messenger.css" --minify >/dev/null 2>&1
# Fonts (OFL) and emoji data are served from the page's own origin: no CDN,
# and offline is normal. static/m/ holds exactly these files.
rm -rf "$out/m" && mkdir -p "$out/m/fonts" "$out/m/emoji/en"
for f in onest rubik; do
	for sub in latin latin-ext cyrillic cyrillic-ext; do
		cp "node_modules/@fontsource-variable/$f/files/$f-$sub-wght-normal.woff2" "$out/m/fonts/"
	done
done
cp node_modules/emojibase-data/en/data.json node_modules/emojibase-data/en/messages.json "$out/m/emoji/en/"
# The license of every bundled (non-dev) package, from the installed files.
node -e '
const fs = require("fs"), path = require("path");
const lock = JSON.parse(fs.readFileSync("package-lock.json", "utf8"));
let text = "Licenses of the packages bundled into static/messenger.mjs, messenger.css and static/m/ (package-lock.json).\n";
for (const [dir, p] of Object.entries(lock.packages)) {
  if (!dir || p.dev || p.devOptional || !fs.existsSync(dir)) continue; // bundled packages only (optional ones absent here are not bundled)
  const name = dir.slice(dir.lastIndexOf("node_modules/") + "node_modules/".length);
  const lic = fs.readdirSync(dir).find((f) => /^licen[cs]e/i.test(f));
  text += "\n==== " + name + " " + p.version + " (" + p.license + ")\n\n" + (lic ? fs.readFileSync(path.join(dir, lic), "utf8").trim() : "(no license file shipped; see the package metadata)") + "\n";
}
fs.writeFileSync(process.argv[1], text);
' "$here/LICENSES.txt"
cd "$out" && sha256sum messenger.mjs messenger.css $(find m -type f | LC_ALL=C sort) > "$here/SHA256SUMS"
