// One row of the chat list: who, the newest line, and what the server says
// about it (unread, waiting for your OK, asked of you, an agent at work,
// read only).
import { IconClock, IconLock, IconShieldExclamation } from "@tabler/icons-react";
import type { T } from "../api";
import { deviceKind, when } from "../model";
import { AgentAvatar, GroupAvatar, PersonAvatar, type Mood } from "../ui/Avatar";
import type { ListItem } from "./ChatList.words";

/** presence is what the server's current member list says about any of
 *  these devices: connected or not, or null when the list does not say now. */
export function presence(o: T.Overview | null, addresses: string[]): boolean | null {
  const d = o?.directory;
  if (!d || !d.current) return null;
  const listed = (d.members || []).filter((m) => addresses.includes(m.address));
  return listed.length ? listed.some((m) => m.presence === "connected") : null;
}

/** groupFaces: the members a group's avatar shows, others before you, so it
 *  never looks like a single person's chat. */
function groupFaces(summary: T.DMSummary | undefined, me: string | undefined, item: ListItem): string[] {
  const members = summary?.members || [];
  const others = members.filter((m) => m.person !== me).map((m) => m.label);
  if (others.length >= 2) return others;
  const all = others.concat(members.filter((m) => m.person === me).map((m) => m.label));
  return all.length >= 2 ? all : [item.title];
}

export function ChatRow({ item, summary, overview, selected, wide, onOpen }: {
  item: ListItem; summary?: T.DMSummary; overview: T.Overview | null; selected: boolean; wide: boolean; onOpen: () => void;
}) {
  const me = overview?.person?.person;
  const unread = item.unread > 0;
  const guestHere = summary?.role === "human_guest";
  const waiting = summary?.waiting || 0;
  const waitingText = waiting === 1 ? "1 message waiting to send" : waiting + " messages waiting to send";
  const mood: Mood = item.working ? "working" : item.needsYou ? "waiting" : "neutral";
  const peerDevices = summary ? (summary.peer.devices || []).map((d) => d.address).concat(summary.peer.address) : [];
  const avatar = item.kind === "agent" ? <AgentAvatar seed={item.avatarSeed} size={48} mood={mood} device={deviceKind(item.avatarSeed)} />
    : item.kind === "group" ? <GroupAvatar names={groupFaces(summary, me, item)} seed={item.avatarSeed} size={48} />
    : <PersonAvatar name={item.title} seed={item.avatarSeed} size={48} online={presence(overview, peerDevices)} />;
  const subtitle = guestHere ? "You’re a guest here" : item.kind === "agent" ? item.subtitle || "" : "";

  // Phones show one screen at a time: only the desktop column marks the open chat; a
  // phone outlines the chat being opened, at once, while it loads.
  return (
    <button type="button" onClick={onOpen} aria-current={selected && wide ? "true" : undefined}
      className={"relative flex w-full items-center gap-3 text-left transition-colors duration-200 "
        + (!wide ? "px-4 py-3 hover:bg-sunken active:bg-sunken " + (selected ? "z-[1] bg-surface [box-shadow:inset_0_0_0_1.5px_var(--an-outline)]" : "")
          : "rounded-2xl border px-2.5 py-2.5 " + (selected ? "border-outline bg-surface shadow-pop-sm" : "border-transparent hover:bg-sunken"))}>
      {avatar}
      <span className="min-w-0 flex-1">
        <span className="flex items-baseline gap-2">
          <span className={"truncate text-[16.5px] leading-tight tracking-[-.01em] lg:text-[16px] " + (item.frozen ? "font-bold text-text-2" : "font-extrabold")}>{item.title}</span>
          {item.guests > 0 && <span className="shrink-0 text-[13px] font-bold text-guest-ink">+{item.guests} {item.guests === 1 ? "guest" : "guests"}</span>}
          <time dateTime={item.lastAt} className={"ml-auto shrink-0 text-[12px] tnum " + (unread ? "font-extrabold text-ink" : "font-semibold text-muted")}>{when(item.lastAt)}</time>
        </span>
        {(subtitle || item.note || item.needsYou > 0 || item.held > 0 || item.keyChanged) && (
          <span className="mt-1 flex min-w-0 items-center gap-2">
            {item.keyChanged && (
              <span className="inline-flex shrink-0 items-center gap-1 rounded-full bg-danger-bg px-2 py-px text-[12px] font-extrabold leading-[18px] text-danger stroke">
                <IconShieldExclamation size={13} stroke={2.4} aria-hidden="true" />Identity changed
              </span>
            )}
            {item.needsYou > 0 && (
              <span className="shrink-0 rounded-full bg-act px-2 py-px text-[12px] font-extrabold leading-[18px] text-act-ink stroke">
                {item.needsYou > 1 ? item.needsYou + " need your OK" : "Needs your OK"}
              </span>
            )}
            {/* A question or task held for you to answer: not an OK, so quiet and uncounted. */}
            {item.held > 0 && (
              <span className="shrink-0 rounded-full border border-hairline bg-sunken px-2 py-px text-[12px] font-bold leading-[18px] text-text-2">Asked you</span>
            )}
            <span className="min-w-0 truncate text-[13px]">
              {subtitle && <span className={"font-bold " + (guestHere ? "text-guest-ink" : "text-agent-ink")}>{subtitle}</span>}
              {item.note && <span className="font-semibold text-muted">{subtitle ? " · " : ""}{item.note}</span>}
            </span>
          </span>
        )}
        <span className="mt-1 flex items-center gap-2">
          <LastLine item={item} unread={unread} />
          {waiting > 0 && (
            <span className="shrink-0 text-muted" title={waitingText}>
              <IconClock size={16} stroke={2.2} aria-hidden="true" />
              <span className="sr-only">{waitingText}</span>
            </span>
          )}
          {unread && (
            <span className="grid h-[22px] min-w-[22px] shrink-0 place-items-center rounded-full bg-ink px-1.5 text-[12px] font-extrabold text-canvas tnum">
              {item.unread > 99 ? "99+" : item.unread}<span className="sr-only"> unread</span>
            </span>
          )}
        </span>
      </span>
    </button>
  );
}

function LastLine({ item, unread }: { item: ListItem; unread: boolean }) {
  const text = item.last || (item.kind === "group" ? "No messages yet" : "Nothing said yet");
  // Who came or left reads quieter than what people said.
  const tone = unread ? "font-semibold text-ink" : item.event ? "text-muted" : "text-text-2";
  return (
    <span className="flex min-w-0 flex-1 items-center gap-1.5 text-[14.5px] leading-snug lg:text-[14px]">
      {item.working && (
        <span className="flex shrink-0 items-center gap-1 font-extrabold text-agent-ink">
          Working<span aria-hidden="true" className="flex gap-0.5"><i className="working-dot size-1 rounded-full bg-current" /><i className="working-dot size-1 rounded-full bg-current" /><i className="working-dot size-1 rounded-full bg-current" /></span>
        </span>
      )}
      {item.frozen && (
        <span className="flex shrink-0 items-center gap-1 font-semibold text-muted" title={item.frozen}>
          <IconLock size={14} stroke={2.4} aria-hidden="true" />Read only ·
        </span>
      )}
      <span className={"truncate " + tone + (item.last ? "" : " italic")}>{text}</span>
    </span>
  );
}
