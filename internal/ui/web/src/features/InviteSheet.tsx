// "Bring someone in": choose a person or an agent, choose exactly what they
// may see, and send the one invitation that fits (an agent, a guest in a
// two-person chat, or a new member of a group). Nothing joins until the
// right person says yes; the sheet says who that is.
import { useEffect, useMemo, useState } from "react";
import { IconAt, IconInfoCircle, IconLock, IconPaperclip, IconSearch, IconCheck } from "@tabler/icons-react";
import { errorText, type T } from "../api";
import { useAgentNames, useApp } from "../context";
import { useStore } from "../store";
import { AgentAvatar, PersonAvatar } from "../ui/Avatar";
import { Button } from "../ui/Button";
import { Sheet } from "../ui/Sheet";
import { TASK_KEYS_MAX, capFor, catalogHosts, pool, readCatalog, routeFor, taskKeys, taskPeople, useCatalogs, type Candidate, type Route, type TaskPerson } from "./InviteSheet.candidates";
import { Checklist, ContextChoice, Preview, sinceShared, type Mode } from "./InviteSheet.context";
import { dueText, localInput } from "../model";
import { callName, canBringIn, plural, shareable } from "./RoomPanel.model";

export function InviteSheet() {
  const store = useApp();
  const invite = useStore(store, (s) => s.invite);
  // Keep the last invitation drawn while the sheet slides away.
  const [kept, setKept] = useState(invite);
  useEffect(() => { if (invite) setKept(invite); }, [invite]);
  const shown = invite || kept;
  if (!shown) return null;
  return <InviteFlow key={seq(shown)} invite={shown} open={!!invite} />;
}

const seqs = new WeakMap<object, number>();
let next = 0;
const seq = (o: object) => seqs.get(o) ?? (seqs.set(o, ++next), next);

type Invite = { conv: string; selected?: string[]; who?: string; label?: string };

function InviteFlow({ invite, open }: { invite: Invite; open: boolean }) {
  const store = useApp();
  const o = useStore(store, (s) => s.overview);
  const openDM = useStore(store, (s) => s.dm);
  const conn = useStore(store, (s) => s.conn);
  const invitations = useStore(store, (s) => s.invitations);
  const names = useAgentNames();

  // The conversation: the open one, or loaded here when the sheet was opened for another.
  const [loaded, setLoaded] = useState<T.DMThread | null>(null);
  const [loadError, setLoadError] = useState("");
  const [attempt, setAttempt] = useState(0);
  const t = openDM?.id === invite.conv ? openDM : loaded;
  useEffect(() => {
    if (openDM?.id === invite.conv) return;
    let alive = true;
    setLoadError("");
    store.api.dm(invite.conv).then((d) => { if (alive) setLoaded(d); }).catch((e) => { if (alive) setLoadError(errorText(e)); });
    return () => { alive = false; };
  }, [invite.conv, attempt]);

  const allowed = canBringIn(t);
  const hosts = useMemo(() => (t && o ? catalogHosts(t, o) : []), [t?.id, o?.seq]);
  const catalogs = useCatalogs(store.api, hosts, open && allowed && !!o?.agents);
  const [sent, setSent] = useState<{ key: string; name: string }[]>([]); // people invited to a group from this sheet
  const p = useMemo(() => {
    if (!t || !o) return null;
    const x = pool(t, o, catalogs, names, invitations, store.host.platform);
    // Until the invitation list catches up, someone just invited stays marked here.
    const asked = new Set(sent.map((s) => s.key));
    return { ...x, candidates: x.candidates.map((c) => (asked.has(c.key) && !c.unavailable ? { ...c, unavailable: "Invited · waiting for them to accept" } : c)) };
  }, [t, o, catalogs, names, invitations, sent]);

  // Who to preselect ("Bring back") and what the selection is ("Since they left").
  const preset = { who: invite.who || "", label: invite.label || "" };
  const [choice, setChoice] = useState("");
  const [query, setQuery] = useState("");
  const pickable = (k: string) => !!p?.candidates.some((c) => c.key === k && !c.unavailable);
  const chosenKey = choice || (pickable(preset.who) ? preset.who : p?.pick || "");
  const c = p?.candidates.find((x) => x.key === chosenKey);

  const [mode, setMode] = useState<Mode>(invite.selected?.length ? "selected" : "recent");
  const [recent, setRecent] = useState(10);
  const [since, setSince] = useState(() => localInput(new Date(Date.now() - 24 * 3600e3))); // "Since a date": a day ago to start
  // Who may give an invited agent tasks without asking, per candidate: your
  // own agent starts with you (own agents inherit your rights); anyone
  // else is a tap of yours.
  const [tasksFor, setTasksFor] = useState<{ cand: string; chosen: Set<string> } | null>(null);
  const [selected, setSelected] = useState(() => new Set(invite.selected || []));
  const [selection, setSelection] = useState(invite.selected?.length ? preset.label : ""); // what the selection is, until changed
  const [note, setNote] = useState("");
  const [filesOk, setFilesOk] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const route: Route | null = c && t ? routeFor(c, t) : null;
  const [guestCheck,setGuestCheck]=useState<T.GuestCheck|null>(null);
  const [checkError,setCheckError]=useState("");
  useEffect(()=>{let alive=true;setGuestCheck(null);setCheckError("");if(route==="guest"&&c?.person?.address&&t)store.api.checkGuest({conv:t.id,host:c.person.address}).then(v=>{if(alive)setGuestCheck(v);}).catch(e=>{if(alive)setCheckError(errorText(e));});return()=>{alive=false;};},[route,c?.key,t?.id]);
  const list = useMemo(() => (t ? shareable(t).filter((m) => (route === "group" || t.kind === "group" ? !!m.group_ref : true)) : []), [t, route]);
  const cap = route ? capFor(route) : 200;
  const max = Math.min(cap, list.length);
  const count = Math.min(recent, max);
  const picked = list.filter((m) => selected.has(m.id));
  const sinceSel = sinceShared(list, since, cap);
  const shared = mode === "recent" ? list.slice(list.length - count) : mode === "since" ? sinceSel.shared : picked;
  const tooMany = mode === "selected" && picked.length > cap;
  const files = shared.reduce((n, m) => n + (m.attachments?.length || 0), 0);
  const filesKey = shared.filter((m) => m.attachments?.length).map((m) => m.id).join(",");
  const needFiles = files > 0 && filesOk !== filesKey;
  const gapless = mode === "recent" || mode === "since" || (picked.length > 0 && list.indexOf(picked[picked.length - 1]) - list.indexOf(picked[0]) === picked.length - 1 && picked[picked.length - 1] === list[list.length - 1]);

  const shownName = c ? (c.kind === "agent" ? callName(c.name) : c.name) : "";
  const Who = c ? c.name : "They";
  const after = route === "agent" ? (t?.kind === "group" ? "Then new group turns while it is a member; its owner decides what runs." : "Then only what someone asks it here.") : route === "group" ? "Then everything new: they become a member." : "Then new messages while they’re here.";
  const offline = conn !== "live";
  const now = route === "agent" && !!c?.mine && !c.alreadyHere; // your own agent joins in this one step

  const people = useMemo(() => (t && o ? taskPeople(t, o) : []), [t, o]);
  const chosenTasks = c && tasksFor?.cand === c.key ? tasksFor.chosen : new Set(c?.mine ? people.filter((x) => x.me).map((x) => x.key) : []);
  const tasksFrom = route === "agent" ? taskKeys(people, chosenTasks) : [];
  const tooManyKeys = tasksFrom.length > TASK_KEYS_MAX;
  const toggleTasks = (key: string) => {
    if (!c) return;
    const next = new Set(chosenTasks);
    if (next.has(key)) next.delete(key); else next.add(key);
    setTasksFor({ cand: c.key, chosen: next });
  };

  const visible = useMemo(() => {
    const q = query.trim().toLowerCase();
    return (p?.candidates || []).filter((x) => !q || (x.name + " " + x.subtitle).toLowerCase().includes(q));
  }, [p, query]);

  async function bring() {
    if (!c || !t || !route || busy || route==="guest"&&!guestCheck) return;
    setBusy(true);
    setError("");
    let v: T.AgentView | undefined;
    try {
      if (route === "agent") {
        if (c.agentId && !c.mine) {
          // A named agent is invited only while its owner still offers it.
          const fresh = await readCatalog(store.api, c.host!);
          if (!fresh.some((r) => r.id === c.agentId)) throw new Error(c.name + " is no longer offered on that computer. Nothing was sent.");
        }
        v = await store.api.inviteAgent({ conv: t.id, host: c.host!, ...(c.agentId ? { agent_id: c.agentId } : {}), share: shared.map((m) => m.id), tasks_from: tasksFrom, note: note.trim() });
      } else if (route === "guest") {
        await store.api.inviteGuest({ conv: t.id, host: c.person!.address, share: shared.map((m) => m.lid || m.id), note: note.trim() });
      } else {
        await store.api.inviteToGroup({ conv: t.id, person: c.person!.person!, history: shared.length ? { refs: shared.map((m) => m.group_ref!) } : {} });
      }
    } catch (e) {
      setError(errorText(e));
      setBusy(false);
      return;
    }
    if (route === "group") {
      // A new group usually needs several people: stay open for the next one.
      setSent((s) => [...s, { key: c.key, name: c.name }]);
      setChoice("-"); // nothing chosen until the next pick
      setNote("");
      setBusy(false);
      void store.refetch();
      return;
    }
    store.closeInvite();
    if (c.alreadyHere) store.toast("Shared more messages with " + shownName, "ok");
    else if (now && v) {
      // "Bring Ledger in now" is the owner's own OK: let it join unless the
      // invitation already made it active.
      let joined = v.state === "active";
      if (!joined && v.can_decide) {
        try { joined = (await store.api.decideAgent(v.pid, true)).state === "active"; } catch (e) {
          store.toast(upper(shownName) + " is invited but didn’t join: " + errorText(e) + " Let it join from “In this chat”.", "error");
          store.setPanel(true);
          void store.refetch();
          return;
        }
      }
      if (joined) store.toast(upper(shownName) + " joined — it sees only " + (shared.length ? "what you shared" : "what someone asks it here"), "ok");
      else { store.toast("Invited " + shownName + " — “In this chat” shows when it’s in", "ok"); store.setPanel(true); }
    } else store.toast(doneText(c, route), "ok");
    void store.refetch();
  }

  const body = !t ? (
    loadError ? <Problem text={loadError} onRetry={() => setAttempt((n) => n + 1)} /> : <Quiet>Opening this chat…</Quiet>
  ) : !allowed ? (
    <Quiet>{t.frozen ? "This chat can’t change right now, so no one can be brought in." : "Only the people in this chat can bring someone in."}</Quiet>
  ) : (
    <>
      <label className="relative mt-1 block">
        <span className="sr-only">Search people and agents</span>
        <IconSearch size={20} className="pointer-events-none absolute left-3.5 top-1/2 -translate-y-1/2 text-text-2" aria-hidden="true" />
        <input type="search" value={query} onChange={(e) => setQuery(e.target.value)} placeholder="Search people & agents" autoComplete="off"
          className="h-12 w-full rounded-2xl stroke bg-surface pl-11 pr-4 text-[16px] text-ink outline-none placeholder:text-muted focus-visible:outline-3 focus-visible:outline-agent-ink lg:text-[15px]" />
      </label>

      <Candidates list={visible} chosen={chosenKey} onChoose={(k) => { setChoice(k); setError(""); }} note={query ? "" : p?.peopleNote || ""}
        loading={catalogs.loading} query={query.trim()} total={p?.candidates.length || 0} />

      {c && route && (
        <div className="fade-in" key={route}>
          <h3 className="mt-5 font-display text-[18px] font-bold leading-tight">{c.alreadyHere ? "Share more messages with " : "What can "}{shownName}{c.alreadyHere ? "" : " see?"}</h3>
          {route === "group" && (
            <p className="mt-1.5 flex gap-2 rounded-xl bg-sunken px-3 py-2 text-[13px] font-semibold text-text-2">
              <IconInfoCircle size={17} className="mt-px shrink-0" aria-hidden="true" />
              People you add to a group become members, not guests: once they accept, they see everything new and stay until an admin removes them.
            </p>
          )}
          {list.length === 0 ? (
            <p className="mt-2.5 flex gap-2 rounded-2xl border-2 border-dashed border-outline/35 bg-surface/70 px-3 py-2.5 text-[14px] font-semibold text-text-2">
              <IconLock size={17} className="mt-0.5 shrink-0" aria-hidden="true" />
              <span>Nothing has been said here yet, so nothing earlier is shared.{route === "group" ? "" : " " + after}</span>
            </p>
          ) : (
          <div className="mt-2.5">
            <ContextChoice mode={mode} onMode={setMode} recent={count} max={max} onRecent={setRecent} picked={picked.length} label={selection} available={list.length}
              since={since} onSince={setSince} sinceCount={sinceSel.total} cap={cap} />
          </div>
          )}
          {mode === "selected" && (
            <Checklist list={list} t={t} o={o} names={names} selected={selected} onClear={() => { setSelected(new Set()); setSelection(""); }}
              onToggle={(id) => { setSelection(""); setSelected((s) => { const n = new Set(s); if (n.has(id)) n.delete(id); else n.add(id); return n; }); }} />
          )}
          {tooMany && <p role="alert" className="mt-2 text-[13px] font-semibold text-danger">Pick at most {cap} messages here.</p>}
          {mode === "selected" && selected.size > picked.length && (
            <p className="mt-2 text-[13px] text-muted">{plural(selected.size - picked.length, "picked message")} can’t be shared this way and will stay private.</p>
          )}
          {!tooMany && list.length > 0 && <Preview key={mode + count + picked.length + (mode === "since" ? since : "")} who={Who} shared={shared} after={after} t={t} o={o} names={names} gapless={gapless}
            limit={mode === "since" && sinceSel.over ? "Only the newest " + cap + " since " + dueText(since) + " are shared: that’s the most one invitation carries." : undefined} />}
          {files > 0 && (
            <label className="mt-3 flex min-h-11 cursor-pointer items-center gap-3 rounded-2xl stroke bg-surface px-3 py-2 has-[:focus-visible]:outline-3 has-[:focus-visible]:outline-agent-ink">
              <input type="checkbox" className="sr-only" checked={!needFiles} onChange={(e) => setFilesOk(e.target.checked ? filesKey : "")} />
              <span aria-hidden="true" className={"grid size-[22px] shrink-0 place-items-center rounded-md border-[1.5px] border-outline " + (!needFiles ? "bg-act text-act-ink" : "bg-surface")}>{!needFiles && <IconCheck size={15} stroke={3} />}</span>
              <IconPaperclip size={18} className="shrink-0 text-text-2" aria-hidden="true" />
              <span className="text-[14px] font-semibold">Also share the {plural(files, "file")} on these messages</span>
            </label>
          )}
          {route === "agent" && people.length > 0 && (
            <TaskChoice people={people} chosen={chosenTasks} onToggle={toggleTasks} mine={!!c.mine} owner={c.ownerName || "its owner"} keys={tasksFrom.length} />
          )}
          {route !== "group" && !c.alreadyHere && (
            <label className="mt-3 flex h-12 items-center gap-2 rounded-2xl stroke bg-surface px-3.5 has-[:focus-visible]:outline-3 has-[:focus-visible]:outline-agent-ink">
              <span className="shrink-0 text-[13px] font-extrabold text-text-2">Why?</span>
              <input value={note} onChange={(e) => setNote(e.target.value)} maxLength={200} autoComplete="off" placeholder={"A note for " + (route === "agent" ? "it" : "them") + " (optional)"}
                className="min-w-0 flex-1 bg-transparent text-[16px] text-ink outline-none placeholder:text-muted lg:text-[15px]" />
            </label>
          )}
        </div>
      )}
    </>
  );

  const ready = !!c && !c.unavailable && !tooMany && !tooManyKeys && !needFiles && !offline && !busy && (route !== "guest" || !!guestCheck);
  const footer = t && allowed ? (
    <div>
      {sent.length > 0 && !error && (
        <p role="status" className="mb-2 flex items-start gap-2 rounded-xl bg-ok-bg px-3 py-2 text-[14px] font-semibold text-ok-ink">
          <IconCheck size={18} stroke={2.6} className="mt-px shrink-0" aria-hidden="true" />
          <span>Invited {joinNames(sent.map((s) => s.name))}. {sent.length === 1 ? "They become a member" : "Each becomes a member"} once they accept.{c ? "" : " Choose someone else, or you’re done."}</span>
        </p>
      )}
      {route === "guest" && <p role="status" className="text-sm text-muted">{checkError || guestCheck?.text || (!guestCheck ? "Checking their app…" : "")}</p>}
      {error && <p role="alert" className="mb-2 rounded-xl bg-danger-bg px-3 py-2 text-[14px] font-semibold text-danger">{error}</p>}
      {offline && <p role="status" className="mb-2 text-[13px] font-semibold text-guest-ink">You’re offline. Invitations go out once you’re connected again.</p>}
      {needFiles && !offline && <p role="status" className="mb-2 text-[13px] font-semibold text-text-2">These messages carry {plural(files, "file")}: tick “Also share” above, or pick messages without files.</p>}
      {!c && sent.length > 0 ? (
        <Button variant="outline" size="lg" onClick={() => store.closeInvite()} className="min-h-14 text-[17px]">Done</Button>
      ) : (
        <Button variant="act" size="lg" disabled={!ready} onClick={bring} className="min-h-14 text-[17px]"
          icon={c ? <CandidateAvatar c={c} size={28} /> : undefined}>
          {!c ? "Choose who to bring in" : busy ? (c.alreadyHere ? "Sharing more messages…" : "Bringing " + shownName + " in…") : c.alreadyHere ? "Share more messages with " + shownName : route === "guest" && guestCheck?.needs_update?.some(p => p.role === "guest") ? "Invite " + shownName + " — waits for update" : "Bring " + shownName + " in" + (now ? " now" : "")}
        </Button>
      )}
      {c && route && <p className="mt-2.5 pb-1 text-center text-[13px] font-semibold leading-snug text-muted">{consentText(c, route)}</p>}
    </div>
  ) : undefined;

  return (
    <Sheet open={open} onOpenChange={(v) => { if (!v) store.closeInvite(); }} title="Bring someone in"
      description={t?.kind === "group"
        ? "People and agents can join as members. Agents stay until removed; their owners decide what runs."
        : "They join as a guest and see only what you share. Anyone here can dismiss them."} footer={footer}>
      {body}
    </Sheet>
  );
}

/** TaskChoice: who may give the invited agent tasks without asking, one row per
 *  person (their devices today). Your own agent starts with you ticked. */
function TaskChoice({ people, chosen, onToggle, mine, owner, keys }: {
  people: TaskPerson[]; chosen: Set<string>; onToggle: (key: string) => void; mine: boolean; owner: string; keys: number;
}) {
  return (
    <fieldset className="mt-4">
      <legend className="font-display text-[16px] font-bold leading-tight">Who can give it tasks without asking</legend>
      <p className="mt-1 text-[13px] text-text-2">
        Their tasks run without waiting for an OK. Everyone else’s wait for {mine ? "yours" : owner + "’s"}.{mine ? "" : " " + owner + " sees this before saying yes."}
      </p>
      <div className="mt-2 flex flex-col gap-1">
        {people.map((p) => {
          const on = chosen.has(p.key), none = !p.keys.length;
          return (
            <label key={p.key} className={"flex min-h-12 items-center gap-3 rounded-2xl px-2.5 py-1.5 has-[:focus-visible]:outline-3 has-[:focus-visible]:outline-agent-ink "
              + (none ? "cursor-not-allowed opacity-60" : "cursor-pointer hover:bg-sunken")}>
              <input type="checkbox" className="sr-only" checked={on} disabled={none} onChange={() => onToggle(p.key)} />
              <span aria-hidden="true" className={"grid size-[22px] shrink-0 place-items-center rounded-md border-[1.5px] border-outline " + (on ? "bg-act text-act-ink" : "bg-surface")}>{on && <IconCheck size={15} stroke={3} />}</span>
              <PersonAvatar name={p.face} seed={p.seed} size={28} />
              <span className="min-w-0 flex-1">
                <span className="block truncate font-semibold">{p.name}</span>
                <span className="block truncate text-[13px] text-text-2">
                  {none ? "Their devices aren’t checked here yet" : p.me ? (mine ? "Your own agent: on by default" : "From your devices") : mine && on ? "They can give your agent tasks without asking you" : "From their devices"}
                </span>
              </span>
            </label>
          );
        })}
      </div>
      {keys > TASK_KEYS_MAX
        ? <p role="alert" className="mt-1.5 text-[13px] font-semibold text-danger">One invitation can name at most {TASK_KEYS_MAX} devices, and these people have {keys}. Untick someone.</p>
        : <p className="mt-1.5 text-[13px] text-muted">It covers the devices they have now. A device they add later waits for an OK.</p>}
    </fieldset>
  );
}

function Candidates({ list, chosen, onChoose, note, loading, query, total }: {
  list: Candidate[]; chosen: string; onChoose: (key: string) => void; note: string; loading: boolean; query: string; total: number;
}) {
  const people = list.filter((c) => c.kind === "person");
  const agents = list.filter((c) => c.kind === "agent");
  const both = people.length > 0 && agents.length > 0;
  return (
    <div role="radiogroup" aria-label="Who to bring in" className="mt-3 flex flex-col gap-1">
      {note && <p className="mb-1 flex gap-2 px-1 text-[13px] font-semibold text-text-2"><IconInfoCircle size={17} className="mt-px shrink-0" aria-hidden="true" />{note}</p>}
      {both && <Section>People</Section>}
      {people.map((c) => <Row key={c.key} c={c} on={c.key === chosen} onChoose={onChoose} />)}
      {both && <Section>Agents</Section>}
      {agents.map((c) => <Row key={c.key} c={c} on={c.key === chosen} onChoose={onChoose} />)}
      {loading && (
        <p className="flex items-center gap-2 px-2 py-2 text-[13px] font-semibold text-muted" role="status">
          <span className="flex gap-0.5" aria-hidden="true"><i className="working-dot size-1.5 rounded-full bg-muted" /><i className="working-dot size-1.5 rounded-full bg-muted" /><i className="working-dot size-1.5 rounded-full bg-muted" /></span>
          Looking for agents…
        </p>
      )}
      {!loading && !list.length && (
        <p className="px-2 py-3 text-[14px] text-text-2">
          {query ? "No one called “" + query + "” here." : total ? "" : "No one else is known here yet. People show up once their device is checked in a chat with them."}
        </p>
      )}
    </div>
  );
}

const Section = ({ children }: { children: string }) => <h4 className="mt-2 px-1 text-[13px] font-extrabold uppercase tracking-wide text-muted first:mt-0">{children}</h4>;

function Row({ c, on, onChoose }: { c: Candidate; on: boolean; onChoose: (key: string) => void }) {
  const off = !!c.unavailable;
  return (
    <label className={"flex min-h-16 items-center gap-3 rounded-2xl border-[1.5px] px-2.5 py-2 transition-[background-color,box-shadow,border-color] duration-200 ease-out-soft has-[:focus-visible]:outline-3 has-[:focus-visible]:outline-agent-ink "
      + (off ? "cursor-not-allowed border-transparent opacity-60" : on ? "cursor-pointer border-outline bg-surface shadow-pop-sm" : "cursor-pointer border-transparent hover:bg-sunken")}>
      <input type="radio" name="invite-who" value={c.key} checked={on} disabled={off} onChange={() => onChoose(c.key)} className="sr-only" />
      <CandidateAvatar c={c} size={40} />
      <span className="min-w-0 flex-1">
        <span className="block truncate text-[15.5px] font-bold leading-tight">{c.name}</span>
        {c.person?.email && <span className="block truncate text-sm text-muted">{c.person.email} · verified by this workspace</span>}
        <span className={"block truncate text-[13px] font-semibold " + (c.kind === "agent" ? "text-agent-ink" : "text-text-2")}>{c.unavailable || c.subtitle}</span>
        {c.reason && <span className="mt-0.5 flex items-center gap-1 text-[13px] font-semibold text-approval-ink"><IconAt size={14} stroke={2.4} aria-hidden="true" />{c.reason}</span>}
      </span>
      <span aria-hidden="true" className={"grid size-[26px] shrink-0 place-items-center rounded-full border-[1.5px] border-outline transition-colors duration-200 " + (on ? "bg-act text-act-ink" : "bg-surface")}>
        {on && <IconCheck size={16} stroke={3} className="pop-in" />}
      </span>
    </label>
  );
}

function CandidateAvatar({ c, size }: { c: Candidate; size: 28 | 40 }) {
  return c.kind === "agent"
    ? <AgentAvatar seed={c.seed} size={size} device={c.alreadyHere ? undefined : c.device} mood={c.online === false ? "asleep" : "neutral"} />
    : <PersonAvatar name={c.name} seed={c.seed} size={size} online={size >= 32 ? c.online : undefined} />;
}

const Quiet = ({ children }: { children: string }) => <p className="py-6 text-center text-[15px] text-text-2" role="status">{children}</p>;

function Problem({ text, onRetry }: { text: string; onRetry: () => void }) {
  return (
    <div className="flex flex-col items-center gap-3 py-6 text-center" role="alert">
      <p className="text-[15px] font-semibold text-danger">{text}</p>
      <Button size="sm" onClick={onRetry}>Try again</Button>
    </div>
  );
}

/** consentText says who must agree before anything joins. */
function consentText(c: Candidate, r: Route): string {
  if (c.alreadyHere) return "Shares selected messages with the agent already here.";
  if (r === "agent") return c.mine ? "It’s yours, so this is your OK: it joins right away." : "It joins only if " + (c.ownerName || "its owner") + " says yes.";
  return r === "group" ? "They become a member only if they accept." : "They join only if they accept.";
}

function doneText(c: Candidate, r: Route): string {
  if (r === "agent") return "Invited " + c.name + " — waiting for " + (c.ownerName ? c.ownerName + "’s" : "its owner’s") + " OK";
  return "Invited " + c.name + " — waiting for them to join";
}

const upper = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);

/** "Anna", "Anna and Bohdan", "Anna, Bohdan and Vitalii". */
const joinNames = (n: string[]) => (n.length < 2 ? n.join("") : n.slice(0, -1).join(", ") + " and " + n[n.length - 1]);
