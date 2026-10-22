#!/bin/sh
# Rebuilds the Comic skin, AgentNet's default interface, as a complete skin
# package (docs/UI_SKINS.md) in ../static/skins/comic/:
#   skin.json     the manifest: api 1, id, name, entry, style, document and
#                 every file the package uses
#   entry.mjs     the React app's ES module (mount/unmount)
#   style.css     Tailwind, compiled to one static file (in the skin's shadow tree)
#   document.css  the rules that only work at document level: the fonts'
#                 @font-face and Tailwind's @property registrations
#   m/            fonts (OFL), emoji data and lazy media modules
# from src/ and the exact package graph in package-lock.json. Also writes
# LICENSES.txt (every bundled package's license) and SHA256SUMS (exactly the
# package's files, checked by the Go tests).
#
# Needs node, npm and access to the npm registry. npm ci checks every
# package against the lockfile's integrity hashes and runs no install
# scripts. Nothing is fetched when AgentNet runs: the output is checked in
# and embedded in the binary. The build is deterministic, so running this
# again must leave `git diff` empty.
#
# The same build makes an overlay skin (src/skin/README.md): Comic's
# interface with a skin layer of its own. Set
#   SKIN_OVERLAY   a directory whose files replace src/skin/ (and fonts/*.woff2,
#                  bundled instead of Comic's fonts)
#   SKIN_OUT       where the package goes, SKIN_MANIFEST where skin.json goes
#   SKIN_ID, SKIN_NAME, SKIN_AGENTNET   the manifest's id, name and agentnet
#   SKIN_LICENSES, SKIN_SUMS            where LICENSES.txt and SHA256SUMS go
# Unset, it builds Comic into static/skins/comic exactly as before.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
out="${SKIN_OUT:-$here/../static/skins/comic}"
overlay="${SKIN_OVERLAY:-}"
if [ -n "$overlay" ]; then overlay=$(cd "$overlay" && pwd); mkdir -p "$out"; out=$(cd "$out" && pwd); fi
licenses="${SKIN_LICENSES:-$here/LICENSES.txt}"
sums="${SKIN_SUMS:-$here/SHA256SUMS}"
export SKIN_ID="${SKIN_ID:-comic}" SKIN_NAME="${SKIN_NAME:-Comic}" SKIN_AGENTNET="${SKIN_AGENTNET:-}"
manifest="${SKIN_MANIFEST:-$out/skin.json}"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
cp -R "$here/package.json" "$here/package-lock.json" "$here/tsconfig.json" "$here/src" "$here/package.mjs" "$work/"
cp "$here/../static/pictures.mjs" "$here/../static/pictures.d.mts" "$work/src/"
cp "$here/../static/optimistic.mjs" "$here/../static/optimistic.d.mts" "$work/src/"
cp "$here/../skins/shared/person-topics.mjs" "$here/../skins/shared/reaction-preferences.mjs" "$here/../skins/shared/collective-mentions.mjs" "$work/src/"
# Bundle the existing presentation-only Drive widgets; transports always
# come from Comic's captured host. No dependency on page globals/assets.
cp "$here/../skins/shared/drivespace.mjs" "$work/src/drive-space.mjs"
cp "$here/../skins/shared/drivespace-setup.mjs" "$work/src/drive-setup.mjs"
if [ -n "$overlay" ]; then
	# The overlay's skin layer replaces Comic's, file by file; fonts/ is not source.
	(cd "$overlay" && node -e '
const fs = require("node:fs"), path = require("node:path");
for (const file of fs.readdirSync(".", {recursive:true}).sort()) {
  if (file.startsWith("fonts/") || !fs.statSync(file).isFile()) continue;
  const target = path.join(process.argv[1], file);
  fs.mkdirSync(path.dirname(target), {recursive:true}); fs.copyFileSync(file, target);
}' "$work/src/skin")
fi
cd "$work"
npm ci --ignore-scripts --no-audit --no-fund >/dev/null
./node_modules/.bin/tsc -p tsconfig.json
# Reuse the pinned lexer for raw-link labels in the plain-text skin packages
# (Classic and Zoom, in agentnet-skins). Only when building Comic in place:
# an overlay build writes nothing here.
[ -n "$overlay" ] || ./node_modules/.bin/esbuild src/linkText.ts --bundle --format=esm --platform=browser --target=es2022 \
  --minify --legal-comments=eof --log-level=warning --outfile="$here/../skins/shared/link-text.mjs"
[ -n "$overlay" ] || { printf '\n/*!\n'; cat node_modules/marked/LICENSE; printf '\n*/\n'; } >> "$here/../skins/shared/link-text.mjs"
rm -rf "$out" && mkdir -p "$out"
./node_modules/.bin/esbuild src/main.tsx --bundle --splitting --format=esm --platform=browser --target=es2022 \
	--jsx=automatic --minify --legal-comments=eof --define:process.env.NODE_ENV='"production"' \
	--log-level=warning --outdir="$out" --entry-names=entry --chunk-names='m/chunks/[name]-[hash]' --out-extension:.js=.mjs
./node_modules/.bin/tailwindcss -i src/styles.css -o "$work/tailwind.css" --minify >/dev/null 2>&1
cat "$here/../skins/shared/drivespace.css" >> "$work/tailwind.css"
# Fonts and emoji data are served from the package itself: no CDN, and
# offline is normal. m/ holds exactly these files.
mkdir -p "$out/m/fonts" "$out/m/emoji/en"
if [ -n "$overlay" ] && [ -d "$overlay/fonts" ]; then
	cp "$overlay"/fonts/*.woff2 "$out/m/fonts/"
else
	for f in onest rubik; do
		for sub in latin latin-ext cyrillic cyrillic-ext; do
			cp "node_modules/@fontsource-variable/$f/files/$f-$sub-wght-normal.woff2" "$out/m/fonts/"
		done
	done
fi
cp node_modules/emojibase-data/en/data.json node_modules/emojibase-data/en/messages.json "$out/m/emoji/en/"
# style.css, document.css and skin.json (package.mjs).
node package.mjs "$work/tailwind.css" src/skin/document.css "$out" "$manifest"
# The license of every bundled (non-dev) package, from the installed files.
node -e '
const fs = require("fs"), path = require("path");
const lock = JSON.parse(fs.readFileSync("package-lock.json", "utf8"));
let text = "Licenses of the packages bundled into the " + process.env.SKIN_NAME + " skin package (entry.mjs, style.css, document.css and m/), from package-lock.json." + (process.env.SKIN_ID === "comic" ? "" : " Built on AgentNet Comic (Apache-2.0); fonts in m/fonts come from the skin layer and carry their own licenses there.") + "\n";
for (const [dir, p] of Object.entries(lock.packages)) {
  if (!dir || p.dev || p.devOptional || !fs.existsSync(dir)) continue; // bundled packages only (optional ones absent here are not bundled)
  const name = dir.slice(dir.lastIndexOf("node_modules/") + "node_modules/".length);
  const lic = fs.readdirSync(dir).find((f) => /^licen[cs]e/i.test(f));
  text += "\n==== " + name + " " + p.version + " (" + p.license + ")\n\n" + (lic ? fs.readFileSync(path.join(dir, lic), "utf8").trim() : "(no license file shipped; see the package metadata)") + "\n";
  if (name === "mediabunny") text += "\nUnmodified Mediabunny source, including src/ and its MPL-2.0 license: " + p.resolved + "\n";
}
fs.writeFileSync(process.argv[1], text);
'  "$licenses"
cd "$out" && node -e '
const fs = require("node:fs"), {execFileSync} = require("node:child_process");
const files = fs.readdirSync(".", {recursive:true}).filter(file => fs.statSync(file).isFile()).sort();
process.stdout.write(execFileSync("sha256sum", ["--", ...files]));
' > "$sums"
