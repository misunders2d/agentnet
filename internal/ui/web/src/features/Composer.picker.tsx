// The @ picker: people and agents in this conversation, as a listbox the
// text field drives (arrows move, Enter or Tab picks, Escape closes). It
// floats above the composer and never takes focus from the text.
import { useEffect, useRef } from "react";
import { AgentAvatar, PersonAvatar } from "../ui/Avatar";
import { Tag } from "../ui/Tag";
import type { Candidate } from "./Composer.mentions";

export const optionId = (list: string, i: number) => list + "-o" + i;

export function MentionList({ id, items, active, query, onPick, onHover }: {
  id: string; items: Candidate[]; active: number; query: string;
  onPick: (c: Candidate) => void; onHover: (i: number) => void;
}) {
  const box = useRef<HTMLUListElement>(null);
  useEffect(() => {
    box.current?.querySelector("[data-active]")?.scrollIntoView({ block: "nearest" });
  }, [active]);
  return (
    <div className="absolute inset-x-2 bottom-full z-20 mb-2 overflow-hidden rounded-2xl bg-surface stroke shadow-pop fade-in lg:inset-x-6 lg:max-w-md">
      <p className="px-4 pt-3 pb-1 text-[13px] font-semibold text-muted" id={id + "-label"}>
        {items.length ? "Mention someone here" : query ? "No one here matches “" + query + "”" : "No one else is here yet"}
      </p>
      {items.length > 0 && (
        <ul ref={box} id={id} role="listbox" aria-labelledby={id + "-label"} className="max-h-72 overflow-y-auto px-1.5 pb-1.5">
          {items.map((c, i) => (
            <li key={c.key} id={optionId(id, i)} role="option" aria-selected={i === active} data-active={i === active ? "" : undefined}
              onMouseDown={(e) => e.preventDefault()} onClick={() => onPick(c)} onMouseMove={() => i !== active && onHover(i)}
              className="flex min-h-13 cursor-pointer items-center gap-3 rounded-xl px-2.5 py-1.5 data-[active]:bg-sunken">
              {c.kind === "agent" ? <AgentAvatar seed={c.seed} size={32} device={c.device} /> : <PersonAvatar name={c.name} seed={c.seed} size={32} guest={c.kind === "guest"} />}
              <span className="min-w-0 flex-1">
                <span className="flex items-center gap-1.5">
                  <span className="truncate font-semibold">{c.name}</span>
                  {c.kind === "guest" && <Tag tone="guest">Guest</Tag>}
                </span>
                <span className="block truncate text-[13px] text-muted">{c.sub}</span>
              </span>
              {c.kind === "agent" && <span className="shrink-0 rounded-full bg-agent px-2.5 py-1 text-[13px] font-semibold text-agent-ink">Ask</span>}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
