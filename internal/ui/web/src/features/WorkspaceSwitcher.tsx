// The workspace switcher: which AgentNet server this is, the others this
// computer belongs to, joining one with an invitation, leaving one and
// reconnecting one this computer left (where its program can). The
// host owns the memberships (loader.js); switching remounts the whole
// interface over the chosen one, so nothing here can act on another
// workspace by accident. Hidden when the host has no workspace list.
import { Popover } from "@base-ui/react/popover";
import { useEffect, useRef, useState, type FormEvent } from "react";
import { IconCheck, IconChevronDown, IconDoorExit, IconPencil, IconPlus, IconCircleCheck, IconPlugConnected } from "@tabler/icons-react";
import { useApp } from "../context";
import { useStore } from "../store";
import { errorText, type T } from "../api";
import type { Host, Workspace } from "../host";
import { deviceName } from "../model";
import { Sheet } from "../ui/Sheet";
import { Button, IconButton } from "../ui/Button";
import { usePortal } from "../owned";

type Memberships = NonNullable<Host["workspaces"]>;

const generic = new Set(["", "current workspace", "this computer", "this server"]);

/** The workspace's own name and its relay's host, as the overview says (the current workspace). */
export type OwnName = { name?: string; server?: string } | null | undefined;

/** workspaceLabel, in this order (skinbar.mjs says the same):
 *  - this device's own label, when it is not a placeholder;
 *  - the workspace's own name its admin set (own.name for the current one, hub_name for others);
 *  - its relay's host name (own.server, or the endpoint's);
 *  - "AgentNet", when nothing says anything. */
export function workspaceLabel(w: Workspace, own?: OwnName): string {
  const name = (w.name || "").trim();
  if (!generic.has(name.toLowerCase())) return name;
  const hub = ((own && own.name) || w.hub_name || "").trim();
  return hub || hostName(own?.server) || serverName(w.endpoint) || "AgentNet";
}

/** hostName is a host name to show, or "" for an address that is only numbers. */
function hostName(h?: string): string {
  h = (h || "").replace(/^www\./, "");
  return !h || h === "localhost" || h.startsWith("[") || /^[\d.]+$/.test(h) ? "" : h;
}

/** serverName is a server's host name, or "" for an address that is only numbers. */
function serverName(endpoint: string): string {
  try { return hostName(new URL(endpoint).hostname); } catch { return ""; }
}

/** useWorkspaceLabel names any workspace as every screen does: from a fresh
 *  list entry (a rename here shows at once) and, for the current one, its
 *  overview (the admin's name reaches it with the member list, no reload). */
export function useWorkspaceLabel(): (w?: Workspace) => string {
  const store = useApp();
  const own = useStore(store, (s) => s.overview?.workspace);
  const ws = store.host.workspaces;
  const active = ws ? ws.active() : store.host.workspace.id;
  return (w: Workspace = store.host.workspace) => {
    const fresh = (ws && ws.list().find((x) => x.id === w.id)) || w;
    return workspaceLabel(fresh, fresh.id === active ? own : undefined);
  };
}

/** coinLetters: "Linen HQ" → "LH", "AgentNet" → "AN", "hub.acme.com" → "A". */
function coinLetters(label: string): string {
  const base = label.includes(".") && !label.includes(" ") ? label.split(".").slice(-2, -1)[0] || label : label;
  const words = base.replace(/(\p{Ll})(\p{Lu})/gu, "$1 $2").split(/[^\p{L}\p{N}]+/u).filter(Boolean);
  if (!words.length) return "A";
  return (words[0][0] + (words[1]?.[0] || "")).toUpperCase();
}

function stateWord(w: Workspace, connected: boolean): string {
  if (w.state === "joining") return "Still joining";
  if (w.state === "disconnected" || !connected) return "Not connected";
  return "";
}

/** useDisconnected: the workspaces this computer left and can reconnect, read
 *  from its program each time revision changes (none where it can't). */
export function useDisconnected(ws: Memberships | null | undefined, revision: number) {
  const [state, setState] = useState<{ list: Workspace[]; error: string }>({ list: [], error: "" });
  useEffect(() => {
    if (!ws?.disconnected) return;
    let live = true;
    ws.disconnected().then((list) => { if (live) setState({ list: list || [], error: "" }); })
      .catch((e) => { if (live) setState((s) => ({ list: s.list, error: errorText(e) })); });
    return () => { live = false; };
  }, [ws, revision]);
  return state;
}

/** useReconnect reconnects a workspace this computer left: the same
 *  membership, with its keys and chats, routed again. It is not selected.
 *  busy is the one reconnecting; error says why the last one failed (shown
 *  under its row, where it can be seen); done is the last one reconnected.
 *  Success is also said in a toast, after onDone(w). */
export function useReconnect(ws: Memberships | null | undefined, onDone: (w: Workspace) => void) {
  const store = useApp();
  const [busy, setBusy] = useState("");
  const [error, setError] = useState<{ id: string; text: string } | null>(null);
  const [done, setDone] = useState("");
  const reconnect = async (w: Workspace) => {
    if (!ws?.reconnect) return;
    setBusy(w.id); setError(null); setDone("");
    try {
      await ws.reconnect(w.id);
      setDone(w.id);
      onDone(w);
      store.toast("Reconnected " + workspaceLabel(w) + ". Its chats and keys are back.", "ok");
    } catch (e) {
      setError({ id: w.id, text: errorText(e) });
    }
    setBusy("");
  };
  return { busy, error, done, reconnect };
}

/** WorkspaceCoin: the desktop rail's top coin; it opens the workspace menu. */
export function WorkspaceCoin() {
  return <WorkspaceControl variant="coin" />;
}

/** WorkspacePill: the phone's equivalent, at the top of the chat list. */
export function WorkspacePill() {
  return <WorkspaceControl variant="pill" />;
}

function WorkspaceControl({ variant }: { variant: "coin" | "pill" }) {
  const portal = usePortal();
  const store = useApp();
  const ws = store.host.workspaces;
  const [menu, setMenu] = useState(false);
  const [joining, setJoining] = useState(false);
  const [leaving, setLeaving] = useState<Workspace | null>(null);
  const [renaming, setRenaming] = useState<Workspace | null>(null);
  const [revision, setRevision] = useState(0); // the list changes only through join, leave, rename and reconnect here
  const labelOf = useWorkspaceLabel();
  if (!ws) return null;

  const current = ws.list().find((w) => w.id === ws.active()) || store.host.workspace;
  const label = labelOf(current);
  const letters = coinLetters(label);
  const refresh = () => setRevision((n) => n + 1);
  // A reconnect closes the menu, so its toast shows (a phone's sheet covers toasts).
  const menuBody = (
    <WorkspaceMenu ws={ws} revision={revision} onChanged={refresh} onDone={() => setMenu(false)}
      onJoin={ws.join ? () => { setMenu(false); setJoining(true); } : undefined}
      onRename={ws.rename ? (w) => { setMenu(false); setRenaming(w); } : undefined}
      onLeave={ws.disconnect ? (w) => { setMenu(false); setLeaving(w); } : undefined} />
  );

  return (
    <>
      {variant === "coin" ? (
        <Popover.Root open={menu} onOpenChange={setMenu}>
          <Popover.Trigger aria-label={"Workspace: " + label + ". Switch or join another"} title={label}
            className="mb-2 grid size-12 place-items-center rounded-[15px] border-2 border-white bg-act font-display text-[17px] font-extrabold tracking-tight text-act-ink transition-transform duration-200 ease-out-soft hover:-rotate-6 data-[popup-open]:-rotate-6 motion-reduce:transition-none motion-reduce:hover:rotate-0 motion-reduce:data-[popup-open]:rotate-0">
            {letters}
          </Popover.Trigger>
          <Popover.Portal container={portal}>
            <Popover.Positioner side="right" align="start" sideOffset={14} collisionPadding={12} className="z-40">
              <Popover.Popup className="w-[340px] origin-[var(--transform-origin)] rounded-3xl bg-canvas p-3 text-ink outline-none stroke shadow-pop transition-[opacity,scale] duration-200 ease-out-soft data-[ending-style]:scale-95 data-[ending-style]:opacity-0 data-[starting-style]:scale-95 data-[starting-style]:opacity-0 motion-reduce:duration-100 motion-reduce:data-[ending-style]:scale-100 motion-reduce:data-[starting-style]:scale-100">
                <Popover.Title className="px-2 pt-1 pb-2 font-display text-[22px] font-extrabold leading-tight">Workspaces</Popover.Title>
                {menuBody}
              </Popover.Popup>
            </Popover.Positioner>
          </Popover.Portal>
        </Popover.Root>
      ) : (
        <>
          <button type="button" onClick={() => setMenu(true)} aria-label={"Workspace: " + label + ". Switch or join another"}
            className="flex h-11 min-w-0 max-w-[60vw] items-center gap-2 rounded-xl bg-surface pl-1.5 pr-2.5 stroke shadow-pop-sm press">
            <span aria-hidden="true" className="grid size-[30px] shrink-0 place-items-center rounded-[9px] bg-[#1B1530] font-display text-[13px] font-extrabold text-[#FFD43B]">{letters}</span>
            <span className="truncate text-[15px] font-extrabold">{label}</span>
            <IconChevronDown size={16} stroke={2.4} aria-hidden="true" className="shrink-0" />
          </button>
          <Sheet open={menu} onOpenChange={setMenu} title="Workspaces" description="Each one is a separate server with its own people and chats.">{menuBody}</Sheet>
        </>
      )}
      <JoinSheet open={joining} onClose={() => setJoining(false)} onJoined={refresh} />
      <LeaveSheet target={leaving} onClose={() => setLeaving(null)} onLeft={refresh} />
      <RenameSheet target={renaming} onClose={() => setRenaming(null)} onRenamed={refresh} />
    </>
  );
}

function WorkspaceMenu({ ws, revision, onChanged, onDone, onJoin, onRename, onLeave }: {
  ws: Memberships; revision: number; onChanged: () => void; onDone: () => void; onJoin?: () => void; onRename?: (w: Workspace) => void; onLeave?: (w: Workspace) => void;
}) {
  const store = useApp();
  const labelOf = useWorkspaceLabel();
  const active = ws.active();
  const list = ws.list();
  const gone = useDisconnected(ws, revision);
  const { busy, error, reconnect } = useReconnect(ws, () => { onChanged(); onDone(); });
  // A failed reconnect gives focus back to its button (it was disabled while it ran).
  const buttons = useRef(new Map<string, HTMLButtonElement>());
  useEffect(() => { if (error) buttons.current.get(error.id)?.focus(); }, [error]);
  const pick = (w: Workspace) => {
    onDone();
    if (w.id === active) return;
    try { ws.select(w.id); } catch (e) { store.toast(errorText(e), "error"); }
  };
  return (
    <div>
      <ul className="flex flex-col gap-1">
        {list.map((w) => {
          const here = w.id === active;
          const connected = ws.has(w.id);
          const name = labelOf(w);
          const server = serverName(w.endpoint);
          const state = stateWord(w, connected);
          const line = here ? "You’re here" : state || (server && server !== name ? server : "Switch to it");
          return (
            <li key={w.id} className={"flex items-center gap-1 rounded-2xl " + (here ? "bg-surface stroke" : "border-[1.5px] border-transparent lg:border")}>
              <button type="button" onClick={() => pick(w)} disabled={!here && !connected} aria-current={here ? "true" : undefined}
                className="flex min-h-14 min-w-0 flex-1 items-center gap-3 rounded-2xl px-2 py-2 text-left hover:bg-sunken disabled:opacity-60 aria-[current]:hover:bg-transparent">
                <span aria-hidden="true" className={"grid size-10 shrink-0 place-items-center rounded-xl font-display text-[15px] font-extrabold stroke " + (here ? "bg-act text-act-ink" : "bg-[#1B1530] text-[#FFD43B]")}>{coinLetters(name)}</span>
                <span className="min-w-0 flex-1">
                  <span className="block truncate font-bold">{name}</span>
                  <span className={"block truncate text-[13px] " + (here ? "font-semibold text-ok-ink" : "text-muted")}>{line}</span>
                </span>
                {here && <IconCheck size={20} stroke={2.6} aria-hidden="true" className="mr-1 shrink-0 text-ok-ink" />}
              </button>
              {onRename && connected && (
                <IconButton label={"Rename " + name + "…"} onClick={() => onRename(w)} className="shrink-0 text-muted hover:text-ink"><IconPencil size={20} /></IconButton>
              )}
              {onLeave && w.id !== "default" && (
                <IconButton label={"Leave " + name + "…"} onClick={() => onLeave(w)} className="shrink-0 text-muted hover:text-danger"><IconDoorExit size={20} /></IconButton>
              )}
            </li>
          );
        })}
      </ul>
      {gone.list.length > 0 && (
        <div className="pt-3">
          <h3 className="px-2 pb-1 text-[12px] font-extrabold uppercase tracking-[.08em] text-muted">Not connected</h3>
          <ul className="flex flex-col gap-1">
            {gone.list.map((w) => {
              const name = workspaceLabel(w);
              const server = serverName(w.endpoint);
              return (
                <li key={w.id} className="rounded-2xl px-2 py-2">
                  <div className="flex items-center gap-3">
                    <span aria-hidden="true" className="grid size-10 shrink-0 place-items-center rounded-xl border-[1.5px] border-dashed border-outline font-display text-[15px] font-extrabold text-muted">{coinLetters(name)}</span>
                    <span className="min-w-0 flex-1">
                      <span className="block truncate font-bold">{name}</span>
                      <span className="block truncate text-[13px] text-muted">{"Not connected" + (server && server !== name ? " · " + server : "")}</span>
                    </span>
                    <Button ref={(b) => { if (b) buttons.current.set(w.id, b); else buttons.current.delete(w.id); }}
                      size="sm" variant="outline" icon={<IconPlugConnected size={18} aria-hidden="true" />} disabled={!!busy} onClick={() => reconnect(w)}
                      aria-label={"Reconnect " + name}>{busy === w.id ? "Reconnecting…" : "Reconnect"}</Button>
                  </div>
                  {error?.id === w.id && <p role="alert" className="pt-1.5 pl-[52px] text-[13px] font-semibold text-danger">Couldn’t reconnect: {error.text}</p>}
                </li>
              );
            })}
          </ul>
        </div>
      )}
      {gone.error && <p role="alert" className="px-2 pt-2 text-[13px] font-semibold text-danger">Couldn’t list the workspaces you left: {gone.error}</p>}
      {onJoin && (
        <button type="button" onClick={onJoin}
          className="mt-2 flex min-h-12 w-full items-center gap-3 rounded-2xl border-[1.5px] border-dashed border-outline px-2 py-2 text-left font-bold hover:bg-sunken lg:border">
          <span aria-hidden="true" className="grid size-10 place-items-center rounded-xl bg-surface stroke"><IconPlus size={20} stroke={2.4} /></span>
          Join a workspace…
        </button>
      )}
    </div>
  );
}

/** JoinSheet joins another workspace with an invitation, then offers to go
 *  there. It renders nothing on a host that cannot join (no workspace shell). */
export function JoinSheet({ open, onClose, onJoined, ws }: { open: boolean; onClose: () => void; onJoined?: () => void; ws?: Memberships | null }) {
  const store = useApp();
  const memberships = ws || store.host.workspaces;
  const [invite, setInvite] = useState("");
  const [name, setName] = useState("");
  const [device, setDevice] = useState(() => deviceName(store.host.workspace.address || ""));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [retryID, setRetryID] = useState(""); // a failed join keeps its id: trying again continues it, never a second membership
  const [joined, setJoined] = useState<Workspace | null>(null);

  if (!memberships?.join) return null;
  const join = memberships.join.bind(memberships);
  const select = (id: string) => memberships.select(id);
  const close = (o: boolean) => {
    if (o) return;
    onClose();
    setJoined(null); setError("");
  };
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    // An invitation often arrives as a link (https://server/#agentnet-invite-v1:…): the part after # is the invitation itself.
    const token = invite.match(/agentnet-invite-v1:[^\s#&]+/)?.[0] || invite.trim();
    const body: T.WorkspaceJoin = { name: name.trim(), invite: token, agent: device.trim(), ...(retryID ? { id: retryID } : {}) };
    if (!body.invite || !body.agent) { setError("Paste the invitation and name this device."); return; } // the name is optional: the workspace's own shows
    setBusy(true); setError("");
    try {
      const h = await join(body);
      setInvite(""); setRetryID(""); // an invitation works once; it is not kept on the page
      setJoined(h.workspace);
      onJoined?.();
    } catch (err) {
      const retry = (err as { retryID?: string }).retryID;
      if (retry) setRetryID(retry);
      setError(errorText(err) + (retry ? " Paste a new invitation and press Join again: it continues this same join." : ""));
    }
    setBusy(false);
  };

  if (joined) {
    const label = workspaceLabel(joined);
    return (
      <Sheet open={open} onOpenChange={close} title={"You joined " + label}
        footer={<div className="flex flex-col gap-2 sm:flex-row-reverse">
          <Button variant="act" size="lg" onClick={() => { close(false); try { select(joined.id); } catch (e) { store.toast(errorText(e), "error"); } }}>Go to {label}</Button>
          <Button variant="outline" size="lg" onClick={() => close(false)}>Stay here</Button>
        </div>}>
        <div className="flex items-start gap-3 rounded-2xl bg-ok-bg p-4 text-ok-ink">
          <IconCircleCheck size={24} aria-hidden="true" className="mt-0.5 shrink-0" />
          <p>{label} has its own people and chats. Switch between workspaces any time from this menu.</p>
        </div>
      </Sheet>
    );
  }

  const field = "mt-1.5 block w-full rounded-xl bg-surface px-3 text-[16px] text-ink stroke placeholder:text-muted";
  return (
    <Sheet open={open} onOpenChange={close} title="Join a workspace" description="Use the invitation someone on that server gave you."
      footer={<Button variant="act" size="lg" type="submit" form="workspace-join" disabled={busy}>{busy ? "Joining…" : "Join"}</Button>}>
      <form id="workspace-join" onSubmit={submit} className="flex flex-col gap-4 pt-1">
        <label className="block font-semibold">
          Invitation
          <textarea value={invite} onChange={(e) => setInvite(e.target.value)} rows={3} required spellCheck={false} autoCapitalize="none" autoComplete="off"
            placeholder="Paste the invitation or its link" className={field + " resize-none py-2.5 font-mono text-[14px] leading-snug"} />
        </label>
        <label className="block font-semibold">
          What you call it <span className="font-normal text-muted">(optional)</span>
          <input value={name} onChange={(e) => setName(e.target.value)} maxLength={120} placeholder="Leave empty to use the workspace’s own name" className={field + " h-12"} />
        </label>
        <label className="block font-semibold">
          This device’s name there
          <input value={device} onChange={(e) => setDevice(e.target.value)} maxLength={32} required autoCapitalize="none" autoComplete="off" spellCheck={false}
            placeholder="laptop" className={field + " h-12"} />
          <span className="mt-1 block text-[13px] font-normal text-muted">Others there see it next to your name.</span>
        </label>
        {error && <p role="alert" className="rounded-xl bg-danger-bg px-3 py-2.5 text-[15px] font-semibold text-danger">{error}</p>}
      </form>
    </Sheet>
  );
}

/** RenameSheet sets this device's own label of a workspace (target); an
 *  empty one, or "Use the workspace’s own name", clears it. It renders
 *  nothing on a host that cannot keep a label. */
export function RenameSheet({ target, onClose, onRenamed, ws }: { target: Workspace | null; onClose: () => void; onRenamed?: () => void; ws?: Memberships | null }) {
  const store = useApp();
  const memberships = ws || store.host.workspaces;
  const own = useStore(store, (s) => s.overview?.workspace);
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    const n = (target?.name || "").trim();
    setName(generic.has(n.toLowerCase()) ? "" : n); setError("");
  }, [target?.id]);
  if (!memberships?.rename || !target) return null;
  const rename = memberships.rename.bind(memberships);
  const fallback = workspaceLabel({ ...target, name: "" }, target.id === memberships.active() ? own : undefined);
  const save = async (value: string) => {
    setBusy(true); setError("");
    try {
      await rename(target.id, value);
      store.toast(value.trim() ? "Renamed to " + value.trim() + " on this device." : "Using the workspace’s own name, " + fallback + ".", "ok");
      onRenamed?.();
      onClose();
    } catch (e) {
      setError(errorText(e));
    }
    setBusy(false);
  };
  const field = "mt-1.5 block h-12 w-full rounded-xl bg-surface px-3 text-[16px] text-ink stroke placeholder:text-muted";
  return (
    <Sheet open={!!target} onOpenChange={(o) => { if (!o) onClose(); }} title={"Rename " + workspaceLabel(target, target.id === memberships.active() ? own : undefined)}
      description="Your own name for it, on this device only. Others keep theirs."
      footer={<div className="flex flex-col gap-2 sm:flex-row-reverse">
        <Button variant="act" size="lg" type="submit" form="workspace-rename" disabled={busy}>{busy ? "Saving…" : "Save"}</Button>
        <Button variant="outline" size="lg" onClick={() => void save("")} disabled={busy}>Use the workspace’s own name</Button>
      </div>}>
      <form id="workspace-rename" onSubmit={(e) => { e.preventDefault(); void save(name); }} className="flex flex-col gap-3 pt-1">
        <label className="block font-semibold">
          Your name for it
          <input value={name} onChange={(e) => setName(e.target.value)} maxLength={120} placeholder={fallback} className={field} />
          <span className="mt-1 block text-[13px] font-normal text-muted">Leave it empty to use the workspace’s own name ({fallback}).</span>
        </label>
        {error && <p role="alert" className="rounded-xl bg-danger-bg px-3 py-2.5 text-[15px] font-semibold text-danger">{error}</p>}
      </form>
    </Sheet>
  );
}

/** LeaveSheet confirms leaving a workspace (target), then leaves it. Leaving
 *  the one shown now makes the host switch and remount. It renders nothing
 *  on a host that cannot leave one. */
export function LeaveSheet({ target: workspace, onClose, onLeft, ws }: { target: Workspace | null; onClose: () => void; onLeft?: () => void; ws?: Memberships | null }) {
  const store = useApp();
  const [busy, setBusy] = useState(false);
  const memberships = ws || store.host.workspaces;
  if (!memberships?.disconnect) return null;
  const disconnect = memberships.disconnect.bind(memberships);
  const label = workspace ? workspaceLabel(workspace) : "";
  const leave = async () => {
    if (!workspace) return;
    setBusy(true);
    try {
      // Leaving the workspace shown now makes the host switch and remount;
      // leaving another one only shortens the list.
      await disconnect(workspace.id);
      store.toast("You left " + label + ".", "ok");
      onLeft?.();
      onClose();
    } catch (e) {
      store.toast(errorText(e), "error");
    }
    setBusy(false);
  };
  return (
    <Sheet open={!!workspace} onOpenChange={(o) => { if (!o) onClose(); }} title={"Leave " + label + "?"}
      footer={<div className="flex flex-col gap-2 sm:flex-row-reverse">
        <Button variant="danger" size="lg" onClick={leave} disabled={busy}>{busy ? "Leaving…" : "Leave " + label}</Button>
        <Button variant="outline" size="lg" onClick={onClose}>Stay</Button>
      </div>}>
      <div className="flex flex-col gap-3 text-text-2">
        <p>This device stops getting messages from {label}.</p>
        <p>Your chats and keys for it stay on this device, and nothing is removed on that server.</p>
        <p>{memberships.reconnect ? "To come back, reconnect it from the workspace menu or Settings → Workspaces." : "To come back later, you need a new invitation."}</p>
      </div>
    </Sheet>
  );
}
