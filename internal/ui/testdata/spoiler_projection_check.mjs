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
  ["```\r\n||sample||\r\n``` \t\r\n||secret||", "```\r\n||sample||\r\n``` \t\r\n[Hidden text]"],
  ...["\uFEFF", "\u0085", "\u2028"].map(whitespace => {
    const fenced = "```\n||sample||\n```" + whitespace + "\n||also code||\n```\n";
    return [fenced + "||secret||", fenced + "[Hidden text]"];
  }),
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
const code = "test -f a || exit 1\n\ntest -f b || exit 2";
const nestedCode = "1. step\n   - sub\n     ```sh\n     " + code.replaceAll("\n", "\n     ") + "\n     ```";
const [nested, , afterCode] = markdownTokens(nestedCode + "\n\n||secret||", "reveal");
assert.equal(nested.items[0].tokens[1].items[0].tokens[1].text, code);
assert.equal(afterCode.tokens[0].type, "spoiler");
const [indentedCode] = markdownTokens("    " + code.replaceAll("\n", "\n    "), "reveal");
assert.equal(indentedCode.text, code);
const [links] = markdownTokens("[check](https://example.test/a||b||c) <https://example.test/a||b||c> ||secret||", "reveal");
assert.deepEqual(links.tokens.filter(token => token.type === "link").map(token => token.href),
  ["https://example.test/a||b||c", "https://example.test/a||b||c"]);
assert.equal(links.tokens.at(-1).type, "spoiler");
const [html] = markdownTokens("<div>Here ||example||</div>", "reveal");
assert.equal(html.text, "<div>Here ||example||</div>");
for (const malformed of [
  "[a](http://e.test/||secret words||)",
  "<https://e.test/||secret words||> ||real||",
  "[a](https://e.test/a||b||c) <https://e.test/x||secret words||> ||real||",
  "<div>\n||a\n\n    secret words||\n\n    X",
  "[a](<https://e.test/||secret words||>)",
  "[a](https://e.test/ '||secret words||')",
  "    literal ||\nplain secret words||",
]) {
  const tokens = markdownTokens(malformed, "reveal");
  assert(!JSON.stringify(tokens).includes("secret words"), "literal fallback exposed a spoiler in " + malformed);
  if (malformed.endsWith("||real||")) assert.equal(tokens[0].tokens.at(-1).type, "spoiler");
}
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
