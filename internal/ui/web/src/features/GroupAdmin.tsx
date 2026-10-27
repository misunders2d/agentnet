// Group admin (MEL-528): rename the group, make or remove an admin, remove
// a member, and leave. Each is one signed group change (POST
// /api/groups/manage); the server checks who may make it, and these
// controls only show where it would. Nothing here recalls what anyone
// already saved, and no role grants anyone tasks for an agent.
import { useEffect, useRef, useState, type ReactNode } from "react";
import { Menu } from "@base-ui/react/menu";
import { IconCrown, IconDoorExit, IconDotsVertical, IconPencil, IconUserMinus } from "@tabler/icons-react";
import { errorText, type T } from "../api";
import { useApp } from "../context";
import { useStore, type PendingGroupChange } from "../store";
import { personName } from "../model";
import { usePortal } from "../owned";
import { Button } from "../ui/Button";
import { Sheet } from "../ui/Sheet";

export type GroupAction = "rename" | "promote" | "demote" | "remove" | "leave";

/** The group's title limit, as the old app and the signed group state keep it. */
const TITLE_MAX = 64;

/** groupRights: what this person may change in group t. Admin actions need an
 *  admin who is a member of a group that is not frozen; anyone in it may leave. */
export function groupRights(t: T.DMThread | null, o: T.Overview | null) {
  const member = !!t && t.kind === "group" && !t.frozen && (!t.role || t.role === "member");
  const me = o?.person?.person;
  const admin = member && !!me && (t!.members || []).some((m) => m.person === me && m.admin);
  return { member, admin };
}

/** GroupChangeSheet asks for one group change in plain words and makes it. */
export function GroupChangeSheet({ open, onOpenChange, t, action, member }: {
  open: boolean; onOpenChange: (o: boolean) => void; t: T.DMThread; action: GroupAction; member?: T.GroupMemberView;
}) {
  const store = useApp();
  const [title, setTitle] = useState(t.title || "");
  const pending = useStore(store, s => s.groupChanges?.[t.id]);
  const busy = !!pending && !pending.error;
  const [error, setError] = useState("");
  useEffect(() => { if (open) { setTitle(t.title || ""); setError(""); } }, [open]);
  const who = member ? personName(member) : "";
  const group = t.title ? "“" + t.title + "”" : "this group";

  const words: Record<GroupAction, { title: string; ok: string; body: ReactNode; danger?: boolean }> = {
    rename: { title: "Rename the group", ok: "Rename", body: "Everyone in it gets the new name. The group and what was said in it stay the same." },
    promote: { title: "Make " + who + " an admin?", ok: "Make admin", body: "Admins can add people, change the name and manage members. It lets no one give tasks to anyone’s agent." },
    demote: { title: "Remove " + who + "’s admin role?", ok: "Remove admin role", body: "They stay in the group as a member. A group always keeps an admin: the last one must make someone else admin first." },
    remove: { title: "Remove " + who + " from the group?", ok: "Remove", danger: true,
      body: "They stop getting new messages once the change reaches them. Anything they already saved stays with them: it can’t be taken back." },
    leave: { title: "Leave " + group + "?", ok: "Leave group", danger: true,
      body: "You stop getting its messages once the others get your departure. What’s on your devices stays. If you’re its last admin, make someone else admin first." },
  };
  const w = words[action];

  const run = () => {
    if (busy) return;
    const name = title.trim();
    if (action === "rename" && !name) { setError("Give the group a name."); return; }
    setError("");
    const work = store.api.manageGroup({ conv: t.id, action, ...(member?.person ? { person: member.person } : {}), ...(action === "rename" ? { title: name } : {}) });
    if (!store.get().groupChanges?.[t.id]) {
      // A newer group can retire the old preview while its write still runs.
      // Keep this editor and explain an unadmitted replacement operation.
      void work.catch(e => { if (store.isActive()) setError(errorText(e)); });
      return;
    }
    onOpenChange(false);
    // Shared pending/error state survives this sheet and navigation. Do not
    // turn a write acknowledgement into a confirmed role or membership.
    void work.catch(() => {});
  };

  return (
    <Sheet open={open} onOpenChange={onOpenChange} title={w.title}
      footer={
        <div className="flex flex-col gap-2">
          {error && <p role="alert" className="text-[14px] font-semibold text-danger">{error}</p>}
          <Button variant={w.danger ? "danger" : "act"} size="lg" disabled={busy || (action === "rename" && (!title.trim() || title.trim() === t.title))} onClick={() => void run()}>{w.ok}</Button>
          <Button variant="ghost" size="lg" onClick={() => onOpenChange(false)}>Cancel</Button>
        </div>
      }>
      {action === "rename" && (
        <label className="mb-3 block">
          <span className="mb-1 block text-[14px] font-bold">Group name</span>
          <input value={title} onChange={(e) => setTitle(e.target.value)} maxLength={TITLE_MAX} autoComplete="off"
            onKeyDown={(e) => { if (e.key === "Enter") { e.preventDefault(); void run(); } }}
            className="min-h-12 w-full rounded-xl stroke bg-surface px-3.5 text-[16px] text-ink outline-none focus-visible:outline-3 focus-visible:outline-agent-ink lg:text-[15px]" />
        </label>
      )}
      <p className="text-[16px] leading-relaxed text-text-2">{w.body}</p>
    </Sheet>
  );
}

function groupChangeLabel(p: PendingGroupChange) {
  const who = p.original.members?.find(m => m.person === p.change.person);
  const name = who ? personName(who) : "Member";
  return ({ rename: "Name: “" + p.change.title + "”", promote: name + ": admin requested",
    demote: name + ": member role requested", remove: name + ": removal requested",
    leave: "You chose to leave “" + (p.original.title || "this group") + "”" })[p.change.action] || "Group change requested";
}

function GroupChangeCard({ p }: { p: PendingGroupChange }) {
  const store = useApp();
  const [checking, setChecking] = useState(false), [error, setError] = useState("");
  const current = useRef(p); current.current = p;
  useEffect(() => { setChecking(false); setError(""); }, [p]);
  const check = async () => {
    if (checking) return;
    setChecking(true); setError("");
    try { await store.checkGroupChange(p.change.conv); }
    catch (e) { if (store.isActive() && current.current === p) setError(errorText(e)); }
    finally { if (current.current === p) setChecking(false); }
  };
  return <div role={p.error ? "alert" : "status"} className="rounded-xl border border-hairline bg-surface p-3 text-sm">
    {p.change.action !== "leave" && <p className="text-muted">{p.original.title || "Group"}</p>}
    <p className="font-semibold">{groupChangeLabel(p)}</p>
    <p className="mt-1 text-muted">{p.error ? "Could not confirm this change. " + p.error : p.saved
      ? (p.queued ? "Saved locally; delivery is pending. " : "Saved. ") + "Checking the current group…"
      : "Saving this change… Roles and membership are not confirmed yet."}</p>
    {error && <p role="alert" className="text-danger">{error}</p>}
    {(p.saved || p.error) && <Button size="sm" variant="ghost" disabled={checking} onClick={() => void check()}>{checking ? "Checking group…" : "Check group change"}</Button>}
    {p.error && <Button size="sm" variant="ghost" onClick={() => void store.api.manageGroup(p.change).catch(() => {})}>Try group change again</Button>}
  </div>;
}

export function GroupChanges({ conv }: { conv?: string }) {
  const store = useApp(), changes = useStore(store, s => s.groupChanges);
  const shown = Object.values(changes || {}).filter(p => !conv || p.change.conv === conv);
  if (!shown.length) return null;
  return <section aria-label="Group changes" className="space-y-2 p-3">{shown.map(p => <GroupChangeCard key={p.change.conv} p={p} />)}</section>;
}

/** MemberMenu: an admin's changes for one member (make or remove admin, remove). */
export function MemberMenu({ t, member, me, onPick }: { t: T.DMThread; member: T.GroupMemberView; me: boolean; onPick: (a: GroupAction, m: T.GroupMemberView) => void }) {
  const portal = usePortal();
  const name = me ? "you" : personName(member);
  const item = "flex min-h-11 cursor-pointer items-center gap-3 rounded-xl px-3 text-[15px] font-medium outline-none data-[highlighted]:bg-sunken";
  if (me && !member.admin) return null;
  return (
    <Menu.Root modal={false}>
      <Menu.Trigger aria-label={"Change " + name + " in " + (t.title || "this group")} title="Change"
        className="grid size-11 shrink-0 place-items-center rounded-full text-text-2 hover:bg-sunken hover:text-ink data-[popup-open]:bg-sunken">
        <IconDotsVertical size={20} />
      </Menu.Trigger>
      <Menu.Portal container={portal}>
        <Menu.Positioner side="bottom" align="end" sideOffset={6} collisionPadding={12} className="z-50">
          <Menu.Popup className="min-w-56 rounded-2xl bg-surface p-1.5 text-ink outline-none stroke shadow-pop transition-[opacity,scale] duration-150 data-[ending-style]:opacity-0 motion-reduce:transition-opacity">
            {member.admin
              ? <Menu.Item className={item} onClick={() => onPick("demote", member)}><IconCrown size={20} />Remove admin role…</Menu.Item>
              : <Menu.Item className={item} onClick={() => onPick("promote", member)}><IconCrown size={20} />Make admin…</Menu.Item>}
            {!me && <Menu.Item className={item + " text-danger"} onClick={() => onPick("remove", member)}><IconUserMinus size={20} />Remove from group…</Menu.Item>}
          </Menu.Popup>
        </Menu.Positioner>
      </Menu.Portal>
    </Menu.Root>
  );
}

/** GroupFooter: "Rename group…" for an admin and "Leave group…" for anyone in it. */
export function GroupFooter({ admin, onPick }: { admin: boolean; onPick: (a: GroupAction) => void }) {
  return (
    <div className="mt-2.5 flex flex-wrap gap-2">
      {admin && <Button size="sm" variant="outline" icon={<IconPencil size={18} />} onClick={() => onPick("rename")}>Rename group…</Button>}
      <Button size="sm" variant="danger" icon={<IconDoorExit size={18} />} onClick={() => onPick("leave")}>Leave group…</Button>
    </div>
  );
}

/** useGroupChange: one change sheet at a time, opened from a menu or a button. */
export function useGroupChange(t: T.DMThread | null) {
  const [change, setChange] = useState<{ action: GroupAction; member?: T.GroupMemberView; open: boolean } | null>(null);
  const pick = (action: GroupAction, member?: T.GroupMemberView) => setChange({ action, member, open: true });
  const sheet = change && t ? (
    <GroupChangeSheet open={change.open} onOpenChange={(o) => setChange((c) => (c ? { ...c, open: o } : c))} t={t} action={change.action} member={change.member} />
  ) : null;
  return { pick, sheet };
}
