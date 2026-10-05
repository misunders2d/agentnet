// The OKs list's building blocks: a card that opens what it names, the
// words inside it, the kind tag, and where focus lands after a decision.
// Shared by every row on the screen.
import { createContext, useContext, type ReactNode } from "react";
import { IconChevronRight } from "@tabler/icons-react";
import { useApp } from "../context";
import { when } from "../model";
import { useStore } from "../store";
import { Tag } from "../ui/Tag";

const ring = " ring-2 ring-agent-ink ring-offset-2 ring-offset-canvas";
const chevron = (
  <span className="grid size-9 shrink-0 self-center place-items-center rounded-full bg-ink text-canvas" aria-hidden="true"><IconChevronRight size={20} stroke={2.5} /></span>
);

/** Card: a row that either opens (onOpen: the whole row is one button) or
 *  holds its own buttons (children). */
export function Card({ children, onOpen, label, current }: { children: ReactNode; onOpen?: () => void; label?: string; current?: boolean }) {
  const cls = "relative flex w-full gap-3 rounded-2xl bg-surface p-3.5 text-left stroke " + (current ? ring : "");
  if (!onOpen) return <article className={cls}>{children}</article>;
  return (
    <button type="button" data-open onClick={onOpen} aria-label={label} aria-current={current || undefined} className={cls + " press shadow-pop-sm hover:bg-sunken"}>
      {children}
      {chevron}
    </button>
  );
}

/** OpenCard: a row whose face and words open the conversation it names,
 *  with its decisions below, outside that button (a button never holds
 *  another). */
export function OpenCard({ face, children, onOpen, label, current, actions, detail }: {
  face: ReactNode; children: ReactNode; onOpen: () => void; label: string; current?: boolean; actions?: ReactNode; detail?: ReactNode;
}) {
  return (
    <article className={"rounded-2xl bg-surface stroke shadow-pop-sm" + (current ? ring : "")}>
      <button type="button" data-open onClick={onOpen} aria-label={label} aria-current={current || undefined}
        className={"group press flex w-full gap-3 rounded-2xl p-3.5 text-left" + (actions ? " pb-2" : "")}>
        {face}
        {children}
        <span className="grid size-9 shrink-0 self-center place-items-center rounded-full bg-ink text-canvas transition-transform duration-200 group-hover:translate-x-0.5 motion-reduce:transition-none motion-reduce:group-hover:translate-x-0" aria-hidden="true"><IconChevronRight size={20} stroke={2.5} /></span>
      </button>
      {detail && <div className="px-3.5 pb-3.5 sm:pl-[66px]">{detail}</div>}
      {actions && <div className="flex flex-wrap items-center gap-2 px-3.5 pb-3.5 sm:pl-[66px]">{actions}</div>}
    </article>
  );
}

export function Body({ tag, at, title, quote, meta, children }: { tag?: ReactNode; at?: string; title: ReactNode; quote?: string; meta?: string; children?: ReactNode }) {
  return (
    <div className="min-w-0 flex-1">
      {(tag || at) && (
        <div className="mb-1 flex items-center gap-2">
          {tag}
          {at && <time dateTime={at} className="tnum ml-auto text-[12px] font-semibold text-muted">{when(at)}</time>}
        </div>
      )}
      <p className="text-[16px] font-bold leading-snug [overflow-wrap:anywhere]">{title}</p>
      {quote && <p className="pt-1 line-clamp-2 text-[15px] leading-snug text-text-2 [overflow-wrap:anywhere]">“{quote}”</p>}
      {meta && <p className="pt-1.5 text-[13px] font-medium text-muted">{meta}</p>}
      {children}
    </div>
  );
}

/** kindTag: Task, Question or Follow-up; tone "muted" for what is not your agent's. */
export const kindTag = (kind?: string, tone: "agent" | "muted" = "agent") =>
  kind === "task" ? <Tag tone={tone}>Task</Tag> : kind === "question" ? <Tag tone={tone}>Question</Tag> : <Tag tone="muted">Follow-up</Tag>;

/** Landing: called by a row once its decision is done (or failed), so focus
 *  moves to the next card, or the screen title, instead of the page. */
export const Landing = createContext<() => void>(() => {});
export const useLand = () => useContext(Landing);

/** useOpen: whether a conversation (at a message) is the one open, and opening it. */
export function useOpen() {
  const store = useApp();
  const open = useStore(store, (s) => s.open);
  return {
    isOpen: (id: string, focus?: string) => !!open && open.id === id && (!focus || open.focus === focus),
    go: (kind: "dm" | "thread", id: string, focus?: string) => void store.open({ kind, id, focus }),
  };
}
