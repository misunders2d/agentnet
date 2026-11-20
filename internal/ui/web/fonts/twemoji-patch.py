# Makes fonts/twemoji.woff2 from twemoji-colr-font 15.0.3 (npm; "Twemoji
# Mozilla" 0.7.0 build of Twemoji 15.0, OFL-1.1 font, CC-BY-4.0 art):
#
#   npm pack twemoji-colr-font@15.0.3   # integrity sha512-UsK4JUpaczeVoMGeYMnKaMTxKt7fujg1nQOk4NaC0teZmdOo+uAai0DIuaSzsMkShtG4J75F0OEsVdGJ+Q1pHQ==
#   tar xzf twemoji-colr-font-15.0.3.tgz
#   python3 -m venv v && v/bin/pip install fonttools==4.68.0 brotli==1.2.1
#   v/bin/python twemoji-patch.py package/twemoji.woff2 twemoji.woff2
#
# Why: Chromium (desktop, Android WebView, WebView2) drops U+FE0F before
# shaping, and Twemoji keys sequences such as 🏳️‍🌈, ❤️‍🔥 and 😶‍🌫️ with it,
# so they showed as two emoji. For every ligature that contains FE0F this
# adds the same ligature without it (Noto Color Emoji's sequences are keyed
# that way). It also renames the font, as a modified OFL font should be.
import sys
from fontTools.ttLib import TTFont
from fontTools.ttLib.tables.otTables import Ligature

src, out = sys.argv[1], sys.argv[2]
font = TTFont(src)
fe0f = font.getBestCmap()[0xFE0F]
added = 0
for lookup in font["GSUB"].table.LookupList.Lookup:
    for sub in lookup.SubTable:
        ligatures = getattr(sub, "ligatures", None)
        if not ligatures:
            continue
        for first in list(ligatures):
            have = {tuple(l.Component) for l in ligatures[first]}
            for lig in list(ligatures[first]):
                if fe0f not in lig.Component:
                    continue
                comp = [c for c in lig.Component if c != fe0f]
                if not comp or tuple(comp) in have:
                    continue
                new = Ligature()
                new.Component, new.CompCount, new.LigGlyph = comp, len(comp) + 1, lig.LigGlyph
                ligatures[first].append(new)
                have.add(tuple(comp))
                added += 1
            ligatures[first].sort(key=lambda l: -len(l.Component))  # longest match first
for rec in font["name"].names:
    if rec.nameID in (1, 3, 4, 6):
        rec.string = {1: "AgentNet Twemoji", 3: "AgentNet Twemoji 15.0.3", 4: "AgentNet Twemoji", 6: "AgentNetTwemoji"}[rec.nameID]
font.flavor = "woff2"
font.save(out)
print("added", added, "ligatures without FE0F")
