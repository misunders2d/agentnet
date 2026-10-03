// The workspace switcher: which AgentNet server this is, the others this
// computer belongs to, joining one with an invitation and leaving one. The
// host owns the memberships (loader.js); switching remounts the whole
// interface over the chosen one, so nothing here can act on another
// workspace by accident. Hidden when the host has no workspace list.
import { Popover } from "@base-ui/react/popover";
import { useState, type FormEvent } from "react";
import { IconCheck, IconChevronDown, IconDoorExit, IconPlus, IconCircleCheck } from "@tabler/icons-react";
import { useApp } from "../context";
import { errorText, type T } from "../api";
import type { Host, Workspace } from "../host";
import { deviceName } from "../model";
import { Sheet } from "../ui/Sheet";
import { Button, IconButton } from "../ui/Button";

type Memberships = NonNullable<Host["workspaces"]>;

const generic = new Set(["", "current workspace", "this computer"]);

/** workspaceLabel is the workspace's own name, or its server's host name when
 *  the name is a placeholder, or "AgentNet" when neither says anything. */
export function workspaceLabel(w: Workspace): string {
  const name = (w.name || "").trim();
  if (!generic.has(name.toLowerCase())) return name;
  return serverName(w.endpoint) || "AgentNet";
}

/** serverName is a server's host name, or "" for an address that is only numbers. */
function serverName(endpoint: string): string {
  try {
    const h = new URL(endpoint).hostname.replace(/^www\./, "");
    if (!h || h === "localhost" || h.startsWith("[") || /^[\d.]+$/.test(h)) return "";
    return h;
  } catch { return ""; }
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

/** WorkspaceCoin: the desktop rail's top coin; it opens the workspace menu. */
export function WorkspaceCoin() {
  return <WorkspaceControl variant="coin" />;
}

/** WorkspacePill: the phone's equivalent, at the top of the chat list. */
export function WorkspacePill() {
  return <WorkspaceControl variant="pill" />;
}

function WorkspaceControl({ variant }: { variant: "coin" | "pill" }) {
  const store = useApp();
  const ws = store.host.workspaces;
  const [menu, setMenu] = useState(false);
  const [joining, setJoining] = useState(false);
  const [leaving, setLeaving] = useState<Workspace | null>(null);
  const [, setRevision] = useState(0); // the list changes only through join and leave here
  if (!ws) return null;

  const current = store.host.workspace;
  const label = workspaceLabel(current);
  const letters = coinLetters(label);
  const refresh = () => setRevision((n) => n + 1);
  const menuBody = (
    <WorkspaceMenu ws={ws} onDone={() => setMenu(false)}
      onJoin={ws.join ? () => { setMenu(false); setJoining(true); } : undefined}
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
          <Popover.Portal>
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
    </>
  );
}

function WorkspaceMenu({ ws, onDone, onJoin, onLeave }: { ws: Memberships; onDone: () => void; onJoin?: () => void; onLeave?: (w: Workspace) => void }) {
  const store = useApp();
  const active = ws.active();
  const list = ws.list();
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
          const name = workspaceLabel(w);
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
              {onLeave && w.id !== "default" && (
                <IconButton label={"Leave " + name + "…"} onClick={() => onLeave(w)} className="shrink-0 text-muted hover:text-danger"><IconDoorExit size={20} /></IconButton>
              )}
            </li>
          );
        })}
      </ul>
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
    if (!body.invite || !body.name || !body.agent) { setError("Paste the invitation, give the workspace a name, and name this device."); return; }
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
          What you call it
          <input value={name} onChange={(e) => setName(e.target.value)} maxLength={48} required placeholder="For example, Linen HQ" className={field + " h-12"} />
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
        <p>To come back later, you need a new invitation.</p>
      </div>
    </Sheet>
  );
}
