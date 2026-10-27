// A local decision leaves attention before the authoritative roster changes.
import { useApp } from "../context";
import { niceDevice } from "../model";
import { useStore } from "../store";
import { Button } from "../ui/Button";

export function DeviceDecisions() {
  const store = useApp();
  const pending = useStore(store, s => s.deviceDecisions);
  if (!pending || !Object.keys(pending).length) return null;
  return <section aria-label="Pending device decisions" className="space-y-2">
    {Object.values(pending).map(p => <div key={p.link.id} role="status" className="rounded-xl border border-hairline bg-surface p-3 text-sm">
      <p>{p.saved ? "Device decision saved; checking current state…" : (p.accept ? "Approving " : "Refusing ") + niceDevice(p.link.name) + "…"}</p>
      <p className="mt-1 text-muted">Device membership is shown after confirmation.</p>
      {p.saved && <Button variant="ghost" size="sm" onClick={() => void store.refetch()}>Check device status</Button>}
    </div>)}
  </section>;
}
