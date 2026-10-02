#!/bin/sh
# Same frozen-graph vendor build model as internal/ui/webvendor/build.sh.
# Build-time Node/npm/network only; NEVER run this from runtime hook install.
# Explicit output dir keeps generated bundle/license/checksums private pending
# root review/publication approval. Source adapter exposes no MCP tools.
set -eu
umask 077
[ "$#" -eq 1 ] || { echo 'usage: build.sh APPROVED_OUTPUT_DIR' >&2; exit 2; }
here=$(cd "$(dirname "$0")" && pwd)
mkdir -p "$1"
out=$(cd "$1" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
cp "$here/package.json" "$here/package-lock.json" "$here/agentnet.mjs" "$work/"
cd "$work"
# Build profile is explicitly unauthenticated public npm, with no inherited
# credentials, user/global config or install scripts. Not an account operation.
# npm refuses the same path for both config levels, including /dev/null.
: > "$work/empty-user.npmrc"
: > "$work/empty-global.npmrc"
env -i PATH="$PATH" HOME="$work" npm ci --ignore-scripts --no-audit --no-fund \
  --registry=https://registry.npmjs.org --userconfig="$work/empty-user.npmrc" \
  --globalconfig="$work/empty-global.npmrc" >/dev/null
./node_modules/.bin/esbuild agentnet.mjs --bundle --format=esm --platform=node --target=node22 \
  --banner:js='import { createRequire as agentnetCreateRequire } from "node:module"; const require = agentnetCreateRequire(import.meta.url);' \
  --legal-comments=inline --log-level=warning --metafile=meta.json --outfile="$out/agentnet.bundle.mjs"
# Bundled ESM can include SDK dependencies with CJS Node-builtin require; the
# banner retains builtin resolution without external SDK packages at runtime.
node --input-type=module - "$out" <<'JS'
import fs from 'node:fs';
import path from 'node:path';
const inputs = JSON.parse(fs.readFileSync('meta.json', 'utf8')).inputs;
const packages = new Map();
for (const input of Object.keys(inputs)) {
  if (!input.startsWith('node_modules/')) continue;
  let dir = path.dirname(input), p;
  for (;;) {
    const file = path.join(dir, 'package.json');
    if (fs.existsSync(file)) {
      p = JSON.parse(fs.readFileSync(file, 'utf8'));
      if (p.name && p.version) break;
    }
    const parent = path.dirname(dir);
    if (parent === dir || dir === '.') throw new Error('unowned bundled dependency');
    dir = parent;
  }
  packages.set(dir, p);
}
let licenses = 'Licenses of packages actually bundled from the frozen Claude channel graph.\n';
for (const [dir, p] of [...packages].sort(([a], [b]) => a.localeCompare(b))) {
  const files = fs.readdirSync(dir).filter((name) => /^licen[cs]e|^notice/i.test(name) && fs.statSync(path.join(dir, name)).isFile()).sort();
  if (!files.length) throw new Error('bundled dependency license absent: ' + p.name);
  licenses += '\n==== ' + p.name + ' ' + p.version + ' (' + p.license + ')\n';
  for (const name of files) licenses += '\n' + name + '\n' + fs.readFileSync(path.join(dir, name), 'utf8').trim() + '\n';
}
fs.writeFileSync(path.join(process.argv[2], 'LICENSES.txt'), licenses);
JS
cd "$out"
sha256sum agentnet.bundle.mjs LICENSES.txt > SHA256SUMS
