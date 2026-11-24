import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { maskSpoilers, previewText, markdownTokens, formatText } from "../skins/shared/link-text.mjs";
import { maskSpoilers as browserMask } from "../static/spoilers.mjs";

const paths = ["../static/spoilers.mjs", "../web/src/spoilers.mjs"];
const [served, source] = await Promise.all(paths.map(path => readFile(new URL(path, import.meta.url))));
assert.deepEqual(served, source, "browser engine and Comic use the same projection source");

for (const [text, expected] of [
  ["ordinary __old bold__", "ordinary __old bold__"],
  ["before ||first\n\nsecond|| after", "before [Hidden text] after"],
  ["**before ||_nested_ and `||code||`|| after**", "**before [Hidden text] after**"],
  ["escaped \\||literal\\|| then ||private||", "escaped \\||literal\\|| then [Hidden text]"],
  ["`||code||` then ||private||", "`||code||` then [Hidden text]"],
  ["before\n~~~sh\n||sample||\n~~~\n||secret||", "before\n~~~sh\n||sample||\n~~~\n[Hidden text]"],
  ["||a|| ||b|| |||literal||| ||unfinished", "[Hidden text] [Hidden text] |||literal||| ||unfinished"],
]) {
  assert.equal(browserMask(text), expected);
  assert.equal(maskSpoilers(text), expected);
}
assert.equal(previewText("**visible** ||hide\nme||").trim(), "visible [Hidden text]");
const [block] = markdownTokens("**||**nested**\n\nsecret||** and ^^underlined^^", "reveal");
assert.equal(block.tokens[0].type, "strong");
assert.equal(block.tokens[0].tokens[0].type, "spoiler");
assert.equal(block.tokens[0].tokens[0].text, "**nested**\n\nsecret");
assert.equal(block.tokens[2].type, "underline");
const entire = text => ({from: 0, to: text.length, direction: "forward"});
const apply = (text, style, range = entire(text), spans = [], url) => formatText(text, spans, range, style, url);
assert.equal(apply("**outer *inner*** and a_b_c", "clear").value, "outer inner and a_b_c");
assert.equal(apply("old __bold__", "clear", {from: 6, to: 10, direction: "forward"}).value, "old bold");
assert.equal(apply("a\n\nb", "bold").value, "**a**\n\n**b**");
assert.equal(apply("a\nb", "underline").value, "^^a^^\n^^b^^");
assert.equal(apply("a\nb", "quote").value, "> a\n> b");
assert.equal(apply("||already hidden||", "spoiler"), null, "invalid nested same delimiters do not silently send visible text");
const mention = {start: 0, name: "Kit", kind: "agent", id: "agent-1"};
const mentioned = apply("@Kit text", "bold", entire("@Kit text"), [mention]);
assert.deepEqual(mentioned.refs, [{...mention, start: 2}]);
assert.equal(apply("@Kit text", "bold", {from: 1, to: 9, direction: "forward"}, [mention]), null);
assert.equal(apply("link", "link", entire("link"), [], "https://example.test/a(b)").value, "[link](<https://example.test/a(b)>)");
console.log("Go-mirrored spoiler projections, GFM nesting and formatting transformations pass");
