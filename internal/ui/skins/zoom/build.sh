#!/bin/sh
set -eu
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
out=${1:-"$here/../../static/skins/zoom"}
mkdir -p "$out"
# JSON is also a JavaScript object literal; no parser/build dependency needed.
{ printf "export default "; cat "$here/skin.json"; printf ";\n"; } > "$here/src/manifest.mjs"
cp "$here/skin.json" "$out/skin.json"
cp "$here/../shared/topics.mjs" "$out/topics.mjs"
cp "$here/../../static/optimistic.mjs" "$out/optimistic.mjs"
for name in entry.mjs manifest.mjs style.css template.mjs typing.mjs drivespace.mjs drivespace.css drivespace-setup.mjs assistant-setup.mjs assistant-setup.css qr.mjs icon.png; do
  cp "$here/src/$name" "$out/$name"
done
(cd "$out" && sha256sum skin.json topics.mjs optimistic.mjs entry.mjs manifest.mjs style.css template.mjs typing.mjs drivespace.mjs drivespace.css drivespace-setup.mjs assistant-setup.mjs assistant-setup.css qr.mjs icon.png) > "$here/SHA256SUMS"
