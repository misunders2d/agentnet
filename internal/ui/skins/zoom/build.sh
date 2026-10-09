#!/bin/sh
set -eu
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
out=${1:-"$here/../../static/skins/zoom"}
mkdir -p "$out"
# JSON is also a JavaScript object literal; no parser/build dependency needed.
{ printf "export default "; cat "$here/skin.json"; printf ";\n"; } > "$here/src/manifest.mjs"
cp "$here/skin.json" "$out/skin.json"
cp "$here/../shared/reaction-preferences.mjs" "$out/reaction-preferences.mjs"
cp "$here/../shared/link-text.mjs" "$out/link-text.mjs"
cp "$here/../shared/topics.mjs" "$out/topics.mjs"
cp "$here/../shared/person-topics.mjs" "$out/person-topics.mjs"
cp "$here/../../static/pictures.mjs" "$out/pictures.mjs"
cp "$here/../../static/optimistic.mjs" "$out/optimistic.mjs"
for name in entry.mjs manifest.mjs style.css template.mjs typing.mjs drivespace.mjs drivespace.css drivespace-setup.mjs assistant-setup.mjs assistant-setup.css qr.mjs icon.png; do
  cp "$here/src/$name" "$out/$name"
done
(cd "$out" && sha256sum skin.json reaction-preferences.mjs link-text.mjs topics.mjs person-topics.mjs optimistic.mjs pictures.mjs entry.mjs manifest.mjs style.css template.mjs typing.mjs drivespace.mjs drivespace.css drivespace-setup.mjs assistant-setup.mjs assistant-setup.css qr.mjs icon.png) > "$here/SHA256SUMS"
