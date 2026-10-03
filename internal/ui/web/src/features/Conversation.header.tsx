// The conversation's header (who this is, whether their computer is on,
// Bring in, and the overflow menu) and, on phones, the guest bar: who is
// helping here right now, with Dismiss said in words.
import { useState } from "react";
import { Menu } from "@base-ui/react/menu";
import {
  IconChevronLeft, IconDotsVertical, IconUserPlus, IconUsers, IconBell, IconBellOff, IconTrash, IconHandStop,
} from "@tabler/icons-react";
import type { Api, T } from "../api";
import { useApp } from "../context";
import { useStore } from "../store";
import { deviceKind, niceDevice, personName, timeOf, type DeviceKind } from "../model";
import { AgentAvatar, GroupAvatar, PersonAvatar } from "../ui/Avatar";
import { Button, IconButton } from "../ui/Button";
import { Tag } from "../ui/Tag";
import { Confirm } from "./Message.actions";
import { agentLabel, eventKind, hostOf, roomTitle, threadAgentName, type Ctx } from "./Message.model";

// ---- who is helping -------------------------------------------------------------------

export interface Helper {
  pid: string;
  who: string;             // the invite sheet's key for them, to bring them back ("p:<person>", "a:<host>#<agent id>")
  name: string;
  agent: boolean;
  seed: string;
  device?: DeviceKind;     // an agent's device badge
  sub: string;             // "since 10:33", "Waiting for your OK", "Waiting for Anna to join"
  active: boolean;         // in the room now; otherwise invited, not a guest yet
  dismiss?: () => Promise<unknown>;
  review: boolean;         // invited, and this person decides
}

/** helpers: agents and people taking part as guests (active or invited), from the server's views. */
export function helpers(ctx: Ctx, run: <R>(fn: (a: Api) => Promise<R>) => Promise<R | undefined>): Helper[] {
  const t = ctx.dm;
  if (!t) return [];
  const joined = (pid: string) => (t.messages || []).find((m) => m.pid === pid && m.event && eventKind(m.event)?.kind === "joined")?.at;
  const out: Helper[] = [];
  for (const a of t.agents || []) {
    if (a.state !== "active" && a.state !== "invited") continue;
    const since = joined(a.pid) || a.invited;
    out.push({
      pid: a.pid, who: "a:" + a.host.address + "#" + (a.agent_id || ""), name: agentLabel(a, ctx), agent: true, seed: a.agent_id || a.host.address, device: deviceKind(a.host.address), active: a.state === "active",
      // An agent joins once its owner says OK (the room panel's words).
      sub: a.state === "active" ? "since " + timeOf(since) : a.host_here ? "Waiting for your OK" : "Waiting for " + personName(a.host) + "’s OK",
      dismiss: a.can_dismiss ? () => run((x) => x.dismissAgent(a.pid)) : undefined, review: a.can_decide,
    });
  }
  for (const g of t.guests || []) {
    if (g.state !== "active" && g.state !== "invited") continue;
    const since = joined(g.pid) || "";
    out.push({
      pid: g.pid, who: g.host.person ? "p:" + g.host.person : "", name: personName(g.host), agent: false, seed: g.host.person || g.host.address, active: g.state === "active",
      sub: g.state === "active" ? (since ? "since " + timeOf(since) : "here now") : g.host_here ? "You’re invited to help" : "Waiting for " + personName(g.host) + " to join",
      dismiss: g.can_end && !g.host_here ? () => run((x) => x.endGuest(g.pid)) : undefined, review: g.can_decide,
    });
  }
  return out;
}

/** headcount: "2 guests", "1 guest · 1 invited" — someone invited is not a guest yet. */
export function headcount(hs: Helper[]): string {
  const here = hs.filter((h) => h.active).length, asked = hs.length - here;
  return [here && here + (here === 1 ? " guest" : " guests"), asked && asked + " invited"].filter(Boolean).join(" · ");
}

/** HelperTag: GUEST while in the room; INVITED (yellow when it waits for you) until then. */
function HelperTag({ h }: { h: Helper }) {
  return h.active ? <Tag tone="guest">Guest</Tag> : <Tag tone={h.review ? "act" : "muted"}>Invited</Tag>;
}

// ---- presence -------------------------------------------------------------------------

/** presence: the Hub's word on any of these devices, only while its view is current. */
export function presence(addresses: string[], o: T.Overview | null): { online: boolean | null; text: string } {
  const d = o?.directory;
  if (!d || d.status !== "listed") return { online: null, text: "" };
  if (!d.current) return { online: null, text: "Connection not known now" };
  const seen = addresses.map((a) => ({ a, p: (d.members || []).find((m) => m.address === a)?.presence })).filter((x) => x.p);
  const on = seen.find((x) => x.p === "connected");
  if (on) return { online: true, text: "Online · " + niceDevice(on.a) };
  if (seen.some((x) => x.p === "reconnecting")) return { online: false, text: "Reconnecting" };
  return seen.length ? { online: false, text: "Offline" } : { online: null, text: "" };
}

const presenceTitle = "Whether the server sees their AgentNet running now. It doesn’t mean a person is there.";

// ---- header -------------------------------------------------------------------------------

export function Header({ ctx, wide, helpers: hs, canInvite }: { ctx: Ctx; wide: boolean; helpers: Helper[]; canInvite: boolean }) {
  const store = useApp();
  const overview = ctx.overview;
  const t = ctx.dm, th = ctx.thread;
  const [del, setDel] = useState(false);
  const unread = useStore(store, (s) => [...(s.overview?.dms || []), ...(s.overview?.threads || [])].reduce((n, c) => n + (c.id === ctx.conv ? 0 : c.unread), 0));

  const me = overview?.person;
  let title = "", sub = "", online: boolean | null = null, avatar = null;
  if (t && t.kind === "group") {
    const others = (t.members || []).filter((m) => !(me && m.person === me.person)).map((m) => m.label);
    title = t.title || "Group";
    const n = (t.members || []).length;
    sub = n + (n === 1 ? " member" : " members") + " · " + (others.length ? "you, " + others.join(", ") : "just you");
    avatar = <GroupAvatar names={others.length ? others : [title]} seed={t.id} size={wide ? 48 : 40} />;
  } else if (t && (t.role === "human_guest" || t.role === "visitor")) {
    title = roomTitle(t);
    const mine = (t.guests || []).find((g) => g.host_here);
    sub = t.role === "visitor" ? "Your agent’s view · only what was shared"
      : mine?.state === "active" ? "You’re a guest here" : mine?.state === "invited" ? "You’re invited as a guest" : "You were a guest here";
    const names = (t.members || []).map((p) => personName(p));
    avatar = <GroupAvatar names={names.length ? names : [title]} seed={t.id} size={wide ? 48 : 40} />;
  } else if (t) {
    title = personName(t.peer);
    const p = presence([t.peer.address, ...(t.peer.devices || []).map((d) => d.address)], overview);
    online = p.online;
    sub = p.text;
    avatar = <PersonAvatar name={title} seed={t.peer.person || t.peer.address} size={wide ? 48 : 40} online={online} />;
  } else if (th) {
    title = threadAgentName(ctx);
    const p = presence([th.peer], overview);
    online = p.online;
    sub = [hostOf(th.peer, overview) ? "on " + niceDevice(th.peer) : "Agent", p.text.replace(/ · .*$/, "")].filter(Boolean).join(" · ");
    avatar = <AgentAvatar seed={th.peer} size={wide ? 48 : 40} device={deviceKind(th.peer)} mood={online === false ? "asleep" : "neutral"} />;
  }

  const notify = overview?.notify;
  const muted = !!t && !!notify && (notify.mutes || []).includes(t.id);
  const remove = () => void store.run((a) => a.deleteConversation(t ? { conv: t.id } : { peer: th!.peer, thread: ctx.conv })).then((r) => {
    if (!r) return;
    store.close();
    if (r.note) store.toast(r.note, "ok");
  });

  const item = "flex min-h-11 cursor-pointer items-center gap-3 rounded-xl px-3 text-[15px] font-medium outline-none data-[highlighted]:bg-sunken";
  return (
    <header className={"flex shrink-0 items-center gap-2 border-b-[1.5px] border-outline bg-surface " + (wide ? "h-[76px] px-5" : "h-16 pl-1 pr-1.5")}>
      {!wide && (
        <button type="button" onClick={() => store.close()} aria-label={unread ? "Back to chats, " + unread + " unread" : "Back to chats"}
          className="flex h-11 shrink-0 items-center rounded-full pl-1 pr-1.5 hover:bg-sunken">
          <IconChevronLeft size={26} stroke={2.2} />
          {unread > 0 && <span className="grid h-5 min-w-5 place-items-center rounded-full bg-ink px-1.5 text-[11px] font-bold text-canvas tnum">{unread > 99 ? "99+" : unread}</span>}
        </button>
      )}
      <button type="button" onClick={t ? () => store.setPanel(true) : undefined} disabled={!t}
        className="flex min-w-[min(220px,40%)] flex-1 items-center gap-3 rounded-2xl py-1 pr-2 text-left enabled:hover:bg-sunken/60 disabled:cursor-default"
        aria-label={t ? title + ". Who’s in this chat" : undefined}>
        {avatar}
        <span className="min-w-0">
          <span className={"block truncate font-display font-bold leading-tight " + (wide ? "text-[22px]" : "text-[18px]")}>{title || " "}</span>
          {sub && (
            <span className="flex items-center gap-1.5 truncate text-[13px] text-text-2" title={online != null ? presenceTitle : undefined}>
              {online != null && <span aria-hidden="true" className={"size-2 shrink-0 rounded-full " + (online ? "bg-online" : "[box-shadow:inset_0_0_0_1.5px_var(--an-muted)]")} />}
              <span className="truncate">{sub}</span>
            </span>
          )}
        </span>
      </button>

      {wide && hs.length > 0 && <GuestChips helpers={hs} />}

      {canInvite && (wide
        ? <Button variant="act" icon={<IconUserPlus size={20} />} onClick={() => store.openInvite(ctx.conv)} className="shrink-0">Bring in</Button>
        : <IconButton label="Bring someone in" onClick={() => store.openInvite(ctx.conv)} className="shrink-0 bg-act text-act-ink stroke hover:bg-act"><IconUserPlus size={21} /></IconButton>)}

      <Menu.Root>
        <Menu.Trigger aria-label="More" title="More" className="grid size-11 shrink-0 place-items-center rounded-full hover:bg-sunken data-[popup-open]:bg-sunken">
          <IconDotsVertical size={22} />
        </Menu.Trigger>
        <Menu.Portal>
          <Menu.Positioner side="bottom" align="end" sideOffset={6} collisionPadding={12} className="z-50">
            <Menu.Popup className="min-w-60 rounded-2xl bg-surface p-1.5 text-ink outline-none stroke shadow-pop transition-[opacity,scale] duration-150 data-[starting-style]:scale-95 data-[starting-style]:opacity-0 data-[ending-style]:opacity-0 motion-reduce:transition-opacity">
              {t && <Menu.Item className={item} onClick={() => store.setPanel(true)}><IconUsers size={20} />In this chat</Menu.Item>}
              {t && notify?.available && (
                <Menu.Item className={item} onClick={() => void store.run((a) => a.notify("mute", { conv: t.id, muted: !muted }), muted ? "Notifications on for this chat" : "This chat won’t notify you")}>
                  {muted ? <IconBell size={20} /> : <IconBellOff size={20} />}{muted ? "Turn notifications on" : "Mute notifications"}
                </Menu.Item>
              )}
              {(t || th) && <Menu.Separator className="mx-2 my-1 h-px bg-hairline" />}
              {(t || th) && <Menu.Item className={item + " text-danger"} onClick={() => setDel(true)}><IconTrash size={20} />Delete conversation…</Menu.Item>}
            </Menu.Popup>
          </Menu.Positioner>
        </Menu.Portal>
      </Menu.Root>

      <Confirm open={del} onOpenChange={setDel} ok="Delete" onOk={remove}
        title={th ? "Delete this thread from this device?" : "Delete this conversation from your devices?"}>
        {th ? <p>It’s stored on this device only, so it’s deleted here and nowhere else. {title} keeps its copy.</p> : <>
          <p>The messages go from this device and, once they connect, from your other linked devices.</p>
          <p>Everyone else keeps their copies. Anything already running finishes first. Members and guests stay as they are; a new message brings the chat back.</p>
        </>}
      </Confirm>
    </header>
  );
}

// GuestChips: a summary of who is helping, for the desktop header while the
// "In this chat" panel is closed; the panel is where guests are managed. It
// opens on its own when someone is helping, so the chips hide while it shows.
function GuestChips({ helpers: hs }: { helpers: Helper[] }) {
  const store = useApp();
  const panel = useStore(store, (s) => s.panel);
  if (panel) return null;
  const one = hs.length === 1 ? hs[0] : null;
  return (
    <button type="button" onClick={() => store.setPanel(true)} title={hs.map((h) => h.name + (h.active ? "" : " (invited)")).join(", ") + " · open “In this chat”"}
      className="flex h-11 min-w-0 shrink items-center rounded-full px-1 hover:bg-sunken [body:has(aside[aria-label='In_this_chat'])_&]:hidden">
      <span className="inline-flex h-9 min-w-0 items-center gap-1.5 rounded-full border-[1.5px] border-dashed border-guest bg-guest-bg pl-1 pr-2 text-[13px] font-bold text-ink">
        <span className="flex shrink-0 -space-x-1.5">
          {hs.slice(0, 3).map((h) => <span key={h.pid} className="rounded-full ring-2 ring-guest-bg">{h.agent ? <AgentAvatar seed={h.seed} size={24} /> : <PersonAvatar name={h.name} seed={h.seed} size={24} />}</span>)}
        </span>
        <span className="min-w-0 truncate">{one ? one.name : headcount(hs)}</span>
        {one && <HelperTag h={one} />}
      </span>
    </button>
  );
}

// ---- the phone guest bar ----------------------------------------------------------------------

export function GuestBar({ helpers: hs, onDismissed }: { helpers: Helper[]; onDismissed: (h: Helper) => void }) {
  const store = useApp();
  const [busy, setBusy] = useState(false);
  if (!hs.length) return null;
  if (hs.length > 1) {
    return (
      <div className="flex h-[52px] shrink-0 items-center gap-2.5 border-b-[1.5px] border-outline bg-guest-bg pl-3 pr-2">
        <span className="flex shrink-0 -space-x-2" aria-hidden="true">
          {hs.slice(0, 3).map((h) => <span key={h.pid} className="rounded-full ring-2 ring-guest-bg">{h.agent ? <AgentAvatar seed={h.seed} size={28} /> : <PersonAvatar name={h.name} seed={h.seed} size={28} />}</span>)}
        </span>
        <p className="min-w-0 flex-1 truncate text-[14px]"><b className="font-bold">{headcount(hs)}</b><span className="text-text-2"> · {hs.map((h) => h.name).join(", ")}</span></p>
        <Button size="sm" variant="outline" onClick={() => store.setPanel(true)}>Manage</Button>
      </div>
    );
  }
  const h = hs[0];
  const dismiss = async () => {
    if (!h.dismiss) return;
    setBusy(true);
    const r = await h.dismiss();
    setBusy(false);
    if (r) onDismissed(h);
  };
  return (
    <div className="flex h-[52px] shrink-0 items-center gap-2.5 border-b-[1.5px] border-outline bg-guest-bg pl-3 pr-2">
      {h.agent ? <AgentAvatar seed={h.seed} size={32} guest device={h.device} mood={h.active ? "neutral" : "waiting"} /> : <PersonAvatar name={h.name} seed={h.seed} size={32} guest />}
      <span className="min-w-0 flex-1 leading-tight">
        <span className="flex items-center gap-1.5"><b className="truncate text-[14px] font-bold">{h.name}</b><HelperTag h={h} /></span>
        <span className={"block truncate text-[13px] " + (!h.active && h.review ? "font-semibold text-approval-ink" : "text-text-2")}>{h.sub}</span>
      </span>
      {h.review ? <Button size="sm" variant="act" onClick={() => store.setPanel(true)}>Review</Button>
        : h.dismiss && (
          <Button size="sm" variant="outline" icon={<IconHandStop size={18} />} disabled={busy} onClick={dismiss}
            title="Stops new messages. What was already shared stays with them.">Dismiss</Button>
        )}
    </div>
  );
}

