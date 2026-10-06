// Starting conversations: the "New" button and its sheet. People known
// here come first; others the server lists are offered too, said plainly
// to be unchecked (starting a chat checks who they are). A group starts
// with a name; its creator then brings people in, and each one accepts.
import { useEffect, useMemo, useRef, useState, type FormEvent } from "react";
import { IconArrowLeft, IconMessagePlus, IconPencil, IconSearch, IconUsersPlus, IconX } from "@tabler/icons-react";
import { useAgentNames, useApp, useWide } from "../context";
import { useStore, type Store } from "../store";
import { errorText, type T } from "../api";
import { deviceKind, niceDevice, personName } from "../model";
import { Sheet } from "../ui/Sheet";
import { Button, IconButton } from "../ui/Button";
import { AgentAvatar, PersonAvatar } from "../ui/Avatar";
import { Tag } from "../ui/Tag";
import { presence } from "./ChatList.row";
import { chatItems, type ListItem } from "./ChatList.words";
import { TeamPeople } from "./Teams";

/** Someone a chat can be started with. */
export interface Candidate {
  key: string;
  name: string;
  email?: string;
  address: string;          // the device the conversation is started through
  seed: string;
  online: boolean | null;   // null: the server's list does not say now
  checked: boolean;         // their person record is kept and checked here
  conflict: boolean;        // they published a different record: no new chats
  serverOnly: boolean;      // only on the server's member list, not a person here yet
  chat?: string;            // an existing chat with them, newest
}

/** candidates lists the people known here and the server's other members. */
export function candidates(o: T.Overview | null): { people: Candidate[]; server: Candidate[] } {
  if (!o || !o.persons || !o.person) return { people: [], server: [] };
  const known = new Set<string>((o.person.devices || []).map((d) => d.address).concat(o.person.address));
  // Your own DMs (not ones you are a guest in), newest first, open ones before read-only ones.
  const chats = (o.dms || []).filter((d) => d.kind !== "group" && (!d.role || d.role === "member"))
    .sort((a, b) => Number(!!a.frozen) - Number(!!b.frozen) || (b.last_at || "").localeCompare(a.last_at || ""));
  const reaches = (d: T.DMSummary, p: T.PersonView) => p.person ? d.peer.person === p.person
    : [p.address, ...(p.devices || []).map((x) => x.address)].includes(d.peer.address);
  const people = (o.people || []).map((p): Candidate => {
    const devices = (p.devices || []).map((d) => d.address);
    for (const a of devices.concat(p.address)) known.add(a);
    return {
      key: "p:" + (p.person || p.address), name: personName(p), email: p.email, address: p.address, seed: p.person || p.address,
      online: presence(o, devices.length ? devices : [p.address]), checked: p.state === "pinned", conflict: p.state === "conflict", serverOnly: false,
      chat: chats.find((d) => reaches(d, p))?.id,
    };
  }).sort((a, b) => Number(!!b.chat) - Number(!!a.chat) || a.name.localeCompare(b.name));
  const d = o.directory;
  const server = d && d.current ? (d.members || []).filter((m) => !known.has(m.address)).map((m): Candidate => {
    const user = m.address.split("/")[0] || m.address;
    return {
      key: "d:" + m.address, name: user.charAt(0).toUpperCase() + user.slice(1), address: m.address, seed: m.address,
      online: m.presence === "connected", checked: false, conflict: false, serverOnly: true,
    };
  }) : [];
  return { people, server };
}

/** directoryNote says what the server's member list is not, when it says less than usual. */
function directoryNote(o: T.Overview): string {
  const d = o.directory;
  if (!d || d.status === "not_listed") return "This workspace doesn’t list its members, so only people you already know show here.";
  if (d.status !== "listed") return "";
  if (!d.current) return "The server’s member list isn’t available right now.";
  return "";
}

export const matches = (q: string, ...words: (string | undefined)[]) => {
  const n = q.trim().toLowerCase();
  return !n || words.some((w) => (w || "").toLowerCase().includes(n));
};

/** startChat opens the chat with someone, starting one when there is none. */
export async function startChat(store: Store, c: Candidate): Promise<boolean> {
  if (c.chat) { void store.open({ kind: "dm", id: c.chat }); return true; }
  const r = await store.run((a) => a.newDM(c.address));
  if (!r) return false;
  void store.open({ kind: "dm", id: r.id });
  return true;
}

/** CandidateRow: one person (or server member) to start a chat with. */
export function CandidateRow({ c, busy, disabled, onPick }: { c: Candidate; busy: boolean; disabled: boolean; onPick: () => void }) {
  const seen = c.online === true ? "Online" : c.online === false ? "Offline" : "";
  const line = c.conflict ? "Can’t start chats: their identity changed"
    : c.serverOnly ? [niceDevice(c.address), seen].filter(Boolean).join(" · ")
    : c.chat ? [seen, "you already chat"].filter(Boolean).join(" · ")
    : c.online === false ? "Offline · gets your message later" : seen;
  return (
    <button type="button" onClick={onPick} disabled={disabled || c.conflict}
      className="flex min-h-14 w-full items-center gap-3 rounded-2xl px-2 py-2 text-left hover:bg-sunken disabled:opacity-60 disabled:hover:bg-transparent">
      <PersonAvatar name={c.name} seed={c.seed} size={40} online={c.online} />
      <span className="min-w-0 flex-1">
        {c.email && <span className="block truncate text-sm text-muted">{c.email} · verified by this workspace</span>}
        <span className="flex items-center gap-2">
          <span className="truncate font-bold">{c.name}</span>
          {!c.checked && !c.conflict && <Tag tone="muted" className="shrink-0">Not checked</Tag>}
        </span>
        {line && <span className={"block truncate text-[13px] " + (c.conflict ? "font-semibold text-danger" : "text-muted")}>{line}</span>}
      </span>
      <span aria-hidden="true" className="shrink-0 text-[13px] font-bold text-text-2">
        {busy ? "Starting…" : c.chat ? "Open" : <IconMessagePlus size={20} />}
      </span>
    </button>
  );
}

function AgentRow({ item, onPick }: { item: ListItem; onPick: () => void }) {
  return (
    <button type="button" onClick={onPick} className="flex min-h-14 w-full items-center gap-3 rounded-2xl px-2 py-2 text-left hover:bg-sunken">
      <AgentAvatar seed={item.avatarSeed} size={40} device={deviceKind(item.avatarSeed)} />
      <span className="min-w-0 flex-1">
        <span className="flex items-center gap-2"><span className="truncate font-bold">{item.title}</span><Tag tone="agent" className="shrink-0">Agent</Tag></span>
        <span className="block truncate text-[13px] font-semibold text-agent-ink">{item.subtitle}{item.note && <span className="text-muted"> · {item.note}</span>}</span>
      </span>
      <span aria-hidden="true" className="shrink-0 text-[13px] font-bold text-text-2">Open</span>
    </button>
  );
}

const heading = "px-2 pt-4 pb-1 text-[12px] font-extrabold uppercase tracking-[.08em] text-muted";

/** NewChatButton: the yellow "New" button and the sheet it opens. */
export function NewChatButton({ size = "md" }: { size?: "md" | "lg" }) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <Button variant="act" size={size} onClick={() => setOpen(true)} icon={<IconPencil size={19} stroke={2.4} aria-hidden="true" />}
        className={size === "lg" ? "!w-auto px-6" : ""}>
        {size === "lg" ? "Start a chat" : "New"}
      </Button>
      <NewChatSheet open={open} onOpenChange={setOpen} />
    </>
  );
}

function NewChatSheet({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) {
  const store = useApp();
  const overview = useStore(store, (s) => s.overview);
  const [step, setStep] = useState<"pick" | "group">("pick");
  const close = (o: boolean) => { onOpenChange(o); if (!o) setStep("pick"); };

  if (step === "group") return (
    <Sheet open={open} onOpenChange={close} title="New group"
      description="Give it a name. Then bring people in: each person says yes before they join.">
      <NewGroupForm onBack={() => setStep("pick")} onCreated={() => close(false)} />
    </Sheet>
  );
  return (
    <Sheet open={open} onOpenChange={close} title="New chat" description="Talk with one person, or start a group.">
      {!overview ? <p className="py-6 text-muted">Loading…</p>
        : !overview.persons ? <p className="py-4 text-text-2">This AgentNet can’t hold chats between people yet.</p>
        : !overview.person ? <NeedsName onDone={() => close(false)} />
        : <PickPeople overview={overview} onGroup={overview.groups ? () => setStep("group") : undefined} onDone={() => close(false)} />}
    </Sheet>
  );
}

function NeedsName({ onDone }: { onDone: () => void }) {
  const store = useApp();
  return (
    <div className="flex flex-col items-start gap-3 py-2">
      <p className="text-text-2">First, choose the name people see when you write to them.</p>
      <Button variant="act" onClick={() => { onDone(); store.showTab("settings"); }}>Choose my name</Button>
    </div>
  );
}

function PickPeople({ overview, onGroup, onDone }: { overview: T.Overview; onGroup?: () => void; onDone: () => void }) {
  const store = useApp();
  const wide = useWide();
  const agentNames = useAgentNames();
  const [query, setQuery] = useState("");
  const [busy, setBusy] = useState("");
  const input = useRef<HTMLInputElement>(null);
  // On a phone the keyboard would cover most of the sheet: focus only on desktop.
  useEffect(() => { if (wide) input.current?.focus(); }, [wide]);

  const { people, server } = useMemo(() => candidates(overview), [overview]);
  const agents = useMemo(() => chatItems(overview, agentNames).filter((i) => i.kind === "agent"), [overview, agentNames]);
  const q = query.trim();
  const shownPeople = people.filter((c) => matches(q, c.name));
  const shownServer = server.filter((c) => matches(q, c.name, niceDevice(c.address)));
  const shownAgents = q ? agents.filter((i) => matches(q, i.title, i.subtitle, i.note)) : [];
  const note = directoryNote(overview);

  const pick = async (c: Candidate) => {
    setBusy(c.key);
    const ok = await startChat(store, c);
    setBusy("");
    if (ok) onDone();
  };
  const nothing = !shownPeople.length && !shownServer.length && !shownAgents.length;

  return (
    <div className="pb-2">
      <label className="mt-1 flex h-12 items-center gap-2 rounded-xl bg-surface px-3 stroke has-[input:focus-visible]:outline-3 has-[input:focus-visible]:outline-offset-2 has-[input:focus-visible]:outline-agent-ink">
        <IconSearch size={19} aria-hidden="true" className="shrink-0" />
        <span className="sr-only">Search people</span>
        <input ref={input} type="search" value={query} onChange={(e) => setQuery(e.target.value)} placeholder="Search people"
          className="min-w-0 flex-1 bg-transparent text-[16px] outline-none placeholder:text-muted focus-visible:outline-none [&::-webkit-search-cancel-button]:hidden" />
        {query && <IconButton label="Clear search" onClick={() => { setQuery(""); input.current?.focus(); }} className="-mr-2"><IconX size={18} /></IconButton>}
      </label>

      {onGroup && !q && (
        <button type="button" onClick={onGroup} className="mt-3 flex min-h-14 w-full items-center gap-3 rounded-2xl px-2 py-2 text-left hover:bg-sunken">
          <span aria-hidden="true" className="grid size-10 shrink-0 place-items-center rounded-full bg-act text-act-ink stroke"><IconUsersPlus size={20} stroke={2.2} /></span>
          <span className="min-w-0 flex-1">
            <span className="block font-bold">New group</span>
            <span className="block text-[13px] text-muted">Name it, then bring people in</span>
          </span>
        </button>
      )}
      {!q && <Button variant="outline" onClick={() => { onDone(); store.showTab("settings", "teams"); }}>People lists</Button>}

      {shownPeople.length > 0 && <>
        <h3 className={heading}>People</h3>
        <ul>{shownPeople.map((c) => <li key={c.key}><CandidateRow c={c} busy={busy === c.key} disabled={!!busy} onPick={() => pick(c)} /></li>)}</ul>
      </>}
      {shownServer.length > 0 && <>
        <h3 className={heading}>Also on this server</h3>
        <p className="px-2 pb-1 text-[13px] text-muted">Not checked here yet. Starting a chat checks who they are first.</p>
        <ul>{shownServer.map((c) => <li key={c.key}><CandidateRow c={c} busy={busy === c.key} disabled={!!busy} onPick={() => pick(c)} /></li>)}</ul>
      </>}
      {shownAgents.length > 0 && <>
        <h3 className={heading}>Agents you talk to</h3>
        <ul>{shownAgents.map((i) => <li key={i.key}><AgentRow item={i} onPick={() => { onDone(); void store.open(i.open); }} /></li>)}</ul>
      </>}

      {nothing && (q
        ? <p className="px-2 py-6 text-center text-text-2">No one here is called “{q}”.</p>
        : <p className="px-2 py-6 text-center text-text-2">No one else is here yet. People show up once they join this server.</p>)}
      {note && !q && <p className="px-2 pt-3 text-[13px] text-muted">{note}</p>}
    </div>
  );
}

export function NewGroupForm({ onBack, onCreated, initialPeople = [] }: { onBack: () => void; onCreated: () => void; initialPeople?: Pick<T.PersonRef, "id">[] }) {
  const store = useApp();
  const overview = useStore(store, (s) => s.overview);
  const [title, setTitle] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [people, setPeople] = useState<Pick<T.PersonRef, "id">[]>(initialPeople);
  const [created, setCreated] = useState("");
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const t = title.trim();
    if (!t && !created) { setError("Give the group a name."); return; }
    if (busy) return;
    setBusy(true); setError("");
    let id = created;
    try {
      if (!id) { id = (await store.api.newGroup(t)).id; if (!store.isActive()) return; setCreated(id); }
      for (const p of people) {
        if (!store.isActive()) return;
        await store.api.inviteToGroup({ conv: id, person: p.id, history: {} });
        setPeople((ps) => ps.filter((x) => x.id !== p.id));
      }
      if (!store.isActive()) return;
      onCreated();
      await store.open({ kind: "dm", id });
      store.showTab("chats");
      store.openInvite(id);
    } catch (e) { if (store.isActive()) setError(errorText(e) + (id ? " The group is already created; retry only the remaining invitations." : "")); }
    finally { setBusy(false); void store.refetch(); }
  };
  return (
    <form onSubmit={submit} className="flex flex-col gap-3 pt-1 pb-2">
      <button type="button" disabled={busy} onClick={onBack} className="-ml-2 flex min-h-11 items-center gap-1.5 self-start rounded-full px-2 text-[15px] font-semibold text-text-2 hover:bg-sunken">
        <IconArrowLeft size={18} aria-hidden="true" />Back to people
      </button>
      <label className="block font-semibold">
        Group name
        <input value={title} onChange={(e) => setTitle(e.target.value)} maxLength={64} autoFocus autoComplete="off" disabled={busy || !!created}
          placeholder="For example, Savannah rush order"
          className="mt-1.5 block h-12 w-full rounded-xl bg-surface px-3 text-[16px] stroke placeholder:text-muted" />
      </label>
      <p className="text-[13px] text-muted">Choose people below. Each decides whether to join; you start as group admin.</p>
      <div className="flex flex-wrap gap-2" aria-label="Selected people">{people.map((p) => <button key={p.id} type="button" disabled={busy}
        onClick={() => setPeople(people.filter((x) => x.id !== p.id))} aria-label={"Remove " + (overview?.people?.find((x) => x.person === p.id)?.label || "person")}
        className="inline-flex min-h-11 max-w-full items-center gap-2 rounded-full bg-agent px-3 stroke">
        <span className="break-words">{overview?.people?.find((x) => x.person === p.id)?.label || "Person " + p.id.slice(0, 8)}</span><IconX size={16} aria-hidden="true" />
      </button>)}</div>
      <fieldset disabled={busy} className="space-y-2">
        <legend className="font-semibold">People</legend>
        <div className="max-h-48 overflow-y-auto">{(overview?.people || []).filter(p => p.state === "pinned" && p.person && p.person !== overview?.person?.person).map(p =>
          <label key={p.person} className="flex min-h-11 cursor-pointer items-center gap-3 px-1">
            <input type="checkbox" checked={people.some(x => x.id === p.person)} className="size-5 shrink-0 accent-[var(--an-agent-ink)]" onChange={e => setPeople(e.target.checked ?
              [...people, { id: p.person! }] : people.filter(x => x.id !== p.person))} />
            <span className="break-words">{personName(p)}</span>
          </label>)}</div>
      </fieldset>
      <TeamPeople selected={people} onChange={setPeople} disabled={busy} showSelected={false} />
      {created && <p role="status">The group is already created. Only the remaining people below will be invited when you retry.</p>}
      {error && <p role="alert" className="rounded-xl bg-danger-bg px-3 py-2.5 font-semibold text-danger">{error}</p>}
      <Button variant="act" size="lg" type="submit" disabled={busy || (!created && !title.trim())} className="mt-1">{busy ? "Sending…" : created ? "Retry remaining invitations" : "Create group"}</Button>
    </form>
  );
}
