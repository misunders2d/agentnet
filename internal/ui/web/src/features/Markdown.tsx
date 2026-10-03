// Markdown: a message's text as React elements, built from marked's lexer.
// Nothing becomes an HTML string: raw HTML in a message reads as the text
// it is, pictures stay links, and only web and mail links are live (they
// open in a new tab). Mentions ([@Name](agentnet:person/ID)) are handed to
// the caller, who knows who is in the conversation.
import { Lexer, type Token, type Tokens } from "marked";
import { Fragment, useMemo, type ReactNode } from "react";

export type MentionKind = "person" | "guest" | "agent";
export type RenderMention = (kind: MentionKind, id: string, name: string) => ReactNode;

const mentionHref = /^agentnet:(person|guest|agent)\/([A-Za-z0-9_-]{1,64})$/;
const safeHref = /^(https?:\/\/|mailto:)/i;

const entities: Record<string, string> = { amp: "&", lt: "<", gt: ">", quot: "\"", apos: "'", nbsp: " " };
// The lexer leaves character references as typed; read the common ones.
const decode = (s: string) => s.replace(/&(#x[0-9a-f]{1,6}|#[0-9]{1,7}|[a-z]{2,6});/gi, (all, ref: string) => {
  if (ref[0] !== "#") return entities[ref.toLowerCase()] ?? all;
  const n = ref[1] === "x" || ref[1] === "X" ? parseInt(ref.slice(2), 16) : parseInt(ref.slice(1), 10);
  return n > 0 && n <= 0x10ffff ? String.fromCodePoint(n) : all;
});

/** Markdown renders text; tail (a message's time) sits at the end of its last line when that line is text. */
export function Markdown({ text, mention, tail, className = "" }: { text: string; mention?: RenderMention; tail?: ReactNode; className?: string }) {
  const tokens = useMemo(() => new Lexer({ gfm: true, breaks: true }).lex(text).filter((t) => t.type !== "space" && t.type !== "def"), [text]);
  const end = tokens[tokens.length - 1];
  const inlineEnd = !!end && ["paragraph", "text", "heading", "html"].includes(end.type);
  return (
    <div className={"min-w-0 break-words [overflow-wrap:anywhere] [&>*+*]:mt-2 " + className}>
      {tokens.map((t, i) => <Fragment key={i}>{block(t as Tokens.Generic, mention, inlineEnd && i === tokens.length - 1 ? tail : undefined)}</Fragment>)}
      {tail && !inlineEnd && <div className="flow-root">{tail}</div>}
    </div>
  );
}

function blocks(tokens: Token[], mention?: RenderMention): ReactNode[] {
  return tokens.map((t, i) => <Fragment key={i}>{block(t as Tokens.Generic, mention)}</Fragment>);
}

function block(t: Tokens.Generic, mention?: RenderMention, tail?: ReactNode): ReactNode {
  switch (t.type) {
    case "space": case "def": return null;
    case "paragraph": return <p className="flow-root">{inline(t.tokens || [], mention)}{tail}</p>;
    case "text": return <p className="flow-root">{t.tokens ? inline(t.tokens, mention) : decode(t.text)}{tail}</p>;
    case "heading": return <p className="flow-root font-display text-[1.05em] font-bold leading-snug">{inline(t.tokens || [], mention)}{tail}</p>;
    case "hr": return <hr className="border-0 border-t-[1.5px] border-current opacity-20" />;
    case "code": return (
      <pre className="max-h-80 overflow-auto rounded-xl bg-ink/[.06] px-3 py-2 font-mono text-[13px] leading-relaxed [overflow-wrap:normal]">
        <code>{(t as Tokens.Code).text}</code>
      </pre>
    );
    case "blockquote": return <blockquote className="border-l-[3px] border-current/30 pl-3 text-text-2 [&>*+*]:mt-1.5">{blocks(t.tokens || [], mention)}</blockquote>;
    case "list": return list(t as Tokens.List, mention);
    case "table": return table(t as Tokens.Table, mention);
    case "html": return <p className="flow-root">{t.text}{tail}</p>;
    default: return t.tokens ? <p>{inline(t.tokens, mention)}</p> : <p>{t.raw}</p>;
  }
}

function list(t: Tokens.List, mention?: RenderMention) {
  const items = t.items.map((it, i) => (
    <li key={i} className="pl-1 marker:text-muted [&>p]:inline [&>p+*]:mt-1">
      {it.task && <span aria-hidden="true" className="mr-1.5 inline-block">{it.checked ? "☑" : "☐"}</span>}
      {it.tokens.map((c, j) => <Fragment key={j}>{block(c as Tokens.Generic, mention)}</Fragment>)}
    </li>
  ));
  return t.ordered
    ? <ol start={typeof t.start === "number" ? t.start : undefined} className="list-decimal space-y-0.5 pl-6">{items}</ol>
    : <ul className="list-disc space-y-0.5 pl-5">{items}</ul>;
}

function table(t: Tokens.Table, mention?: RenderMention) {
  const align = (a: string | null) => (a === "right" ? "text-right" : a === "center" ? "text-center" : "text-left");
  return (
    <div className="max-w-full overflow-x-auto rounded-xl border border-current/20">
      <table className="w-full border-collapse text-[14px] tnum [overflow-wrap:normal]">
        <thead className="bg-ink/[.05]">
          <tr>{t.header.map((c, i) => <th key={i} className={"px-3 py-1.5 font-semibold " + align(c.align)}>{inline(c.tokens, mention)}</th>)}</tr>
        </thead>
        <tbody>
          {t.rows.map((r, i) => (
            <tr key={i} className="border-t border-current/15">
              {r.map((c, j) => <td key={j} className={"px-3 py-1.5 align-top " + align(c.align)}>{inline(c.tokens, mention)}</td>)}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function inline(tokens: Token[], mention?: RenderMention): ReactNode[] {
  return tokens.map((t, i) => <Fragment key={i}>{span(t as Tokens.Generic, mention)}</Fragment>);
}

function span(t: Tokens.Generic, mention?: RenderMention): ReactNode {
  switch (t.type) {
    case "text": return t.tokens ? inline(t.tokens, mention) : decode(t.text);
    case "escape": return t.text;
    case "strong": return <strong className="font-bold">{inline(t.tokens || [], mention)}</strong>;
    case "em": return <em>{inline(t.tokens || [], mention)}</em>;
    case "del": return <del className="opacity-70">{inline(t.tokens || [], mention)}</del>;
    case "codespan": return <code className="rounded-md bg-ink/[.07] px-1 py-px font-mono text-[0.9em]">{t.text}</code>;
    case "br": return <br />;
    case "html": return t.text;
    case "checkbox": return <span aria-hidden="true">{t.checked ? "☑ " : "☐ "}</span>;
    case "link": {
      const l = t as Tokens.Link;
      const ref = mentionHref.exec(l.href);
      if (ref && mention && l.text.startsWith("@")) return mention(ref[1] as MentionKind, ref[2], l.text.slice(1));
      if (!safeHref.test(l.href)) return inline(l.tokens, mention);
      return (
        <a href={l.href} target="_blank" rel="noopener noreferrer" title={l.title || undefined}
          className="font-medium underline decoration-current/40 decoration-[1.5px] underline-offset-2 hover:decoration-current">
          {l.autolink ? l.text : inline(l.tokens, mention)}
        </a>
      );
    }
    case "image": {
      const img = t as Tokens.Image;
      const label = img.text || img.href;
      return safeHref.test(img.href)
        ? <a href={img.href} target="_blank" rel="noopener noreferrer" className="underline decoration-current/40 underline-offset-2">{label}</a>
        : label;
    }
    default: return t.raw;
  }
}
