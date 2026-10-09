// A team grants no conversation access. Expand a verified snapshot, review
// the people, then send independent invitations (each still needs consent).
import { useEffect, useRef, useState, type FormEvent } from "react";
import { errorText, type T } from "../api";
import { useApp } from "../context";
import { useStore } from "../store";
import { Button } from "../ui/Button";
import { Sheet } from "../ui/Sheet";
import { Tag } from "../ui/Tag";
import { Card, Hint, input, PageHead, useLoad } from "./Settings.parts";
import { Confirm } from "./Message.actions";
import { NewGroupForm } from "./NewChat";

export function useTeams(enabled = true) {
  const store = useApp();
  const [view, setView] = useState<T.TeamsView | null>(null);
  const [error, setError] = useState("");
  const seq = useRef(0);
  const reload = async () => {
    const n = ++seq.current;
    try { const v = await store.api.teams(); if (n === seq.current && store.isActive()) { setView(v); setError(""); } }
    catch (e) {
      if (n !== seq.current || !store.isActive()) return;
      const detail = errorText(e), unsupported = /not found|404/i.test(detail);
      setError(unsupported ? "" : detail);
      setView((v) => ({ ...v, tags: v?.tags || false, status: unsupported ? "unsupported" : "unavailable", current: false, reason: detail, teams: v?.teams || [], at: v?.at || "", truncated: v?.truncated || false }));
    }
  };
  useEffect(() => {
    if (!enabled) return;
    void reload();
    const stop = store.host.listen((e) => { if (e.type === "change") void reload(); });
    return () => { ++seq.current; stop(); };
  }, [store, enabled]);
  return { view, error, reload };
}

const personLabel = (o: T.Overview | null, id: string) => o?.person?.person === id ? (o.person.label + " (you)")
  : o?.people?.find((p) => p.person === id)?.label || "Person " + id.slice(0, 8);

function TeamStatus({ view }: { view: T.TeamsView | null }) {
  if (!view) return <Hint>Reading people lists…</Hint>;
  if (view.current && view.status === "available") return null;
  const text: Record<string, string> = { unsupported: "This server does not support people lists.", unavailable: "The server could not be read. These are the last verified people lists.", conflict: "A signed record conflicts. Changes are frozen.", unknown: "People lists are not known yet." };
  return <p role="status" className="text-sm text-muted">{text[view.status] || "Shown as last verified; list records may have changed."}{view.reason && view.status !== "unsupported" && " " + view.reason}{view.at && !view.current && " As of " + view.at + "."}</p>;
}

export function TeamsSection({ titleRef }: { titleRef?: React.Ref<HTMLHeadingElement> }) {
  const store = useApp();
  const o = useStore(store, (s) => s.overview);
  const permissions = useLoad(() => store.api.workspace(), [o?.seq]);
  const { view, error, reload } = useTeams();
  const [query, setQuery] = useState("");
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [chosen, setChosen] = useState<string | null>(null);
  const [draft, setDraft] = useState<T.PersonRef[] | null>(null);
  const [change, setChange] = useState<{ c: T.TeamChange; title: string; hint: string } | null>(null);
  const team = view?.teams?.find((t) => t.id === chosen);
  const act = async (c: T.TeamChange) => {
    if (busy) return;
    setBusy(true);
    const r = await store.run((a) => a.changeTeam(c), "People list changed.");
    if (r && store.isActive()) { if (c.op === "create") { setName(""); setChosen(r.id); } if (c.op === "delete") setChosen(null); await reload(); }
    setBusy(false);
  };
  const create = (e: FormEvent) => { e.preventDefault(); if (name.trim()) void act({ op: "create", v: view?.tags ? 2 : 1, name: name.trim() }); };
  const select = async () => {
    if (!team) return;
    const snapshot = await store.run((a) => a.teamSnapshot([team.id]));
    if (snapshot && store.isActive()) { setChosen(null); setDraft((snapshot.persons || []).filter((p) => p.id !== o?.person?.person)); }
  };
  const confirm = (c: T.TeamChange, title: string, hint: string) => setChange({ c, title, hint });
  return <>
    <PageHead title="People & agent lists" titleRef={titleRef} lead="Shared named sets. Everyone can use a list’s @tag to address its people and agents already in a chat." />
    <div className="space-y-3">
      <TeamStatus view={view} />
      {error && <p role="alert" className="text-danger">{error}</p>}
      {o?.person && view && view.status !== "unsupported" && <form onSubmit={create} className="space-y-2">
        <label className="block font-semibold">New list name<input className={input} value={name} maxLength={64} onChange={(e) => setName(e.target.value)} /></label>
        <Hint>{view.tags ? "Choose its people and agents after creating it. You manage this list; everyone can use it." : "This server supports people lists. Update it to create mixed people and agent tags."}</Hint>
        <Button type="submit" disabled={busy || !name.trim()}>Create list</Button>
      </form>}
      <label className="block font-semibold">Search people lists<input className={input} value={query} onChange={(e) => setQuery(e.target.value)} /></label>
      {(view?.teams || []).filter((t) => t.name.toLowerCase().includes(query.toLowerCase())).sort((a, b) => Number(a.archived) - Number(b.archived) || a.name.localeCompare(b.name)).map((t) => <Card key={t.id} className="p-3">
        <button type="button" className="w-full min-w-0 text-left" onClick={() => { setChosen(t.id); setName(""); }}>
          <span className="block break-words font-bold">{t.name}</span>
          <span className="text-sm">{t.members?.length || 0} people · {t.agents?.length || 0} agents</span>
          <span className="ml-2 inline-flex flex-wrap gap-1">{t.archived && <Tag>Archived</Tag>}{t.conflict && <Tag>Frozen</Tag>}{t.manager ? <Tag>You manage</Tag> : t.member && <Tag>Member</Tag>}</span>
        </button>
      </Card>)}
      {view && !view.teams?.length && view.status !== "unsupported" && <Hint>No people lists yet.</Hint>}
      <Button variant="outline" onClick={() => void reload()}>Read lists again</Button>
    </div>
    <Sheet open={!!team} onOpenChange={(v) => { if (!v) setChosen(null); }} title={team?.name || "People list"}>
      {team && <div className="space-y-3">
        <TeamStatus view={view} />
        {team.conflict && <Hint>Frozen: nothing changes until the conflicting signed record is resolved on the server.</Hint>}
        {team.version === 2 && <TeamTargets team={team} busy={busy} act={act} />}
        <ul aria-label="Members" className="space-y-3">{(team.members || []).map((id) => <li key={id} className="min-w-0 rounded-xl bg-sunken p-3">
          <p className="break-words font-semibold">{personLabel(o, id)} {(team.managers || []).includes(id) && <Tag>Manager</Tag>}</p>
          {team.manager && !team.archived && !team.conflict && (team.version === 2 || id !== o?.person?.person) && <div className="mt-2 flex flex-wrap gap-2">
            <Button size="sm" variant="outline" disabled={busy} onClick={() => confirm({ team: team.id, op: (team.managers || []).includes(id) ? "manager-remove" : "manager-add", target: id }, "Change the manager role?", "Managers rename, delete and manage members. The server refuses removal of the last manager.")}>{(team.managers || []).includes(id) ? "Take manager role" : "Make manager"}</Button>
            <Button size="sm" variant="outline" disabled={busy} onClick={() => confirm({ team: team.id, op: "remove", target: id }, "Remove " + personLabel(o, id) + "?", "Removes them from this list. Their chat membership and permissions stay the same.")}>Remove</Button>
          </div>}
        </li>)}</ul>
        {team.version !== 2 && team.member && team.manager && team.managers?.length === 1 && <Hint>You are the only manager: the server refuses your leaving or losing the role until another manager exists.</Hint>}
        <div className="flex flex-wrap gap-2">
          {team.version !== 2 && o?.person && !team.archived && !team.conflict && <Button disabled={busy} onClick={() => void act({ team: team.id, op: team.member ? "leave" : "join" })}>{team.member ? "Leave list" : "Join list"}</Button>}
          {(team.manager || permissions.data?.can_rename) && !team.conflict && <Button variant="outline" disabled={busy} onClick={() => confirm({ team: team.id, op: "delete" }, "Delete this people list?", "Only the list is deleted. Chats, messages and group membership stay as they are.")}>Delete list</Button>}
          {!team.archived && !team.conflict && !!team.members?.length && <Button variant="act" disabled={busy || !o?.groups} onClick={() => void select()}>Select for a conversation…</Button>}
        </div>
        {team.manager && !team.archived && !team.conflict && <form onSubmit={(e) => { e.preventDefault(); if (name.trim()) void act({ team: team.id, op: "rename", name: name.trim() }); }} className="space-y-2">
          <label className="block font-semibold">New name<input className={input} value={name} maxLength={64} onChange={(e) => setName(e.target.value)} /></label><Button type="submit" disabled={busy || !name.trim()}>Rename list</Button>
        </form>}
      </div>}
    </Sheet>
    <Confirm open={!!change} onOpenChange={(v) => { if (!v) setChange(null); }} title={change?.title || "Change people list?"} ok="Confirm change" onOk={() => { if (change) void act(change.c); }}>
      <p>{change?.hint}</p>
    </Confirm>
    <Sheet open={draft !== null} onOpenChange={(v) => { if (!v) setDraft(null); }} title="New group" description="Review the people. Each receives an independent invitation and must accept.">
      {draft && <NewGroupForm initialPeople={draft} onBack={() => setDraft(null)} onCreated={() => { setDraft(null); setChosen(null); }} />}
    </Sheet>
  </>;
}

function TeamTargets({team,busy,act}: {team:T.TeamView;busy:boolean;act:(c:T.TeamChange)=>Promise<void>}) {
  const store=useApp(),o=useStore(store,s=>s.overview);
  const people=[...(o?.person?[o.person]:[]),...(o?.people||[])].filter(p=>p.person && (p.state==="self"||p.state==="pinned"));
  const persons=[...new Map(people.map(p=>[p.person,p])).values()];
  const hosts=[...new Map(people.flatMap(p=>(p.devices||[]).map(d=>[d.address,{address:d.address,name:p.label+" · "+(d.name||d.address)}] as const))).values()];
  const [person,setPerson]=useState(""),[host,setHost]=useState(""),[agent,setAgent]=useState("");
  const catalog=useLoad(()=>store.api.agents(host),[host]);
  const catalogCurrent=!catalog.loading && catalog.data?.host===(host||o?.me.address);
  const available=catalogCurrent ? catalog.data?.agents||[] : [];
  const record=available.find(a=>a.record.id===agent)?.record;
  const canEdit=team.manager&&!team.archived&&!team.conflict;
  const label=(a:T.TeamAgent)=>catalog.data?.agents?.find(x=>x.record.id===a.id&&x.record.host_key===a.host_key)?.record.label || store.get().agentNames[a.id] || "Agent "+a.id.slice(0,8);
  return <>
    <Hint>Everyone can use @{team.name}. It addresses only selected people and agents already in that chat. Managed by {(team.managers||[]).map(id=>personLabel(o,id)).join(", ")}.</Hint>
    <ul aria-label="Agents in this list" className="space-y-2">{(team.agents||[]).map(a=><li key={a.host+"/"+a.host_key+"/"+a.id} className="rounded-xl bg-sunken p-3">
      <p className="break-words font-semibold">{label(a)}</p><p className="text-sm text-muted">{a.host}</p>
      {canEdit&&<Button size="sm" variant="outline" disabled={busy} onClick={()=>void act({team:team.id,op:"agent-remove",agent:a})}>Remove agent</Button>}
    </li>)}</ul>
    {canEdit&&<fieldset disabled={busy} className="space-y-2 rounded-xl border border-hairline p-3">
      <legend className="px-1 font-semibold">Add to this tag</legend>
      <label className="block">Person<select aria-label="Person" className={input} value={person} onChange={e=>setPerson(e.target.value)}><option value="">Choose a person</option>{persons.filter(p=>!team.members?.includes(p.person!)).map(p=><option key={p.person} value={p.person}>{p.label}</option>)}</select></label>
      <Button size="sm" disabled={!person||busy} onClick={()=>void act({team:team.id,op:"add",target:person}).then(()=>setPerson(""))}>Add person</Button>
      <label className="block">Agent’s device<select aria-label="Agent’s device" className={input} value={host} onChange={e=>{setHost(e.target.value);setAgent("");}}><option value="">This device</option>{hosts.filter(h=>h.address!==o?.me.address).map(h=><option key={h.address} value={h.address}>{h.name}</option>)}</select></label>
      <label className="block">Agent<select aria-label="Agent" className={input} value={agent} disabled={!catalogCurrent} onChange={e=>setAgent(e.target.value)}><option value="">{catalog.loading ? "Reading agents…" : "Choose an agent"}</option>{available.filter(a=>a.enabled&&!team.agents?.some(x=>x.id===a.record.id&&x.host===a.record.host&&x.host_key===a.record.host_key)).map(a=><option key={a.record.id} value={a.record.id}>{a.record.label}</option>)}</select></label>
      {catalog.error&&<p role="alert" className="text-sm text-danger">{catalog.error}</p>}
      <Button size="sm" disabled={!record||busy} onClick={()=>record&&void act({team:team.id,op:"agent-add",agent:{id:record.id,host:record.host,host_key:record.host_key}}).then(()=>setAgent(""))}>Add agent</Button>
    </fieldset>}
  </>;
}

export function TeamPeople({ selected, onChange, excluded = [], disabled = false, showSelected = true }: {
  selected: Pick<T.PersonRef, "id">[]; onChange: (p: Pick<T.PersonRef, "id">[]) => void; excluded?: string[]; disabled?: boolean; showSelected?: boolean;
}) {
  const store = useApp();
  const o = useStore(store, (s) => s.overview);
  const { view, error } = useTeams();
  const [team, setTeam] = useState("");
  const [busy, setBusy] = useState(false);
  const [detail, setDetail] = useState("");
  const [at, setAt] = useState(0);
  const stopped = useRef(false);
  useEffect(() => { stopped.current = false; return () => { stopped.current = true; }; }, []);
  const add = async () => {
    if (!team || busy || disabled || !view?.current) return;
    setBusy(true); setDetail("");
    try {
      const snap = await store.api.teamSnapshot([team]);
      if (stopped.current || !store.isActive()) return;
      const all = new Map(selected.map((p) => [p.id, p]));
      for (const p of snap.persons || []) if (p.id !== o?.person?.person && !excluded.includes(p.id)) all.set(p.id, p);
      onChange([...all.values()]); setAt(snap.at);
    } catch (e) { if (!stopped.current && store.isActive()) setDetail(errorText(e)); }
    finally { if (!stopped.current) setBusy(false); }
  };
  return <fieldset disabled={disabled || busy} className="space-y-2 rounded-xl border border-hairline p-3">
    <legend className="px-1 font-semibold">Add people from a list</legend>
    <TeamStatus view={view} />
    <label className="block text-sm">People list<select className={input} value={team} onChange={(e) => setTeam(e.target.value)}>
      <option value="">Choose a people list</option>{(view?.teams || []).filter((t) => t.listed && !t.archived && !t.conflict).map((t) => <option key={t.id} value={t.id}>{t.name}</option>)}
    </select></label>
    <Button size="sm" type="button" disabled={!team || !view?.current || busy || disabled} onClick={() => void add()}>{busy ? "Reading people…" : "Add list’s people"}</Button>
    {error || detail ? <p role="alert" className="text-sm text-danger">{detail || error}</p> : null}
    {at > 0 && <Hint>Reviewed snapshot at {new Date(at * 1000).toLocaleString()}. Later list changes do not change this selection.</Hint>}
    {showSelected && <ul aria-label="Selected people" className="flex flex-wrap gap-2">{selected.map((p) => <li key={p.id} className="flex min-w-0 items-center gap-2">
      <span className="min-w-0 flex-1 break-words">{personLabel(o, p.id)}</span><Button variant="outline" size="sm" disabled={disabled || busy} onClick={() => onChange(selected.filter((x) => x.id !== p.id))}>Remove {personLabel(o, p.id)}</Button>
    </li>)}</ul>}
    {showSelected && <Hint>This selection grants no access. Only the people you keep are invited, and each decides whether to join.</Hint>}
  </fieldset>;
}
