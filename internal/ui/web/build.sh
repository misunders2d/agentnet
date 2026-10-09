#!/bin/sh
# Rebuilds the Comic skin, AgentNet's default interface, as a complete skin
# package (docs/UI_SKINS.md) in ../static/skins/comic/:
#   skin.json     the manifest: api 1, id, name, entry, style, document and
#                 every file the package uses
#   entry.mjs     the React app, one ES module (mount/unmount)
#   style.css     Tailwind, compiled to one static file (in the skin's shadow tree)
#   document.css  the rules that only work at document level: the fonts'
#                 @font-face and Tailwind's @property registrations
#   m/            fonts (OFL) and emoji data, fetched relative to the package
# from src/ and the exact package graph in package-lock.json. Also writes
# LICENSES.txt (every bundled package's license) and SHA256SUMS (exactly the
# package's files, checked by the Go tests).
#
# Needs node, npm and access to the npm registry. npm ci checks every
# package against the lockfile's integrity hashes and runs no install
# scripts. Nothing is fetched when AgentNet runs: the output is checked in
# and embedded in the binary. The build is deterministic, so running this
# again must leave `git diff` empty.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
out="$here/../static/skins/comic"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
cp -R "$here/package.json" "$here/package-lock.json" "$here/tsconfig.json" "$here/src" "$here/package.mjs" "$work/"
cp "$here/../static/pictures.mjs" "$here/../static/pictures.d.mts" "$work/src/"
cp "$here/../static/optimistic.mjs" "$here/../static/optimistic.d.mts" "$work/src/"
cp "$here/../skins/shared/person-topics.mjs" "$here/../skins/shared/reaction-preferences.mjs" "$work/src/"
# Bundle the existing presentation-only Drive widgets; transports always
# come from Comic's captured host. No dependency on page globals/assets.
cp "$here/../skins/classic/src/drivespace.mjs" "$work/src/drive-space.mjs"
cp "$here/../skins/classic/src/drivespace-setup.mjs" "$work/src/drive-setup.mjs"
cd "$work"
npm ci --ignore-scripts --no-audit --no-fund >/dev/null
./node_modules/.bin/tsc -p tsconfig.json
# Reuse the pinned lexer for raw-link labels in the plain-text bundled skins.
./node_modules/.bin/esbuild src/linkText.ts --bundle --format=esm --platform=browser --target=es2022 \
  --minify --legal-comments=eof --log-level=warning --outfile="$here/../skins/shared/link-text.mjs"
{ printf '\n/*!\n'; cat node_modules/marked/LICENSE; printf '\n*/\n'; } >> "$here/../skins/shared/link-text.mjs"
rm -rf "$out" && mkdir -p "$out"
./node_modules/.bin/esbuild src/main.tsx --bundle --format=esm --platform=browser --target=es2022 \
	--jsx=automatic --minify --legal-comments=eof --define:process.env.NODE_ENV='"production"' \
	--log-level=warning --outfile="$out/entry.mjs"
./node_modules/.bin/tailwindcss -i src/styles.css -o "$work/tailwind.css" --minify >/dev/null 2>&1
cat "$here/../skins/classic/src/drivespace.css" >> "$work/tailwind.css"
# Fonts and emoji data are served from the package itself: no CDN, and
# offline is normal. m/ holds exactly these files.
mkdir -p "$out/m/fonts" "$out/m/emoji/en"
for f in onest rubik; do
	for sub in latin latin-ext cyrillic cyrillic-ext; do
		cp "node_modules/@fontsource-variable/$f/files/$f-$sub-wght-normal.woff2" "$out/m/fonts/"
	done
done
cp node_modules/emojibase-data/en/data.json node_modules/emojibase-data/en/messages.json "$out/m/emoji/en/"
# style.css, document.css and skin.json (package.mjs).
node package.mjs "$work/tailwind.css" src/document.css "$out"
# The license of every bundled (non-dev) package, from the installed files.
node -e '
const fs = require("fs"), path = require("path");
const lock = JSON.parse(fs.readFileSync("package-lock.json", "utf8"));
let text = "Licenses of the packages bundled into the Comic skin package (static/skins/comic: entry.mjs, style.css, document.css and m/), from package-lock.json.\n";
for (const [dir, p] of Object.entries(lock.packages)) {
  if (!dir || p.dev || p.devOptional || !fs.existsSync(dir)) continue; // bundled packages only (optional ones absent here are not bundled)
  const name = dir.slice(dir.lastIndexOf("node_modules/") + "node_modules/".length);
  const lic = fs.readdirSync(dir).find((f) => /^licen[cs]e/i.test(f));
  text += "\n==== " + name + " " + p.version + " (" + p.license + ")\n\n" + (lic ? fs.readFileSync(path.join(dir, lic), "utf8").trim() : "(no license file shipped; see the package metadata)") + "\n";
}
fs.writeFileSync(process.argv[1], text);
' "$here/LICENSES.txt"
cd "$out" && sha256sum $(find . -type f | sed 's|^\./||' | LC_ALL=C sort) > "$here/SHA256SUMS"
