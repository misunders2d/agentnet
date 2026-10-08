import { Lexer, type Token, type Tokens } from "marked";

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

// Classic/Zoom keep their plain-text formatting. Use the same Markdown lexer
// as Comic to recognize raw links without touching code or descriptive links.
export function rawLinkParts(text: string): LinkPart[] {
  if (text.length <= 64 || !/https?:\/\/|www\./i.test(text)) return [text];
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
        if (label !== t.text) links.push({ start: offset + at, end: offset + cursor, href: t.href, label });
      } else if (!["code", "codespan", "html", "image"].includes(t.type)) {
        if (t.tokens) scan(t.raw, t.tokens, offset + at);
        else if (t.type === "list") scan(t.raw, (t as Tokens.List).items, offset + at);
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
