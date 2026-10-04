// Storage (what AgentNet holds here and on your server, as the program
// reports it: unknown stays unknown), workspaces (the memberships this
// computer holds) and About (version, updates, technical details).
import { IconArrowUpRight, IconPlugConnected, IconPlus, IconRefresh } from "@tabler/icons-react";
import { useEffect, useReducer, useRef, useState } from "react";
import type { T } from "../api";
import { useApp } from "../context";
import type { Workspace } from "../host";
import { hue, initials } from "../model";
import { useStore } from "../store";
import { AgentAvatar } from "../ui/Avatar";
import { Button } from "../ui/Button";
import { Tag } from "../ui/Tag";
import { harnessName } from "./Settings.assistant";
import { JoinSheet, LeaveSheet, useDisconnected, useReconnect, workspaceLabel } from "./WorkspaceSwitcher";
import { Card, Details, Fact, Failed, GroupLabel, Hint, PageHead, Skeleton, useLoad } from "./Settings.parts";

// ---- Storage -----------------------------------------------------------------

const bytes = (n: number) => n < 1024 ? n + " B" : n < 1 << 20 ? (n / 1024).toFixed(1) + " KB" : n < 1 << 30 ? (n / (1 << 20)).toFixed(1) + " MB" : (n / (1 << 30)).toFixed(2) + " GB";
const files = (n: number) => n === 1 ? "1 file" : n + " files";
const days = (sec: number) => { const d = Math.round(sec / 86400); return d === 1 ? "1 day" : d + " days"; };

export function StorageSection({ titleRef }: { titleRef?: React.Ref<HTMLHeadingElement> }) {
  const store = useApp();
  const r = useLoad(() => store.api.storage(), []);
  const head = <PageHead title="Storage" titleRef={titleRef} lead="What AgentNet keeps, as it reports it. Nothing here deletes anything." />;
  if (r.error && !r.data) return <>{head}<Failed text={/not found|404/i.test(r.error) ? "This AgentNet doesn’t report storage yet." : "Storage couldn’t be read just now."} retry={r.reload} /></>;
  if (!r.data) return <>{head}<Skeleton lines={5} /></>;
  const { local, remote } = r.data;
  const here = local.scope === "browser-device-records" ? "this browser" : "this computer";
  const own = remote.status === "available" && remote.usage ? remote.usage.own.stored : null;
  return (
    <>
      {head}
      <div className="space-y-6">
        <div className="grid grid-cols-2 gap-2.5">
          <Total label={"On " + here} value={bytes(local.known.bytes)} sub={(local.complete ? "" : "at least ") + files(local.known.files)} />
          <Total label="On your server" value={own ? bytes(own.reserved_bytes) : "Unknown"} sub={own ? files(own.files) + ", encrypted" : "It didn’t say"} />
        </div>

        <section aria-labelledby="storage-here">
          <GroupLabel id="storage-here">{"On " + here}</GroupLabel>
          <Card className="divide-y divide-hairline">
            {(local.areas || []).map((a) => (
              <div key={a.directory} className={"px-4 py-3 " + (a.status === "available" ? "" : "opacity-75")}>
                <div className="flex items-start gap-3">
                  <div className="min-w-0 flex-1">
                    <p className="font-semibold">{a.label}</p>
                    <div className="mt-0.5"><Tag tone={a.kind === "plaintext" ? "muted" : "ok"}>{a.kind === "plaintext" ? "Readable files" : "Encrypted"}</Tag></div>
                  </div>
                  <p className="shrink-0 text-right text-[15px] font-semibold tnum">{a.usage ? bytes(a.usage.bytes) : "Unknown"}{a.usage && <span className="block text-[13px] font-normal text-muted">{files(a.usage.files)}</span>}</p>
                </div>
                {a.reason && <Hint className="mt-1">{a.reason}</Hint>}
              </div>
            ))}
          </Card>
          <Details label="Where and how long" className="px-1">
            {(local.areas || []).map((a) => (
              <div key={a.directory} className="rounded-xl bg-sunken p-3">
                <p className="font-semibold text-ink">{a.label}</p>
                <Fact name="Folder">{a.directory}</Fact>
                <p className="pt-1">{a.lifetime}</p>
              </div>
            ))}
          </Details>
          {!local.complete && <Hint className="mt-2 px-1">Some of it couldn’t be looked at, so the total is a lower bound.</Hint>}
          {local.exclusions && <Hint className="mt-2 px-1">{local.exclusions}</Hint>}
        </section>

        <section aria-labelledby="storage-server">
          <GroupLabel id="storage-server">On your server</GroupLabel>
          <Remote r={remote} />
        </section>
        <Button variant="outline" icon={<IconRefresh size={18} />} disabled={r.loading} onClick={r.reload}>{r.loading ? "Reading…" : "Read again"}</Button>
      </div>
    </>
  );
}

function Total({ label, value, sub }: { label: string; value: string; sub: string }) {
  return (
    <Card className="p-4">
      <p className="text-[13px] font-semibold text-muted">{label}</p>
      <p className="font-display text-[28px] font-extrabold leading-tight tnum @md:text-[34px]">{value}</p>
      <p className="text-[13px] text-text-2 tnum">{sub}</p>
    </Card>
  );
}

function Remote({ r }: { r: T.RemoteStorage }) {
  if (r.status !== "available" || !r.usage) {
    return <Card className="p-4"><p>{r.reason || (r.status === "unsupported" ? "Your server doesn’t report storage." : "Not known right now: your server didn’t answer.")}</p></Card>;
  }
  const u = r.usage, inc = u.own.incomplete, pol = u.policy;
  return (
    <Card className="space-y-3 p-4">
      <p>Your encrypted files there: <strong className="tnum">{files(u.own.stored.files)} · {bytes(u.own.stored.reserved_bytes)}</strong>{inc.files > 0 && <>, plus {files(inc.files)} still uploading</>}. The server holds them encrypted.</p>
      <p>The server’s limit is <strong className="tnum">{bytes(u.quota_bytes)}</strong> for everyone together, so it isn’t your own allowance. Largest file: <span className="tnum">{bytes(u.max_file_bytes)}</span>.</p>
      {u.global && <p>Everyone on this server: <span className="tnum">{files(u.global.stored.files)} · {bytes(u.global.stored.reserved_bytes)}</span>. You see this because you’re an admin.</p>}
      <Details label="How long the server keeps things">
        <Fact name="Delivered files">{pol.delivered_attachments}</Fact>
        <Fact name="Undelivered">{pol.undelivered_attachments}</Fact>
        <Fact name="Never attached">{pol.unattached_attachments}</Fact>
        <Fact name="Messages">{pol.message_envelopes}</Fact>
        <Fact name="Unfinished">{pol.incomplete_uploads}{u.upload_idle_ttl_seconds ? " (idle limit " + Math.round(u.upload_idle_ttl_seconds / 3600) + " h)" : ""}</Fact>
        <Fact name="Cleanup">{"Only when the operator runs it: delivered files older than " + days(pol.manual_delivered_age_default_seconds) + ", never-attached older than " + days(pol.manual_unattached_age_default_seconds) + "."}</Fact>
        <Fact name="Where">{u.location}</Fact>
      </Details>
    </Card>
  );
}

// ---- Workspaces ----------------------------------------------------------------

const COINS = ["#FFB4A2", "#B5E3C4", "#A8D8FF", "#FFD6A5", "#D9C2FF", "#FFC6E0", "#C7F0E8", "#F6E3A1"];
const hostOf = (endpoint: string) => { try { return new URL(endpoint).host; } catch { return endpoint; } };

// Coin stands in until WorkspaceSwitcher exports its coin; joining and
// leaving use WorkspaceSwitcher's own sheets.
function Coin({ ws }: { ws: Workspace }) {
  return (
    <span aria-hidden="true" className="grid size-11 shrink-0 place-items-center rounded-xl stroke font-display text-[17px] font-extrabold text-[#1B1530]"
      style={{ background: COINS[hue(ws.realm || ws.id)] }}>{initials(workspaceLabel(ws))}</span>
  );
}

export function WorkspacesSection({ titleRef }: { titleRef?: React.Ref<HTMLHeadingElement> }) {
  const store = useApp();
  const ws = store.host.workspaces;
  const [revision, refresh] = useReducer((n: number) => n + 1, 0);
  const [joining, setJoining] = useState(false);
  const [leaving, setLeaving] = useState<Workspace | null>(null);
  useEffect(() => ws?.onChange(refresh), [ws]);
  const gone = useDisconnected(ws, revision);
  const { busy, error, done, reconnect } = useReconnect(ws, refresh);
  // Focus follows the reconnect (its button was disabled while it ran): to
  // the workspace back in the list, or back to the button when it failed.
  const box = useRef<HTMLDivElement>(null);
  const focusOn = useRef("");
  useEffect(() => { focusOn.current = done || error?.id || ""; }, [done, error]);
  useEffect(() => { // once, as soon as that row is drawn
    if (!focusOn.current || !box.current) return;
    const row = box.current.querySelector<HTMLElement>("[data-ws=\"" + CSS.escape(focusOn.current) + "\"]");
    if (!row) return;
    focusOn.current = "";
    (row.querySelector<HTMLElement>("button:not([disabled])") || row).focus();
  });
  const head = <PageHead title="Workspaces" titleRef={titleRef} lead="Each workspace is its own server, with its own people and chats. You switch between them here." />;
  if (!ws) return (
    <>{head}
      <Card className="flex items-center gap-3 p-4"><Coin ws={store.host.workspace} /><div><p className="font-semibold">{workspaceLabel(store.host.workspace)}</p><Hint>The only workspace on this {store.host.platform === "browser" ? "browser" : "computer"}.</Hint></div></Card>
    </>
  );
  const list = ws.list(), active = ws.active();
  const filesWaiting = Object.values(store.get().drafts).some((d) => d.files && d.files.length);
  const choose = (id: string) => {
    if (filesWaiting) { store.toast("Send or remove the files waiting in a message first: switching can’t keep them.", "error"); return; }
    ws.select(id);
  };
  return (
    <div ref={box}>
      {head}
      <ul className="space-y-3">
        {list.map((w) => (
          <li key={w.id} data-ws={done === w.id ? w.id : undefined} tabIndex={done === w.id ? -1 : undefined} className="rounded-2xl">
            <Card className="px-4 pb-1 pt-4">
              <div className="flex items-center gap-3">
                <Coin ws={w} />
                <div className="min-w-0 flex-1">
                  <p className="flex flex-wrap items-center gap-2 font-semibold">{workspaceLabel(w)}{w.id === active && <Tag tone="ok">In use</Tag>}</p>
                  <Hint className="truncate">{hostOf(w.endpoint)}</Hint>
                </div>
                {w.id !== active && ws.has(w.id) && <Button size="sm" onClick={() => choose(w.id)}>Switch</Button>}
              </div>
              <div className="flex flex-wrap items-center justify-between gap-2">
                <Details>
                  <Fact name="You are">{w.address}</Fact>
                  <Fact name="Server">{w.endpoint}</Fact>
                </Details>
                {w.id !== "default" && ws.disconnect && <Button size="sm" variant="ghost" className="text-danger" aria-label={"Leave " + workspaceLabel(w)} onClick={() => setLeaving(w)}>Leave</Button>}
              </div>
            </Card>
          </li>
        ))}
      </ul>
      {gone.list.length > 0 && <>
        <div className="mt-6"><GroupLabel>Not connected</GroupLabel></div>
        <Hint className="-mt-1 mb-3">You left these on this {store.host.platform === "browser" ? "browser" : "computer"}. Their keys and chats are still here; reconnecting brings them back as the same device.</Hint>
        <ul className="space-y-3">
          {gone.list.map((w) => (
            <li key={w.id} data-ws={done === w.id ? undefined : w.id}>
              <Card className="px-4 pb-1 pt-4">
                <div className="flex items-center gap-3">
                  <span className="opacity-60"><Coin ws={w} /></span>
                  <div className="min-w-0 flex-1">
                    <p className="font-semibold">{workspaceLabel(w)}</p>
                    <Hint className="truncate">{hostOf(w.endpoint)}</Hint>
                  </div>
                  <Button size="sm" variant="outline" icon={<IconPlugConnected size={18} aria-hidden="true" />} disabled={!!busy} aria-label={"Reconnect " + workspaceLabel(w)} onClick={() => reconnect(w)}>{busy === w.id ? "Reconnecting…" : "Reconnect"}</Button>
                </div>
                {error?.id === w.id && <p role="alert" className="pt-2 text-[14px] font-semibold text-danger">Couldn’t reconnect: {error.text}</p>}
                <Details>
                  <Fact name="You were">{w.address}</Fact>
                  <Fact name="Server">{w.endpoint}</Fact>
                </Details>
              </Card>
            </li>
          ))}
        </ul>
      </>}
      {gone.error && <p role="alert" className="mt-3 text-[14px] font-semibold text-danger">Couldn’t list the workspaces you left: {gone.error}</p>}
      {ws.join && <Button variant="outline" size="lg" className="mt-5" icon={<IconPlus size={20} />} onClick={() => setJoining(true)}>Join a workspace</Button>}
      <JoinSheet ws={ws} open={joining} onClose={() => setJoining(false)} onJoined={refresh} />
      <LeaveSheet ws={ws} target={leaving} onClose={() => setLeaving(null)} onLeft={refresh} />
    </div>
  );
}

// ---- About -------------------------------------------------------------------

export function AboutSection({ titleRef }: { titleRef?: React.Ref<HTMLHeadingElement> }) {
  const store = useApp();
  const o = useStore(store, (s) => s.overview);
  const [paths, setPaths] = useState<T.HarnessView[] | null>(null);
  const loadPaths = () => { if (!paths && store.host.platform !== "browser") store.api.responder().then((r) => setPaths(r.harnesses || []), () => setPaths([])); };
  return (
    <>
      <PageHead title="About" titleRef={titleRef} />
      <div className="space-y-4">
        <Card className="flex items-center gap-4 p-4">
          <AgentAvatar seed="agentnet" size={72} mood="done" />
          <div className="min-w-0">
            <p className="font-display text-[28px] font-extrabold leading-tight">AgentNet</p>
            <p className="text-text-2">Version <span className="font-semibold tnum">{o?.version || "…"}</span></p>
            {o && !o.release && <Hint className="mt-0.5">Your server doesn’t recommend a newer one.</Hint>}
          </div>
        </Card>
        {o?.release ? (
          <Card tone="agent" className="p-4">
            <p className="font-display text-[20px] font-bold leading-tight">Version {o.release} is out</p>
            <p className="mt-1 text-[15px] text-text-2">Your server recommends it. Updates aren’t installed from this page: in a terminal on this computer, run <code className="rounded-md bg-surface px-1.5 py-0.5 font-mono text-[13px] stroke">agentnet update</code>.</p>
            <a href="https://github.com/misunders2d/agentnet/releases" target="_blank" rel="noopener noreferrer"
              className="mt-3 inline-flex min-h-11 items-center gap-1.5 rounded-full stroke bg-surface px-4 font-semibold hover:bg-sunken">What’s new<IconArrowUpRight size={18} aria-hidden="true" /></a>
          </Card>
        ) : null}
        {o && (
          <Card className="px-4 py-1.5">
            <Details label="Technical details" onOpen={loadPaths} className="pb-2">
              <Fact name="Address">{o.me.address}</Fact>
              <Fact name="Key">{o.me.fingerprint}</Fact>
              {o.person?.person && <Fact name="Person ID">{o.person.person}</Fact>}
              <Fact name="Workspace">{store.host.workspace.name + " · " + store.host.workspace.endpoint}</Fact>
              <Fact name="Runs as">{store.host.platform === "browser" ? "This browser" : "AgentNet on this computer"}</Fact>
              {o.me.responder && <Fact name="Agent">{harnessName(o.me.responder) + " in " + o.me.responder_dir}</Fact>}
              {(paths || []).filter((h) => h.path).map((h) => <Fact key={h.name} name={harnessName(h.name)}>{h.path}</Fact>)}
            </Details>
          </Card>
        )}
      </div>
    </>
  );
}
