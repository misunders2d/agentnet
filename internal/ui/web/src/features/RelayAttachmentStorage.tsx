import { useCallback, useEffect, useRef, useState } from "react";
import { errorText } from "../api";
import type { Host } from "../host";
import { Button } from "../ui/Button";
import { Sheet } from "../ui/Sheet";
import { canUseStorageAction, readRelayStorage, storageBytes, storageFiles, storageTitle, type RelayStorageAction, type RelayStorageStatus } from "./RelayAttachmentStorage.model";

const endpoint = "/api/storage/attachments";
const active = () => true;

/** All calls remain bound to the captured workspace host. No Google transport,
 *  tokens, per-user consent, polling or direct fetch lives in this component. */
export function RelayAttachmentStorage({ host, isActive = active }: { host: Host; isActive?: () => boolean }) {
  const [status, setStatus] = useState<RelayStorageStatus | null>(null);
  const [loading, setLoading] = useState(true), [busy, setBusy] = useState<RelayStorageAction | null>(null);
  const [error, setError] = useState(""), [consent, setConsent] = useState("");
  const [confirm, setConfirm] = useState<"move" | "disconnect" | null>(null);
  const scope = useRef(0), reads = useRef(0), acting = useRef(false), queuedRefresh = useRef(false);
  const known = useRef<RelayStorageStatus | null>(null);

  const refresh = useCallback(async (background = false) => {
    if (!isActive()) return;
    if (acting.current) { queuedRefresh.current = true; return; }
    const owner = scope.current, request = ++reads.current;
    const current = () => owner === scope.current && request === reads.current && isActive();
    if (!background || !known.current) setLoading(true);
    try {
      const next = readRelayStorage(await host.api(endpoint));
      if (!current()) return;
      known.current = next; setStatus(next); setError("");
      if (!next.can_admin || !next.configured || next.state === "connected" || next.state === "migrating") setConsent("");
    } catch (e) { if (current()) setError(errorText(e)); }
    finally { if (current()) setLoading(false); }
  }, [host, isActive]);

  useEffect(() => {
    scope.current++; reads.current++; acting.current = false; queuedRefresh.current = false; known.current = null;
    setStatus(null); setBusy(null); setConfirm(null); setConsent(""); setError("");
    void refresh();
    const stop = host.listen(e => { if (e.type === "restart" || (e.type === "change" && !e.hint)) void refresh(true); });
    const visible = host.android?.onVisibility(() => { if (host.android?.visible()) void refresh(true); });
    return () => { scope.current++; reads.current++; stop(); visible?.(); };
  }, [host, refresh]);

  const run = async (action: RelayStorageAction) => {
    const before = known.current;
    if (acting.current || !isActive() || !before || !canUseStorageAction(before, action)) return;
    const owner = scope.current;
    const current = () => owner === scope.current && isActive();
    acting.current = true; reads.current++; setBusy(action); setLoading(false); setError(""); setConfirm(null);
    if (action === "connect") setConsent("");
    try {
      const next = readRelayStorage(await host.api(endpoint, { action, ...(action === "connect" ? {} : { confirm: true }) }), action === "connect");
      if (!current()) return;
      known.current = next; setStatus(next);
      setConsent(action === "connect" ? next.consent_url || "" : "");
      if (action === "connect" && !next.consent_url && next.state !== "connected") setError("Google consent did not start. Refresh and check the relay’s Google setup.");
    } catch (e) { if (current()) setError(errorText(e)); }
    finally {
      if (current()) {
        acting.current = false; setBusy(null);
        if (queuedRefresh.current) { queuedRefresh.current = false; void refresh(true); }
      }
    }
  };

  const pending = status?.pending;
  const moveAmount = status?.local;
  const locked = loading || busy !== null;
  const migrationBusy = !!status && (status.migration.running || status.state === "migrating");
  const disconnectBlocked = !!status && (status.remote_only_files > 0 || status.remote.files > 0 || migrationBusy);
  return (
    <section aria-labelledby="relay-attachments" className="mb-5 min-w-0 space-y-3 rounded-2xl stroke bg-surface p-4 [overflow-wrap:anywhere]" data-part="relay-attachment-storage">
      <div>
        <h3 id="relay-attachments" className="font-display text-[20px] font-bold">Workspace attachments</h3>
        <p className="mt-1 text-[15px] text-text-2">One workspace administrator connects Google Drive for this relay. Members do not connect their own Google accounts.</p>
      </div>
      {status ? <>
        <p role="status" className="font-semibold">{storageTitle(status, !!consent || busy === "connect")}</p>
        <p className="text-[15px] text-text-2">{status.connected
          ? "All new attachments handled by this relay use its connected Google Drive. Messages and attachments stay end-to-end encrypted."
          : "Attachments stay end-to-end encrypted. Connecting this relay’s Google Drive does not share plaintext with Google."}</p>
        {status.state === "not_configured" && <p className="text-[15px] text-text-2">The relay operator needs to configure Google access before an administrator can connect it.</p>}
        {status.state === "offline" && <p className="text-[15px] text-text-2">Drive transfers need a working connection. Local copies remain until remote storage has been verified.</p>}
        {status.state === "needs_reconnect" && <p className="text-[15px] text-text-2">An administrator needs to reconnect the same Google account so existing attachments remain accessible.</p>}
        {status.can_admin && <><dl className="grid grid-cols-2 gap-3 rounded-xl bg-sunken p-3">
          <Amount name="Local relay copies" amount={status.local} />
          <Amount name="In Google Drive" amount={status.remote} />
        </dl>
        <p className="text-[13px] text-muted">Local and Drive totals can overlap while local copies remain. This device’s files have separate storage totals.</p></>}
        {status.can_admin && status.account && <p className="text-[14px] text-text-2">Connected account: <strong>{status.account.email || status.account.name}</strong>{status.folder?.name && <> · Folder: {status.folder.name}</>}</p>}
        {status.can_admin && !!pending?.files && <p className="text-[15px] text-text-2">Not yet in Drive: <strong className="tnum">{storageFiles(pending.files)} · {storageBytes(pending.bytes)}</strong>.</p>}
        {status.can_admin && (status.migration.running || status.migration.total_files > 0) && <div className="space-y-1.5 rounded-xl bg-sunken p-3">
          <p className="font-semibold">{status.migration.running ? "Moving existing attachments" : "Last move"}</p>
          <progress aria-label="Existing attachment migration" className="block h-3 w-full accent-agent-ink" max={status.migration.total_bytes || status.migration.total_files || 1}
            value={Math.min(status.migration.total_bytes ? status.migration.bytes : status.migration.files, status.migration.total_bytes || status.migration.total_files || 1)} />
          <p className="text-[14px] tnum">{storageFiles(status.migration.files)} of {storageFiles(status.migration.total_files)} verified · {storageBytes(status.migration.bytes)} of {storageBytes(status.migration.total_bytes)}</p>
          <p className="text-[13px] text-muted">A local copy is removed only after its Drive copy is verified and recorded. Interrupted work can resume.</p>
        </div>}
        {status.can_admin && status.notice && <p className="text-[14px] text-text-2">{status.notice}</p>}
        {consent && status.can_admin && <div className="space-y-2 rounded-xl bg-sunken p-3">
          <p className="text-[15px]">Continue in your browser, choose the relay’s Google account, then return and refresh this status.</p>
          <a href={consent} target={host.platform === "android" ? "_self" : "_blank"} rel="noopener noreferrer"
            className="inline-flex min-h-11 items-center rounded-full stroke bg-surface px-4 font-semibold underline hover:bg-sunken focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ink">Continue to Google</a>
          <Button size="sm" variant="ghost" disabled={locked || !canUseStorageAction(status, "connect")} onClick={() => void run("connect")}>Start again</Button>
        </div>}
        {error && <p role="alert" className="text-[15px] text-danger">{error}</p>}
        {status.can_admin && <div className="flex flex-wrap gap-2">
          {(!status.connected || status.state === "needs_reconnect") && <Button disabled={locked || !!consent || !canUseStorageAction(status, "connect")} onClick={() => void run("connect")}>{busy === "connect" ? "Starting connection…" : status.state === "needs_reconnect" ? "Reconnect Google Drive" : "Connect Google Drive"}</Button>}
          {status.connected && <>
            <Button disabled={locked || !canUseStorageAction(status, "move")} onClick={() => setConfirm("move")}>{busy === "move" ? "Starting move…" : status.state === "offline" ? "Retry moving attachments…" : "Move existing attachments…"}</Button>
            <Button variant="ghost" disabled={locked || disconnectBlocked} aria-describedby={disconnectBlocked ? "relay-disconnect-reason" : undefined} onClick={() => setConfirm("disconnect")}>Disconnect Drive…</Button>
          </>}
        </div>}
        {status.can_admin && disconnectBlocked && <p id="relay-disconnect-reason" className="text-[13px] text-muted">{migrationBusy
          ? "Disconnect is unavailable while attachments are being moved."
          : status.remote_only_files > 0
            ? `Disconnect is unavailable: ${storageFiles(status.remote_only_files)} ${status.remote_only_files === 1 ? "has its" : "have their"} only relay copy in Drive. Keep the account connected so ${status.remote_only_files === 1 ? "it stays" : "they stay"} accessible.`
            : `Disconnect is unavailable: ${storageFiles(status.remote.files)} retained in Drive still need this account for access or cleanup, even when local copies exist.`}</p>}
      </> : loading ? <p role="status" className="text-text-2">Reading attachment storage…</p> : null}
      {!status && error && <p role="alert" className="text-[15px] text-danger">{error}</p>}
      <Button size="sm" variant="ghost" disabled={locked} onClick={() => void refresh()}>{loading ? "Refreshing…" : "Refresh storage status"}</Button>
      <Sheet open={confirm !== null} onOpenChange={open => { if (!open) setConfirm(null); }}
        title={confirm === "disconnect" ? "Disconnect this relay’s Drive?" : "Move existing relay attachments?"}
        description={confirm === "disconnect" ? "This changes attachment storage for everyone in this workspace." : "Move encrypted copies stored on this relay, without changing chats or attachment references."}
        footer={<div className="flex flex-wrap justify-end gap-2">
          <Button onClick={() => setConfirm(null)}>Cancel</Button>
          <Button variant={confirm === "disconnect" ? "danger" : "act"} disabled={locked || !status || !confirm || !canUseStorageAction(status, confirm)} onClick={() => { if (confirm) void run(confirm); }}>{confirm === "disconnect" ? "Disconnect Drive" : "Move attachments"}</Button>
        </div>}>
        {confirm === "move" ? <div className="space-y-3">
          <p>This moves up to <strong>{storageFiles(moveAmount?.files || 0)} · {storageBytes(moveAmount?.bytes || 0)}</strong> to the relay’s connected Google Drive. The relay selects complete attachments that need moving.</p>
          <p>Only encrypted attachment data is uploaded. Local copies are removed after remote verification and a saved storage record. If interrupted, the move can resume.</p>
          <p>Your own device downloads and files saved outside AgentNet are unaffected.</p>
        </div> : <p>New attachments will use local relay storage. Disconnect is blocked while Drive copies or transfer records still need this account for access or cleanup.</p>}
      </Sheet>
    </section>
  );
}

function Amount({ name, amount }: { name: string; amount: { files: number; bytes: number } }) {
  return <div className="min-w-0"><dt className="text-[13px] font-semibold text-text-2">{name}</dt><dd className="mt-1 font-semibold tnum">{storageBytes(amount.bytes)}<span className="block text-[13px] font-normal text-muted">{storageFiles(amount.files)}</span></dd></div>;
}
