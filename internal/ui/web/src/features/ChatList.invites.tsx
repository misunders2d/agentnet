// Group invitations for this person, at the top of the chat list. Saying
// yes records consent; membership follows when the group's admin adds you,
// so an accepted invitation stays here, quietly, until the group arrives.
import { useState } from "react";
import { IconChevronDown, IconHourglassHigh, IconUsersGroup } from "@tabler/icons-react";
import { useApp } from "../context";
import { useStore } from "../store";
import type { T } from "../api";
import { personName } from "../model";
import { Button } from "../ui/Button";

/** inviterName turns the inviting device's address into its person's name. */
function inviterName(o: T.Overview | null, address: string): string {
  const p = (o?.people || []).find((x) => x.address === address || (x.devices || []).some((d) => d.address === address));
  return p ? personName(p) : "Someone";
}

export function GroupInvitations() {
  const store = useApp();
  const invitations = useStore(store, (s) => s.invitations);
  const overview = useStore(store, (s) => s.overview);
  const rooms = new Set((overview?.dms || []).map((d) => d.id));
  const pending = invitations.filter((i) => i.direction === "in" && i.status === "pending");
  const accepted = invitations.filter((i) => i.direction === "in" && i.status === "accepted" && !rooms.has(i.conv)
    && !pending.some((p) => p.conv === i.conv));
  const [all, setAll] = useState(false);
  if (!pending.length && !accepted.length) return null;
  // One invitation is shown whole; more wait behind one line, so the chats stay in view.
  const shown = all ? pending : pending.slice(0, 1);
  const hidden = pending.length - shown.length;
  return (
    <section aria-label="Group invitations" className="flex flex-col gap-3 px-4 pt-4 lg:px-3">
      {shown.map((i) => <InvitationCard key={i.id} invitation={i} from={inviterName(overview, i.inviter)} />)}
      {hidden > 0 && (
        <button type="button" onClick={() => setAll(true)}
          className="-mt-1 flex min-h-11 items-center justify-center gap-1.5 rounded-full text-[14px] font-bold text-text-2 hover:bg-sunken">
          {hidden === 1 ? "1 more invitation" : hidden + " more invitations"}<IconChevronDown size={16} stroke={2.4} aria-hidden="true" />
        </button>
      )}
      {accepted.map((i) => (
        <p key={i.id} className="flex items-center gap-3 rounded-2xl border-[1.5px] border-dashed border-hairline px-3 py-2.5 text-[14px] text-text-2 lg:border">
          <IconHourglassHigh size={20} aria-hidden="true" className="shrink-0 text-muted" />
          <span>You said yes to <b className="font-bold text-ink">{i.title}</b>. You’re in once {inviterName(overview, i.inviter)} adds you.</span>
        </p>
      ))}
    </section>
  );
}

function InvitationCard({ invitation: i, from }: { invitation: T.GroupInvitationView; from: string }) {
  const store = useApp();
  const [busy, setBusy] = useState(false);
  const shared = (i.history || []).length;
  const decide = async (accept: boolean) => {
    setBusy(true);
    await store.run((a) => a.decideGroup(i.id, accept), accept ? "You said yes. You’re in once " + from + " adds you." : "Invitation declined.");
    setBusy(false);
  };
  return (
    <article className="pop-in rounded-2xl bg-surface p-3.5 stroke">
      <div className="flex items-start gap-3">
        <span aria-hidden="true" className="grid size-11 shrink-0 -rotate-3 place-items-center rounded-2xl bg-sunken text-ink stroke"><IconUsersGroup size={22} stroke={2} /></span>
        <div className="min-w-0 flex-1">
          <p className="text-[13px] font-semibold text-text-2">{from} invited you to a group</p>
          <h3 className="truncate font-display text-[18px] font-bold leading-tight">{i.title || "Untitled group"}</h3>
          <p className="mt-0.5 text-[13px] text-muted">
            {shared ? (shared === 1 ? "You’ll see 1 earlier message." : "You’ll see " + shared + " earlier messages.") : "You’ll see what’s said after you join."}
          </p>
        </div>
      </div>
      <div className="mt-3 flex gap-2">
        <Button variant="act" size="sm" onClick={() => decide(true)} disabled={busy} className="flex-1">Join</Button>
        <Button variant="ghost" size="sm" onClick={() => decide(false)} disabled={busy} className="flex-1 stroke">No thanks</Button>
      </div>
    </article>
  );
}
