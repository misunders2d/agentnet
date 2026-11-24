import { Lexer, type Token, type Tokens } from "marked";
import type { Span } from "./Composer.mentions";
import { spoilerRanges } from "../spoilers.mjs";

export const formats = [
  { id: "bold", label: "Bold", key: "KeyB", shortcut: "B", marker: "**" },
  { id: "italic", label: "Italic", key: "KeyI", shortcut: "I", marker: "*" },
  { id: "underline", label: "Underline", key: "KeyU", shortcut: "U", marker: "^^" },
  { id: "strike", label: "Strikethrough", key: "KeyX", shortcut: "Shift+X", marker: "~~" },
  { id: "code", label: "Monospace", key: "KeyM", shortcut: "Shift+M", browserShortcut: "M", marker: "`" },
  { id: "quote", label: "Quote", key: "Period", shortcut: "Shift+.", marker: "" },
  { id: "spoiler", label: "Hidden text", key: "KeyP", shortcut: "Shift+P", browserShortcut: "P", marker: "||" },
  { id: "link", label: "Link", key: "KeyK", shortcut: "K", marker: "" },
  { id: "clear", label: "Clear formatting", key: "KeyN", shortcut: "Shift+N", browserShortcut: "N", marker: "" },
] as const;

export type Format = typeof formats[number]["id"];
export type Selection = { from: number; to: number; direction: "backward" | "forward" | "none" };
type Change = { from: number; to: number; insert: string };

export function fieldSelection(element: HTMLTextAreaElement): Selection | null {
  const { selectionStart: from, selectionEnd: to, selectionDirection: direction } = element;
  return from < to ? { from, to, direction } : null;
}

function apply(text: string, spans: Span[], selection: Selection, changes: Change[]) {
  let value = "", cursor = 0;
  for (const change of changes) {
    value += text.slice(cursor, change.from) + change.insert;
    cursor = change.to;
  }
  value += text.slice(cursor);
  const offset = (at: number, includeInsertion: boolean) => {
    let delta = 0;
    for (const change of changes) {
      if (at < change.from || at === change.from && change.to === at && !includeInsertion) break;
      if (at < change.to) return change.from + delta + change.insert.length;
      delta += change.insert.length - (change.to - change.from);
    }
    return at + delta;
  };
  const refs = spans.map(span => ({ ...span, start: offset(span.start, true) }));
  return { value, refs, selection: { ...selection, from: offset(selection.from, true), to: offset(selection.to, false) } };
}

const remove = (from: number, to: number): Change => ({ from, to, insert: "" });
const insert = (at: number, value: string): Change => ({ from: at, to: at, insert: value });

// Remove recognized GFM wrappers. Track the original offsets so exact
// mention spans move around the punctuation without changing identity.
function clear(text: string, from: number, to: number): Change[] {
  const changes: Change[] = [];
  const selected = text.slice(from, to);
  const lexer = new Lexer({ gfm: true, breaks: true });
  function scan(tokens: Token[], source: string, offset: number) {
    let cursor = 0;
    for (const token of tokens as Tokens.Generic[]) {
      const index = source.indexOf(token.raw, cursor);
      if (index < 0) continue;
      cursor = index + token.raw.length;
      const at = offset + index, end = offset + cursor;
      switch (token.type) {
        case "strong": case "em": case "del": case "link": {
          if (token.type === "link" && !token.raw.startsWith("[")) break;
          // Search after the opening marker, since the first glyph of a
          // nested style may match the enclosing marker.
          const marker = token.type === "strong" ? 2 : 1;
          const start = token.raw.indexOf(token.text, marker);
          if (start < 0 || !token.text) break;
          changes.push(remove(at, at + start), remove(at + start + token.text.length, end));
          if (token.tokens) scan(token.tokens, token.text, at + start);
          break;
        }
        case "codespan": {
          const ticks = token.raw.match(/^`+/)![0].length;
          const content = token.raw.slice(ticks, -ticks);
          const padding = content.startsWith(" ") && content.endsWith(" ") && !!content.trim() ? 1 : 0;
          changes.push(remove(at, at + ticks + padding), remove(end - ticks - padding, end));
          break;
        }
        case "blockquote":
          for (const line of token.raw.matchAll(/^ {0,3}(?:>[ 	]?)+.*$/gm)) {
            const prefix = /^ {0,3}(?:>[ 	]?)+/.exec(line[0])![0].length;
            const rest = line[0].slice(prefix), lineAt = at + line.index;
            changes.push(remove(lineAt, lineAt + prefix));
            scan(lexer.inlineTokens(rest), rest, lineAt + prefix);
          }
          break;
        default:
          if (token.tokens) scan(token.tokens, token.raw, at);
          break;
      }
    }
  }
  scan(lexer.lex(selected), selected, from);
  for (const match of selected.matchAll(/(?<![\\^])\^\^([^\s^][^\n^]*?)\^\^(?!\^)/g)) {
    const at = from + match.index, end = at + match[0].length;
    changes.push(remove(at, at + 2), remove(end - 2, end));
  }
  for (const {start, end} of spoilerRanges(selected)) {
    changes.push(remove(from + start, from + start + 2), remove(from + end - 2, from + end));
  }
  // An explicit link's destination can itself look like a delimiter. Its
  // removal subsumes any shorter range the inline scan did not visit.
  const ordered = changes.sort((a, b) => a.from - b.from || b.to - a.to);
  const disjoint: Change[] = [];
  for (const change of ordered) {
    if (change.from >= (disjoint.at(-1)?.to ?? from) && change.to > change.from) disjoint.push(change);
  }
  return disjoint;
}

export function formatText(text: string, refs: Span[], range: Selection, style: Format, url = "") {
  let {from, to} = range;
  const inside = text.slice(from, to);
  if (!inside.trim() || refs.some(ref => [from, to].some(at => ref.start < at && at < ref.start + ref.name.length + 1))) return null;
  let changes: Change[] = [];
  if (style === "quote") {
    from = from ? text.lastIndexOf("\n", from - 1) + 1 : 0;
    if (text[to - 1] === "\n") to--;
    const end = text.indexOf("\n", to);
    to = end < 0 ? text.length : end;
    const lines = [...text.slice(from, to).matchAll(/^.*$/gm)];
    const unquote = lines.every(line => /^>[ \t]?/.test(line[0]));
    changes = lines.map(line => {
      const at = from + line.index;
      return unquote ? remove(at, at + line[0].match(/^>[ \t]?/)![0].length) : insert(at, "> ");
    });
  } else if (style === "clear") {
    // Keep selection after applying a style, so Clear can remove its wrappers
    // without requiring the person to select the newly inserted punctuation.
    // Adjacent runs can mix styles in any order, including Markdown
    // written before the formatting menu existed.
    const boundary = text.slice(0, from).match(/[`*_~^|]+$/)?.[0] || "";
    for (let at = boundary.length; at > 0;) {
      const marker = formats.find(format => format.marker && boundary.slice(0, at).endsWith(format.marker) && text.startsWith(format.marker, to))?.marker
        || (boundary[at - 1] === "_" && text[to] === "_" ? "_" : "");
      if (!marker) break;
      at -= marker.length;
      from -= marker.length;
      to += marker.length;
    }
    const linkSuffix = text.slice(to).match(/^\]\((?:<[^<>\s]+>|[^\s)]+)\)/);
    if (from > 0 && text[from - 1] === "[" && linkSuffix) { from--; to += linkSuffix[0].length; }
    const lineStart = from ? text.lastIndexOf("\n", from - 1) + 1 : 0;
    if (/^ {0,3}>[ \t]?$/.test(text.slice(lineStart, from))) from = lineStart;
    changes = clear(text, from, to).filter(change => !refs.some(
      ref => change.from < ref.start + ref.name.length + 1 && change.to > ref.start,
    ));
  } else if (style === "link") {
    if (/^https?:\/\/|^mailto:/i.test(url) && !/[<>\s]/.test(url) && !refs.some(ref => ref.start >= from && ref.start < to) && !inside.includes("\n")) {
      changes = [insert(from, "["), insert(to, "](<" + url + ">)")];
    }
  } else {
    from += inside.length - inside.trimStart().length;
    to -= inside.length - inside.trimEnd().length;
    const marker = formats.find(format => format.id === style)!.marker;
    const left = text.slice(from - marker.length, from);
    if (left === marker && text.startsWith(marker, to) && !(style === "italic" && (text[from - 2] === "*" || text[to + 1] === "*"))) {
      changes = [remove(from - marker.length, from), remove(to, to + marker.length)];
    } else if (style === "code") {
      if (refs.some(ref => ref.start >= from && ref.start < to)) return null;
      let size = 1;
      for (const run of inside.matchAll(/`+/g)) size = Math.max(size, run[0].length + 1);
      const fence = "`".repeat(size);
      const pad = /^`|`$/.test(inside.trim()) ? " " : "";
      changes = [insert(from, fence + pad), insert(to, pad + fence)];
    } else {
      if (style === "spoiler") {
        changes = [insert(from, marker), insert(to, marker)];
      } else {
        const body = text.slice(from, to);
        for (const match of body.matchAll(style === "underline" ? /[^\n]+/g : /(?:[^\n]|\n(?![ \t]*\n))+/g)) {
          const chunk = match[0], trimmed = chunk.trim();
          if (!trimmed) continue;
          const start = from + match.index + chunk.length - chunk.trimStart().length;
          changes.push(insert(start, marker), insert(start + trimmed.length, marker));
        }
      }
      if (style === "spoiler") {
        const candidate = apply(text, refs, range, changes);
        const covered = spoilerRanges(candidate.value).some(secret => secret.start <= from && secret.end >= to + 4);
        if (!covered) return null;
      }
    }
  }
  return changes.length ? apply(text, refs, range, changes) : null;
}
