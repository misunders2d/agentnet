import { Lexer, type Token, type Tokens, type TokenizerThis, type MarkedOptions } from "marked";
import { hiddenText, maskSpoilers, spoilerRanges } from "./spoilers.mjs";
export { fieldSelection, formats, formatText } from "./features/TextFormatting.model";
export { hiddenText, maskSpoilers, spoilerRanges, replaceSpoilers } from "./spoilers.mjs";
export { localRunningDetail } from "./runningDetail";
export { requestResultText } from "./requestResult";

const entities: Record<string,string> = {amp:"&",lt:"<",gt:">",quot:'"',apos:"'",nbsp:" "};
export const markdownText = (s: string): string => s.replace(/&(#x[0-9a-f]{1,6}|#[0-9]{1,7}|[a-z]{2,6});/gi, (all, ref: string) => {
  if (ref[0] !== "#") return entities[ref.toLowerCase()] ?? all;
  const n = /^#x/i.test(ref) ? parseInt(ref.slice(2),16) : parseInt(ref.slice(1),10);
  return n > 0 && n <= 0x10ffff ? String.fromCodePoint(n) : all;
});
type SpoilerMode = "mask" | "reveal" | "literal";
type Extensions = NonNullable<MarkedOptions["extensions"]>;

function underline(this: TokenizerThis, source: string): Tokens.Generic | undefined {
  const pair = /^\^\^(?!\^)([^\s^][^\n^]*?)\^\^(?!\^)/.exec(source);
  if (pair && !/\s$/.test(pair[1])) return {type: "underline", raw: pair[0], text: pair[1], tokens: this.lexer.inlineTokens(pair[1])};
}

const markdownOptions = { gfm: true, breaks: true };
const underlineExtensions: Extensions = {
  renderers: {}, childTokens: {}, inline: [underline], startInline: [source => source.indexOf("^^")],
};

export function markdownInlineTokens(text: string): Token[] {
  return new Lexer(text.includes("^^") ? { ...markdownOptions, extensions: underlineExtensions } : markdownOptions).inlineTokens(text);
}

export function markdownTokens(text: string, spoilers: SpoilerMode = "mask"): Token[] {
  if (!text.includes("||") || spoilers === "literal") {
    return new Lexer(text.includes("^^") ? { ...markdownOptions, extensions: underlineExtensions } : markdownOptions).lex(text);
  }
  if (spoilers === "mask") return markdownTokens(maskSpoilers(text), "literal");
  const ranges = spoilerRanges(text);
  if (!ranges.length) return markdownTokens(text, "literal");

  // Shield spans before block lexing. GFM otherwise splits on blank lines,
  // table pipes and quote markers before an inline tokenizer can see the pair.
  const occupied = new Set(Array.from(text.matchAll(/\uE000AN(\d+):/g), match => match[1]));
  let nonce = 0;
  while (occupied.has(String(nonce))) nonce++;
  const prefix = "\uE000AN" + nonce + ":";
  const seen = new Set<number>();
  const underlined = text.includes("^^");
  const extensions: Extensions = {
    renderers: {}, childTokens: {},
    inline: [source => {
      if (!source.startsWith(prefix)) return;
      const match = /^(\d+)\uE001/.exec(source.slice(prefix.length));
      if (!match) return;
      const index = Number(match[1]);
      const range = ranges[index];
      if (!range) return;
      seen.add(index);
      return {type: "spoiler", raw: prefix + match[0], text: text.slice(range.start + 2, range.end - 2)};
    }, ...(underlined ? underlineExtensions.inline! : [])],
    startInline: [source => source.indexOf(prefix), ...(underlined ? underlineExtensions.startInline! : [])],
  };
  function shield(onlySeen = false) {
    let source = "", at = 0;
    for (const [index, range] of ranges.entries()) {
      if (onlySeen && !seen.has(index)) continue;
      source += text.slice(at, range.start) + prefix + index + "\uE001";
      at = range.end;
    }
    return source + text.slice(at);
  }
  const lex = (source: string) => new Lexer({ ...markdownOptions, extensions }).lex(source);
  const tokens = lex(shield());
  if (seen.size === ranges.length) return tokens;
  // GFM can consume a marker as literal code, HTML or a link destination.
  // Keep those ranges in the original source so its indentation and URLs survive.
  return seen.size ? lex(shield(true)) : markdownTokens(text, "literal");
}

// Marked keeps certain constructs (such as HTML and link destinations) as a
// single token. If they swallowed a placeholder, keep its replacement inert.
const unresolvedSpoiler = /\uE000AN\d+:\d+\uE001/g;
export const spoilerFallback = (text: string) => text.includes("\uE000AN") ? text.replace(unresolvedSpoiler, hiddenText) : text;
export const hasSpoilerPlaceholder = (text: string) => spoilerFallback(text) !== text;

// Only previews use this projection. Literal code/escapes and raw HTML remain
// text; the message's signed body, copy target and user-chosen title stay intact.
export function previewText(text: string): string {
  if (!text.includes("^^") && !text.includes("||") && !/[\\`*_~[\]<>!&#\n]|^\s*(?:[-+>]\s|\d+[.)]\s)/.test(text)) return text;
  const read = (tokens: Token[]): string => tokens.map(token => {
    const t = token as Tokens.Generic;
    if (t.type === "space" || t.type === "br") return "\n";
    if (t.type === "def" || t.type === "hr") return "";
    if (t.type === "list") return (t as Tokens.List).items.map(it => read(it.tokens)).join("\n") + "\n";
    if (t.type === "table") {
      const table=t as Tokens.Table;
      return [table.header,...table.rows].map(row=>row.map(c=>read(c.tokens)).join(" · ")).join("\n")+"\n";
    }
    const value=t.tokens ? read(t.tokens) : t.type === "text" ? markdownText(t.text) : t.text ?? t.raw;
    return value + (["paragraph","heading","blockquote","code"].includes(t.type) ? "\n" : "");
  }).join("");
  return read(markdownTokens(text));
}

// Presentation only. The original message and href remain the copy/open targets.
export function shortLinkLabel(label: string, href: string): string {
  if (label.length <= 64 || !/^https?:\/\//i.test(href)) return label;
  if (label !== href && `https://${label}` !== href && `http://${label}` !== href) return label;
  try {
    const url = new URL(href);
    // Include userinfo when present: never conceal a misleading authority.
    const authority = href.slice(href.indexOf("://") + 3).split(/[/?#]/, 1)[0];
    if (!url.hostname || authority.length > 60) return label;
    return authority + "/…";
  } catch { return label; }
}

export type LinkPart = string | { href: string; label: string };

// Canonical portable navigation anchors; the host still validates local access.
export const portableLink = (value: string): boolean => /^agentnet:\/\/open#realm=[0-9a-f]{32}&(?:conv=[0-9a-f]{64}&topic=(?:main|[0-9a-f]{32})|thread=[0-9a-f]{32})$/.test(value);

// Plain-text skins (the Classic and Zoom packages in agentnet-skins) keep
// their formatting and take this as skins/shared/link-text.mjs (build.sh).
// Use the same Markdown lexer as Comic to recognize raw links without
// touching code or descriptive links.
export function rawLinkParts(text: string): LinkPart[] {
  if (!text.includes("agentnet://open#") && (text.length <= 64 || !/https?:\/\/|www\./i.test(text))) return [text];
  const links: { start: number; end: number; href: string; label: string }[] = [];
  function scan(source: string, tokens: Token[], offset: number) {
    let cursor = 0;
    for (const token of tokens) {
      const at = source.indexOf(token.raw, cursor);
      if (at < 0) return; // A normalized construct stays plain text.
      cursor = at + token.raw.length;
      const t = token as Tokens.Generic;
      if (t.type === "link") {
        const label = shortLinkLabel(t.text, t.href);
        if (label !== t.text || portableLink(t.href)) links.push({ start: offset + at, end: offset + cursor, href: t.href, label: portableLink(t.href) ? markdownText(label) : label });
      } else if (!["code", "codespan", "html", "image"].includes(t.type)) {
        if (t.tokens) scan(t.raw, t.tokens, offset + at);
        else if (t.type === "list") scan(t.raw, (t as Tokens.List).items, offset + at);
        else if (t.type === "table") {
          const table = t as Tokens.Table;
          scan(t.raw, [table.header, ...table.rows].flatMap(row => row.flatMap(cell => cell.tokens)), offset + at);
        }
      }
    }
  }
  scan(text, new Lexer({ gfm: true }).lex(text), 0);
  const parts: LinkPart[] = [];
  let cursor = 0;
  for (const link of links) {
    if (link.start < cursor) continue;
    parts.push(text.slice(cursor, link.start), { href: link.href, label: link.label });
    cursor = link.end;
  }
  parts.push(text.slice(cursor));
  return parts;
}
