// Completes the Comic skin package in OUT (build.sh, dev.sh): style.css,
// document.css and skin.json, from Tailwind's output and src/document.css.
//
//   node package.mjs TAILWIND_CSS DOCUMENT_CSS OUT
//
// A skin's stylesheet applies inside its shadow tree, where browsers ignore
// @font-face and @property. Those rules go to document.css, which the host
// adopts at document level (docs/UI_SKINS.md, "Document rules"): the
// fonts' faces (src/document.css) and Tailwind's @property registrations,
// moved out of the compiled stylesheet. Everything else stays in style.css.
// The manifest lists every file in OUT, in a fixed order.
import fs from "node:fs";
import path from "node:path";

const [tailwind, documentCSS, out] = process.argv.slice(2);
if (!tailwind || !documentCSS || !out) { console.error("usage: package.mjs TAILWIND_CSS DOCUMENT_CSS OUT"); process.exit(2); }
const css = fs.readFileSync(tailwind, "utf8");
const property = /@property\s+--[\w-]+\s*\{[^{}]*\}/g;
const registered = css.match(property) || [];
const style = css.replace(property, "");
if (/@font-face|@property/.test(style)) throw new Error("style.css still has document-level rules: move them to src/document.css");
fs.writeFileSync(path.join(out, "style.css"), style);
fs.writeFileSync(path.join(out, "document.css"), fs.readFileSync(documentCSS, "utf8").trim() + "\n" + registered.join("\n") + "\n");

const files = [];
const walk = (dir) => {
  for (const name of fs.readdirSync(path.join(out, dir)).sort()) {
    const rel = dir ? dir + "/" + name : name;
    if (fs.statSync(path.join(out, rel)).isDirectory()) walk(rel);
    else if (rel !== "skin.json") files.push(rel);
  }
};
walk("");
const first = ["entry.mjs", "style.css", "document.css"];
files.sort((a, b) => (first.includes(a) ? first.indexOf(a) : 9) - (first.includes(b) ? first.indexOf(b) : 9) || (a < b ? -1 : a > b ? 1 : 0));
const manifest = { api: 1, id: "comic", name: "Comic", entry: "entry.mjs", style: "style.css", document: "document.css", files };
fs.writeFileSync(path.join(out, "skin.json"), JSON.stringify(manifest, null, 2) + "\n");
