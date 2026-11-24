import { parseCollective } from '../collective-mentions.mjs';
// Markdown: a message's text as React elements, built from marked's lexer.
// Nothing becomes an HTML string: raw HTML in a message reads as the text
// it is, pictures stay links, and web and mail links open in a new tab.
// Exact portable AgentNet destinations use the host's guarded navigation.
// Mentions ([@Name](agentnet:person/ID)) are handed to
// the caller, who knows who is in the conversation.
import { type Token, type Tokens } from "marked";
import { createContext, Fragment, memo, useContext, useMemo, useState, type ReactNode } from "react";
import { hasSpoilerPlaceholder, hiddenText, markdownInlineTokens, markdownText, markdownTokens, shortLinkLabel, spoilerFallback } from "../linkText";

export type MentionKind = "person" | "guest" | "agent" | "collective";
export type RenderMention = (kind: MentionKind, id: string, name: string) => ReactNode;

const mentionHref = /^agentnet:(person|guest|agent)\/([A-Za-z0-9_-]{1,64})$/;
const safeHref = /^(https?:\/\/|mailto:)/i;
const portableHref = /^agentnet:\/\/open#realm=[0-9a-f]{32}&(?:conv=[0-9a-f]{64}&topic=(?:main|[0-9a-f]{32})|thread=[0-9a-f]{32})$/;
const sourceHref = /^agentnet:message\/([0-9a-f]{32})(?:\?conv=([0-9a-f]{64}))?$/;
export const MessageLinks = createContext<((id: string, conv?: string) => void) | null>(null);

function SourceLink({id, conv, children}: {id: string; conv?: string; children: ReactNode}) {
  const open = useContext(MessageLinks);
  return open ? <button type="button" className="font-medium underline underline-offset-2" onClick={() => open(id, conv)}>{children}</button> : children;
}

const decode = (text: string) => markdownText(spoilerFallback(text));

function Spoiler({ text, mention }: { text: string; mention?: RenderMention }) {
  const [open, setOpen] = useState(false);
  const content = useMemo(() => open ? markdownInlineTokens(text) : [], [open, text]);
  const toggle = () => setOpen(value => !value);
  return <span role="button" tabIndex={0} aria-pressed={open}
    className={"cursor-pointer rounded-sm px-0.5 focus-visible:outline-2 focus-visible:outline-agent-ink " +
      (open ? "whitespace-pre-wrap bg-ink/[.05]" : "select-none bg-ink/15 font-medium")}
    onClick={event => {
      const link = (event.target as Element).closest("a[href],button");
      if (open && link && event.currentTarget.contains(link)) return;
      event.preventDefault(); event.stopPropagation(); toggle();
    }}
    onKeyDown={event => {
      if (event.target === event.currentTarget && (event.key === "Enter" || event.key === " ")) {
        event.preventDefault(); event.stopPropagation(); toggle();
      }
    }}>{open ? inline(content, mention) : hiddenText}</span>;
}

/** Markdown renders text; tail (a message's time) sits at the end of its last line when that line is text. */
export const Markdown = memo(function Markdown({ text, mention, tail, className = "", spoilers = "mask" }: { text: string; mention?: RenderMention; tail?: ReactNode; className?: string; spoilers?: "mask" | "reveal" | "literal" }) {
  const tokens = useMemo(() => markdownTokens(text, spoilers).filter((t) => t.type !== "space" && t.type !== "def"), [text, spoilers]);
  const end = tokens[tokens.length - 1];
  const inlineEnd = !!end && ["paragraph", "text", "heading", "html"].includes(end.type);
  return (
    <div className={"min-w-0 break-words [overflow-wrap:anywhere] [&>*+*]:mt-2 " + className}>
      {tokens.map((t, i) => <Fragment key={i}>{block(t as Tokens.Generic, mention, inlineEnd && i === tokens.length - 1 ? tail : undefined)}</Fragment>)}
      {tail && !inlineEnd && <div className="flow-root">{tail}</div>}
    </div>
  );
});

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
        <code>{spoilerFallback((t as Tokens.Code).text)}</code>
      </pre>
    );
    case "blockquote": return <blockquote className="border-l-[3px] border-current/30 pl-3 text-text-2 [&>*+*]:mt-1.5">{blocks(t.tokens || [], mention)}</blockquote>;
    case "list": return list(t as Tokens.List, mention);
    case "table": return table(t as Tokens.Table, mention);
    case "html": return <p className="flow-root">{spoilerFallback(t.text)}{tail}</p>;
    default: return t.tokens ? <p>{inline(t.tokens, mention)}</p> : <p>{spoilerFallback(t.raw)}</p>;
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
    case "escape": return spoilerFallback(t.text);
    case "underline": return <u>{inline(t.tokens || [], mention)}</u>;
    case "spoiler": return <Spoiler key={t.text} text={t.text} mention={mention} />;
    case "strong": return <strong className="font-bold">{inline(t.tokens || [], mention)}</strong>;
    case "em": return <em>{inline(t.tokens || [], mention)}</em>;
    case "del": return <del className="opacity-70">{inline(t.tokens || [], mention)}</del>;
    case "codespan": return <code className="rounded-md bg-ink/[.07] px-1 py-px font-mono text-[0.9em]">{spoilerFallback(t.text)}</code>;
    case "br": return <br />;
    case "html": return spoilerFallback(t.text);
    case "checkbox": return <span aria-hidden="true">{t.checked ? "☑ " : "☐ "}</span>;
    case "link": {
      const l = t as Tokens.Link;
      if (hasSpoilerPlaceholder(l.href) || hasSpoilerPlaceholder(l.title || "")) return inline(l.tokens, mention);
      const collective=parseCollective(l.href);
      if(collective && !hasSpoilerPlaceholder(l.text) && l.text.startsWith('@'))return mention?mention('collective',l.href,l.text.slice(1)):<strong>{l.text}</strong>;
      const ref = mentionHref.exec(l.href);
      if (ref && mention && !hasSpoilerPlaceholder(l.text) && l.text.startsWith("@")) return mention(ref[1] as MentionKind, ref[2], l.text.slice(1));
      const source = sourceHref.exec(l.href);
      if (source) return <SourceLink id={source[1]} conv={source[2]}>{inline(l.tokens, mention)}</SourceLink>;
      const portable = portableHref.test(l.href);
      if (!portable && !safeHref.test(l.href)) return inline(l.tokens, mention);
      const label = hasSpoilerPlaceholder(l.text) ? l.text : shortLinkLabel(l.text, l.href);
      const shortened = label !== l.text;
      return (
        <a href={l.href} target={portable ? undefined : "_blank"} rel="noopener noreferrer" title={shortened ? l.href : l.title || undefined} aria-label={shortened ? l.href : undefined}
          className="font-medium underline decoration-current/40 decoration-[1.5px] underline-offset-2 hover:decoration-current">
          {shortened ? label : l.autolink ? spoilerFallback(l.text) : inline(l.tokens, mention)}
        </a>
      );
    }
    case "image": {
      const img = t as Tokens.Image;
      const label = spoilerFallback(img.text || img.href);
      return safeHref.test(img.href) && !hasSpoilerPlaceholder(img.href)
        ? <a href={img.href} target="_blank" rel="noopener noreferrer" className="underline decoration-current/40 underline-offset-2">{label}</a>
        : label;
    }
    default: return spoilerFallback(t.raw);
  }
}
