// Group admin (MEL-528): rename the group, make or remove an admin, remove
// a member, and leave. Each is one signed group change (POST
// /api/groups/manage); the server checks who may make it, and these
// controls only show where it would. Nothing here recalls what anyone
// already saved, and no role grants anyone tasks for an agent.
import { useEffect, useState, type ReactNode } from "react";
import { Menu } from "@base-ui/react/menu";
import { IconCrown, IconDoorExit, IconDotsVertical, IconPencil, IconUserMinus } from "@tabler/icons-react";
import { errorText, type T } from "../api";
import { useApp } from "../context";
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
  const [busy, setBusy] = useState(false);
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

  const run = async () => {
    const name = title.trim();
    if (action === "rename" && !name) { setError("Give the group a name."); return; }
    setBusy(true);
    setError("");
    try {
      const r = await store.api.manageGroup({ conv: t.id, action, ...(member?.person ? { person: member.person } : {}), ...(action === "rename" ? { title: name } : {}) });
      onOpenChange(false);
      if (action === "leave") {
        store.setPanel(false);
        store.close();
        store.toast(r.queued ? "You left. The others are told when it reaches them; they haven’t confirmed yet." : "You left the group.", "ok");
      } else {
        store.toast({ rename: "Renamed to “" + name + "”", promote: who + " is now an admin", demote: who + " is no longer an admin", remove: who + " was removed from the group" }[action], "ok");
      }
      void store.refetch();
    } catch (e) {
      setError(errorText(e));
    }
    setBusy(false);
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
          <Menu.Popup className="min-w-56 rounded-2xl bg-surface p-1.5 text-ink outline-none stroke shadow-pop transition-[opacity,scale] duration-150 data-[starting-style]:scale-95 data-[starting-style]:opacity-0 data-[ending-style]:opacity-0 motion-reduce:transition-opacity">
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
