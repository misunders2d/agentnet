#!/bin/sh
# Rebuilds ../static/vendor/age.mjs, the age library (typage, the official
# TypeScript age implementation) a browser device encrypts and decrypts
# messages with, ../static/vendor/qr.mjs, the QR encoder the pages draw a
# new device's link with, ../static/vendor/idb.mjs (idb: promises over
# IndexedDB, the device store's plumbing) and ../static/vendor/sse.mjs
# (eventsource-parser: the framing of the signed event stream), from the
# exact package graph in package-lock.json: age-encryption 0.3.1 and its
# noble and scure dependencies, qr (encoder only), idb 8.0.3 and
# eventsource-parser 4.1.1, each bundled by esbuild 0.28.2 into one ES
# module. Also writes LICENSES.txt and SHA256SUMS.
#
# Needs node, npm and access to the npm registry. npm ci checks every
# package against the lockfile's integrity hashes and runs no install
# scripts. Nothing is fetched when AgentNet runs: the output is checked in
# and embedded in the binary. The build is deterministic, so running this
# again must leave `git diff` empty; wire_test.go checks SHA256SUMS.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
out="$here/../static/vendor"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
cp "$here/package.json" "$here/package-lock.json" "$here/entry.js" "$here/entry-qr.js" "$here/entry-idb.js" "$here/entry-sse.js" "$work/"
cd "$work"
npm ci --ignore-scripts --no-audit --no-fund >/dev/null
mkdir -p "$out"
for pair in entry.js:age.mjs entry-qr.js:qr.mjs entry-idb.js:idb.mjs entry-sse.js:sse.mjs; do
	./node_modules/.bin/esbuild "${pair%%:*}" --bundle --format=esm --platform=browser --target=es2022 \
		--legal-comments=inline --log-level=warning --outfile="$out/${pair#*:}"
done
# The license of every bundled package, from the installed files.
node -e '
const fs = require("fs"), path = require("path");
const lock = JSON.parse(fs.readFileSync("package-lock.json", "utf8"));
let text = "Licenses of the packages bundled into static/vendor/age.mjs and qr.mjs (package-lock.json).\n";
for (const [dir, p] of Object.entries(lock.packages)) {
  if (!dir || p.dev) continue;
  const name = dir.slice(dir.lastIndexOf("node_modules/") + "node_modules/".length);
  const lic = fs.readdirSync(dir).find((f) => /^licen[cs]e/i.test(f));
  text += "\n==== " + name + " " + p.version + " (" + p.license + ")\n\n" + fs.readFileSync(path.join(dir, lic), "utf8").trim() + "\n";
}
fs.writeFileSync(process.argv[1], text);
' "$here/LICENSES.txt"
cd "$out" && sha256sum age.mjs qr.mjs idb.mjs sse.mjs > "$here/SHA256SUMS"
