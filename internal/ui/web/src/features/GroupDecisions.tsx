// One local invitation choice follows the person between Chats and Review.
import { useApp } from "../context";
import { useStore } from "../store";
import { Button } from "../ui/Button";

export function GroupDecisions() {
  const store = useApp();
  const pending = useStore(store, s => s.groupDecisions);
  if (!pending || !Object.keys(pending).length) return null;
  return <section aria-label="Pending group decisions" className="space-y-2">
    {Object.values(pending).map(p => <div key={p.invitation.id} role="status" className="rounded-xl border border-hairline bg-surface p-3 text-sm">
      <p>{p.accept ? "You chose to join " : "You declined "}“{p.invitation.title || "Untitled group"}”.</p>
      <p className="mt-1 text-muted">{p.saved ? "Decision saved; checking current state…" : "Saving your choice…"}{p.accept && " Membership still needs the group’s confirmation."}</p>
      {p.saved && <Button variant="ghost" size="sm" onClick={() => void store.refetch()}>Check group status</Button>}
    </div>)}
  </section>;
}
