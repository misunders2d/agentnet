#!/bin/sh
set -eu
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
out=${1:-"$here/../../static/skins/classic"}
mkdir -p "$out"
cp "$here/skin.json" "$out/skin.json"
for name in entry.mjs style.css template.mjs typing.mjs drivespace.mjs drivespace.css drivespace-setup.mjs assistant-setup.mjs assistant-setup.css qr.mjs; do
  cp "$here/src/$name" "$out/$name"
done
(cd "$out" && sha256sum skin.json entry.mjs style.css template.mjs typing.mjs drivespace.mjs drivespace.css drivespace-setup.mjs assistant-setup.mjs assistant-setup.css qr.mjs) > "$here/SHA256SUMS"
