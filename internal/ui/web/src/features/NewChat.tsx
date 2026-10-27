// Starting conversations: the "New" button and its sheet. People known
// here come first; others the server lists are offered too, said plainly
// to be unchecked (starting a chat checks who they are). A group starts
// with a name; its creator then brings people in, and each one accepts.
import { useEffect, useLayoutEffect, useMemo, useRef, useState, type FormEvent, type RefObject } from "react";
import { IconArrowLeft, IconMessagePlus, IconPencil, IconSearch, IconUsersPlus, IconX } from "@tabler/icons-react";
import { useAgentNames, useApp, useWide } from "../context";
import { useStore } from "../store";
import { errorText, type T } from "../api";
import { deviceKind, niceDevice, personName } from "../model";
import { Sheet } from "../ui/Sheet";
import { Button, IconButton } from "../ui/Button";
import { AgentAvatar, PersonAvatar } from "../ui/Avatar";
import { Tag } from "../ui/Tag";
import { presence } from "./ChatList.row";
import { chatItems, type ListItem } from "./ChatList.words";
import { OwnAgentChats } from "./AgentChat";
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
  // Prefer existing history over a newer empty root, as the chat list does.
  // Keep every separate signed conversation reachable from its chooser.
  const chats = (o.dms || []).filter((d) => d.kind !== "group" && (!d.role || d.role === "member"))
    .sort((a, b) => Number(b.count > 0) - Number(a.count > 0) || Number(!!a.frozen) - Number(!!b.frozen) || (b.last_at || "").localeCompare(a.last_at || ""));
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

/** Show the exact destination before native identity checks/create complete. */
export function useStartChat(active = true, onStarted?: () => void) {
  const store = useApp();
  const [pending, setPending] = useState<{ candidate: Candidate; error?: string } | null>(null);
  const request = useRef<{ candidate: Candidate; current: () => boolean; failed?: boolean } | null>(null);
  const cancel = () => { request.current = null; setPending(null); };
  useLayoutEffect(() => {
    if (!active) cancel();
    const stop = store.subscribe(() => { if (request.current && !request.current.current()) cancel(); });
    return () => { request.current = null; stop(); };
  }, [store, active]);
  const start = async (candidate: Candidate) => {
    if (!active || !store.isActive() || request.current && !request.current.failed) return;
    if (candidate.chat) { onStarted?.(); void store.open({ kind: "dm", id: candidate.chat }); return; }
    const before = store.get(), owner = before.overview?.person?.person;
    const flight = { candidate, current: () => {
      const now = store.get();
      return store.isActive() && now.overview?.person?.person === owner && now.open === before.open && now.tab === before.tab && now.messageLanding?.target === before.messageLanding?.target;
    } };
    request.current = flight; setPending({ candidate });
    try {
      const result = await store.api.newDM(candidate.address);
      if (request.current !== flight || !flight.current()) return;
      request.current = null; setPending(null); onStarted?.();
      void store.open({ kind: "dm", id: result.id });
    } catch (error) {
      if (request.current === flight && flight.current()) { request.current.failed = true; setPending({ candidate, error: errorText(error) }); }
    } finally { if (store.isActive()) void store.refetch(); }
  };
  return { pending, start, cancel };
}

export function StartingChat({ flow }: { flow: ReturnType<typeof useStartChat> }) {
  const p = flow.pending, region = useRef<HTMLElement>(null);
  useLayoutEffect(() => { region.current?.scrollIntoView({ block: "nearest", behavior: "instant" }); }, [p]);
  if (!p) return null;
  return <section ref={region} aria-label={"Starting chat with " + p.candidate.name} className="space-y-3 rounded-2xl bg-surface p-4 stroke">
    <div className="flex items-center gap-3"><PersonAvatar name={p.candidate.name} seed={p.candidate.seed} size={40} /><h3 className="min-w-0 break-words font-display text-xl font-bold">{p.candidate.name}</h3></div>
    <p role={p.error ? "alert" : "status"}>{p.error ? "Could not confirm this chat. " + p.error : "Starting this chat…"}</p>
    <p className="text-sm text-muted">{p.error ? "A chat may already exist. Go back and check before trying again." : "Checking the person and creating the conversation. Going back leaves that work running."}</p>
    <div className="flex flex-wrap gap-2"><Button variant="outline" onClick={flow.cancel}>Back to people</Button>{p.error && <Button variant="act" onClick={() => void flow.start(p.candidate)}>Try starting chat again</Button>}</div>
  </section>;
}

/** CandidateRow: one person (or server member) to start a chat with. */
export function CandidateRow({ c, busy, disabled, onPick }: { c: Candidate; busy: boolean; disabled: boolean; onPick: () => void }) {
  const seen = c.online === true ? "Online" : c.online === false ? "Offline" : "";
  const line = c.conflict ? "Can’t start chats: their identity changed"
    : c.serverOnly ? [niceDevice(c.address), seen].filter(Boolean).join(" · ")
    : c.chat ? [seen, "you already chat"].filter(Boolean).join(" · ")
    : c.online === false ? "Offline · gets your message later" : seen;
  return (
    <button type="button" data-new-chat-choice onClick={onPick} disabled={disabled || c.conflict}
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
    <button type="button" data-new-chat-choice onClick={onPick} className="flex min-h-14 w-full items-center gap-3 rounded-2xl px-2 py-2 text-left hover:bg-sunken">
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
  const trigger = useRef<HTMLButtonElement>(null);
  return (
    <>
      <Button ref={trigger} variant="act" size={size} onClick={() => setOpen(true)} icon={<IconPencil size={19} stroke={2.4} aria-hidden="true" />}
        className={size === "lg" ? "!w-auto px-6" : ""}>
        {size === "lg" ? "Start a chat" : "New"}
      </Button>
      <NewChatSheet open={open} onOpenChange={setOpen} finalFocus={trigger} />
    </>
  );
}

function NewChatSheet({ open, onOpenChange, finalFocus }: { open: boolean; onOpenChange: (open: boolean) => void; finalFocus?: RefObject<HTMLElement | null> }) {
  const store = useApp();
  const overview = useStore(store, (s) => s.overview);
  const [step, setStep] = useState<"pick" | "group">("pick");
  const close = (o: boolean) => { onOpenChange(o); if (!o) setStep("pick"); };

  if (step === "group") return (
    <Sheet open={open} onOpenChange={close} finalFocus={finalFocus} title="New group"
      description="Give it a name. Then bring people in: each person says yes before they join.">
      <NewGroupForm onBack={() => setStep("pick")} onCreated={() => close(false)} />
    </Sheet>
  );
  return (
    <Sheet open={open} onOpenChange={close} finalFocus={finalFocus} title="New chat" description="Talk with one person, or start a group.">
      {!overview ? <p className="py-6 text-muted">Loading…</p>
        : !overview.persons ? <p className="py-4 text-text-2">This AgentNet can’t hold chats between people yet.</p>
        : !overview.person ? <NeedsName onDone={() => close(false)} />
        : <PickPeople overview={overview} active={open} onGroup={overview.groups ? () => setStep("group") : undefined} onDone={() => close(false)} />}
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

function PickPeople({ overview, active, onGroup, onDone }: { overview: T.Overview; active: boolean; onGroup?: () => void; onDone: () => void }) {
  const store = useApp();
  const wide = useWide();
  const agentNames = useAgentNames();
  const [query, setQuery] = useState("");
  const starting = useStartChat(active, onDone);
  // Bound the first render without limiting directory search or access.
  const pageSize = 40;
  const [peopleLimit, setPeopleLimit] = useState(pageSize);
  const [serverLimit, setServerLimit] = useState(pageSize);
  const [agentLimit, setAgentLimit] = useState(pageSize);
  const search = (value: string) => {
    setQuery(value);
    setPeopleLimit(pageSize); setServerLimit(pageSize); setAgentLimit(pageSize);
  };
  const input = useRef<HTMLInputElement>(null);
  const choices = useRef<HTMLDivElement>(null);
  // On a phone the keyboard would cover most of the sheet: focus only on desktop.
  useEffect(() => { if (wide) input.current?.focus(); }, [wide]);

  const { people, server } = useMemo(() => candidates(overview), [overview]);
  const agents = useMemo(() => chatItems(overview, agentNames).filter((i) => i.kind === "agent"), [overview, agentNames]);
  const q = query.trim();
  const shownPeople = people.filter((c) => matches(q, c.name));
  const shownServer = server.filter((c) => matches(q, c.name, niceDevice(c.address)));
  const shownAgents = q ? agents.filter((i) => matches(q, i.title, i.subtitle, i.note)) : [];
  const note = directoryNote(overview);

  const nothing = !shownPeople.length && !shownServer.length && !shownAgents.length;

  if (starting.pending) return <StartingChat flow={starting} />;

  return (
    <div ref={choices} className="pb-2" onKeyDown={e => {
      if (e.defaultPrevented || !["ArrowUp", "ArrowDown"].includes(e.key)) return;
      const rows = [...e.currentTarget.querySelectorAll<HTMLButtonElement>('[data-new-chat-choice]:not(:disabled)')];
      const index = rows.indexOf(e.target as HTMLButtonElement);
      if (index < 0) return;
      e.preventDefault();
      const next = index + (e.key === "ArrowDown" ? 1 : -1);
      if (next < 0) input.current?.focus(); else rows[Math.min(next, rows.length - 1)]?.focus();
    }}>
      <label className="mt-1 flex h-12 items-center gap-2 rounded-xl bg-surface px-3 stroke has-[input:focus-visible]:outline-3 has-[input:focus-visible]:outline-offset-2 has-[input:focus-visible]:outline-agent-ink">
        <IconSearch size={19} aria-hidden="true" className="shrink-0" />
        <span className="sr-only">Search people</span>
        <input ref={input} type="search" value={query} onChange={(e) => search(e.target.value)} placeholder="Search people"
          onKeyDown={e => {
            if (!q || e.nativeEvent.isComposing || !["Enter", "ArrowDown"].includes(e.key)) return;
            const first = choices.current?.querySelector<HTMLButtonElement>('[data-new-chat-choice]:not(:disabled)');
            if (first) { e.preventDefault(); if (e.key === "Enter") first.click(); else first.focus(); }
          }}
          className="min-w-0 flex-1 bg-transparent text-[16px] outline-none placeholder:text-muted focus-visible:outline-none [&::-webkit-search-cancel-button]:hidden" />
        {query && <IconButton label="Clear search" onClick={() => { search(""); input.current?.focus(); }} className="-mr-2"><IconX size={18} /></IconButton>}
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

      <h3 className={heading}>Your agents</h3>
      <OwnAgentChats overview={overview} query={q} onStarted={onDone} />

      {shownPeople.length > 0 && <>
        <h3 className={heading}>People</h3>
        <ul>{shownPeople.slice(0, peopleLimit).map((c) => <li key={c.key}><CandidateRow c={c} busy={false} disabled={false} onPick={() => void starting.start(c)} /></li>)}</ul>
        {shownPeople.length > peopleLimit && <Button size="sm" variant="outline" onClick={() => setPeopleLimit(n => n + pageSize)}>Show more people ({shownPeople.length - peopleLimit})</Button>}
      </>}
      {shownServer.length > 0 && <>
        <h3 className={heading}>Also on this server</h3>
        <p className="px-2 pb-1 text-[13px] text-muted">Not checked here yet. Starting a chat checks who they are first.</p>
        <ul>{shownServer.slice(0, serverLimit).map((c) => <li key={c.key}><CandidateRow c={c} busy={false} disabled={false} onPick={() => void starting.start(c)} /></li>)}</ul>
        {shownServer.length > serverLimit && <Button size="sm" variant="outline" onClick={() => setServerLimit(n => n + pageSize)}>Show more server members ({shownServer.length - serverLimit})</Button>}
      </>}
      {shownAgents.length > 0 && <>
        <h3 className={heading}>Agents you talk to</h3>
        <ul>{shownAgents.slice(0, agentLimit).map((i) => <li key={i.key}><AgentRow item={i} onPick={() => { onDone(); void store.open(i.open); }} /></li>)}</ul>
        {shownAgents.length > agentLimit && <Button size="sm" variant="outline" onClick={() => setAgentLimit(n => n + pageSize)}>Show more agents ({shownAgents.length - agentLimit})</Button>}
      </>}

      {nothing && (q
        ? <p className="px-2 py-6 text-center text-text-2">No one here is called “{q}”.</p>
        : <p className="px-2 py-6 text-center text-text-2">No one else is here yet. People show up once they join this server.</p>)}
      {note && !q && <p className="px-2 pt-3 text-[13px] text-muted">{note}</p>}
    </div>
  );
}

export function NewGroupForm({ onBack, onCreated, initialPeople = [], navigate = true }: { onBack: () => void; onCreated: (id: string) => void; initialPeople?: Pick<T.PersonRef, "id">[]; navigate?: boolean }) {
  const store = useApp();
  const overview = useStore(store, (s) => s.overview);
  const [title, setTitle] = useState("");
  const creation = useStore(store, s => s.groupCreation);
  const busy = !!creation && !creation.done && !creation.error;
  const mounted = useRef(true), attempt = useRef(0), region = useRef<HTMLElement>(null);
  useLayoutEffect(() => { mounted.current = true; return () => { mounted.current = false; attempt.current++; }; }, []);
  useLayoutEffect(() => { region.current?.scrollIntoView({ block: "nearest", behavior: "instant" }); }, [creation?.request, creation?.error]);
  const [error, setError] = useState("");
  const [people, setPeople] = useState<Pick<T.PersonRef, "id">[]>(initialPeople);
  const [peoplePending, setPeoplePending] = useState(false);
  const pageSize = 40;
  const [query, setQuery] = useState("");
  const [peopleLimit, setPeopleLimit] = useState(pageSize);
  const peopleSearch = useRef<HTMLInputElement>(null);
  const eligible = useMemo(() => (overview?.people || []).filter(p => p.state === "pinned" && p.person && p.person !== overview?.person?.person), [overview?.people, overview?.person?.person]);
  const matching = useMemo(() => eligible.filter(p => matches(query, personName(p), p.address, ...(p.devices || []).flatMap(d => [d.name, d.address]))), [eligible, query]);

  const finishCreated = (id: string) => {
    const pending = store.get().groupCreation;
    if (!mounted.current || !store.isActive() || !pending || pending.id !== id || pending.person !== store.get().overview?.person?.person) return;
    store.clearGroupCreation(pending.request);
    onCreated(id);
    if (navigate) {
      store.showTab("chats");
      const opening = store.open({ kind: "dm", id });
      const destination = store.get().open;
      void opening.then(() => {
        const now = store.get();
        if (store.isActive() && now.overview?.person?.person === pending.person && now.open === destination && now.tab === "chats") store.openInvite(id);
      });
    }
  };
  const run = async (name: string, recipients: string[]) => {
    const before = store.get(), owner = before.overview?.person?.person, token = ++attempt.current;
    const current = () => mounted.current && attempt.current === token && store.isActive() && store.get().overview?.person?.person === owner && store.get().open === before.open && store.get().tab === before.tab;
    try {
      const id = await store.createGroup(name, recipients);
      if (current()) finishCreated(id);
    } catch (e) { if (current() && !store.get().groupCreation) setError(errorText(e)); }
  };
  const submit = (e: FormEvent) => {
    e.preventDefault();
    const name = title.trim();
    if (!name) { setError("Give the group a name."); return; }
    if (busy || peoplePending) return;
    setError("");
    void run(name, people.map(p => p.id));
  };
  const back = () => { attempt.current++; onBack(); };
  if (creation) return <section ref={region} aria-label={"Creating group “" + creation.request.title + "”"} className="space-y-3 rounded-2xl bg-surface p-4 stroke">
    <h3 className="break-words font-display text-xl font-bold">{creation.request.title}</h3>
    <p role={creation.error ? "alert" : "status"}>{creation.error ? "Could not confirm this step. " + creation.error : creation.done ? "Group created. Invitations sent; each person decides whether to join." : creation.id ? "Group created. Sending invitations…" : "Creating the group…"}</p>
    {!!creation.remaining.length && <div><p className="font-semibold">{creation.id ? "Invitations remaining" : "People to invite"}</p><ul className="max-h-48 overflow-y-auto">{creation.remaining.map(id => <li key={id} className="break-words">{overview?.people?.find(p => p.person === id)?.label || "Person " + id.slice(0, 8)}</li>)}</ul></div>}
    <p className="text-sm text-muted">{creation.error ? creation.id ? "The group already exists. Retry sends only the remaining invitations." : "A group may already exist. Check Chats before trying creation again." : creation.done ? "No membership is granted until each person accepts." : "You can go back while this finishes. Reopen New group to check its progress."}</p>
    <div className="flex flex-wrap gap-2">
      <Button variant="outline" onClick={back}>Back to people</Button>
      {creation.error && <Button variant="act" onClick={() => void run(creation.request.title, creation.request.people)}>{creation.id ? "Retry remaining invitations" : "Try creating this group again"}</Button>}
      {creation.id && (creation.done || creation.error) && <Button variant="act" onClick={() => finishCreated(creation.id!)}>{creation.error ? "Use group without remaining invitations" : "Open created group"}</Button>}
      {!busy && <Button variant="ghost" onClick={() => { attempt.current++; setTitle(creation.request.title); setPeople(creation.remaining.map(id => ({id}))); setError(creation.id ? "" : "The previous creation could not be confirmed. Check Chats before starting another group."); store.clearGroupCreation(creation.request); }}>{creation.id ? "Start another group" : "Back to group details"}</Button>}
    </div>
  </section>;
  return (
    <form onSubmit={submit} className="flex flex-col gap-3 pt-1 pb-2">
      <button type="button" disabled={busy} onClick={back} className="-ml-2 flex min-h-11 items-center gap-1.5 self-start rounded-full px-2 text-[15px] font-semibold text-text-2 hover:bg-sunken">
        <IconArrowLeft size={18} aria-hidden="true" />Back to people
      </button>
      <label className="block font-semibold">
        Group name
        <input value={title} onChange={(e) => setTitle(e.target.value)} maxLength={64} autoFocus autoComplete="off" disabled={busy}
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
        <div className="flex items-end gap-2">
          <label className="min-w-0 flex-1 text-sm">Search people<input ref={peopleSearch} type="search" value={query} onChange={e => { setQuery(e.target.value); setPeopleLimit(pageSize); }} placeholder="Name or device" autoComplete="off" className="mt-1 block h-11 w-full rounded-xl bg-surface px-3 text-[16px] stroke" /></label>
          {query && <Button type="button" size="sm" variant="outline" onClick={() => { setQuery(""); setPeopleLimit(pageSize); peopleSearch.current?.focus({preventScroll:true}); }}>Clear search</Button>}
        </div>
        <div className="max-h-48 overflow-y-auto">{matching.slice(0, peopleLimit).map(p =>
          <label key={p.person} className="flex min-h-11 cursor-pointer items-center gap-3 px-1">
            <input type="checkbox" checked={people.some(x => x.id === p.person)} className="size-5 shrink-0 accent-[var(--an-agent-ink)]" onChange={e => setPeople(e.target.checked ?
              [...people, { id: p.person! }] : people.filter(x => x.id !== p.person))} />
            <span className="break-words">{personName(p)}</span>
          </label>)}</div>
        {matching.length > peopleLimit && <Button type="button" size="sm" variant="outline" onClick={() => setPeopleLimit(n => n + pageSize)}>Show more people ({matching.length - peopleLimit})</Button>}
        {!matching.length && <p className="text-sm text-muted">No matching people.</p>}
      </fieldset>
      <TeamPeople selected={people} onChange={setPeople} disabled={busy} showSelected={false} onPendingChange={setPeoplePending} />
      {error && <p role="alert" className="rounded-xl bg-danger-bg px-3 py-2.5 font-semibold text-danger">{error}</p>}
      <Button variant="act" size="lg" type="submit" disabled={busy || peoplePending || !title.trim()} className="mt-1">Create group</Button>
    </form>
  );
}
