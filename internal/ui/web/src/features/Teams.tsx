// A team grants no conversation access. Expand a verified snapshot, review
// the people, then send independent invitations (each still needs consent).
import { useEffect, useRef, useState, type FormEvent } from "react";
import { errorText, type T } from "../api";
import { useApp } from "../context";
import { useStore } from "../store";
import { Button } from "../ui/Button";
import { Sheet } from "../ui/Sheet";
import { Tag } from "../ui/Tag";
import { Card, Hint, input, PageHead } from "./Settings.parts";
import { Confirm } from "./Message.actions";
import { NewGroupForm } from "./NewChat";

function useTeams() {
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
      setView((v) => ({ ...v, status: unsupported ? "unsupported" : "unavailable", current: false, reason: detail, teams: v?.teams || [], at: v?.at || "", truncated: v?.truncated || false }));
    }
  };
  useEffect(() => {
    void reload();
    const stop = store.host.listen((e) => { if (e.type === "change") void reload(); });
    return () => { ++seq.current; stop(); };
  }, [store]);
  return { view, error, reload };
}

const personLabel = (o: T.Overview | null, id: string) => o?.person?.person === id ? (o.person.label + " (you)")
  : o?.people?.find((p) => p.person === id)?.label || "Person " + id.slice(0, 8);

function TeamStatus({ view }: { view: T.TeamsView | null }) {
  if (!view) return <Hint>Reading teams…</Hint>;
  if (view.current && view.status === "available") return null;
  const text: Record<string, string> = { unsupported: "This server does not support teams.", unavailable: "The server could not be read. These are the last verified teams.", conflict: "A signed record conflicts. Changes are frozen.", unknown: "Teams are not known yet." };
  return <p role="status" className="text-sm text-muted">{text[view.status] || "Shown as last verified; team records may have changed."}{view.reason && view.status !== "unsupported" && " " + view.reason}{view.at && !view.current && " As of " + view.at + "."}</p>;
}

export function TeamsSection({ titleRef }: { titleRef?: React.Ref<HTMLHeadingElement> }) {
  const store = useApp();
  const o = useStore(store, (s) => s.overview);
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
    const r = await store.run((a) => a.changeTeam(c), "Team change recorded.");
    if (r && store.isActive()) { if (c.op === "create") setName(""); await reload(); }
    setBusy(false);
  };
  const create = (e: FormEvent) => { e.preventDefault(); if (name.trim()) void act({ op: "create", name: name.trim() }); };
  const select = async () => {
    if (!team) return;
    const snapshot = await store.run((a) => a.teamSnapshot([team.id]));
    if (snapshot && store.isActive()) { setChosen(null); setDraft((snapshot.persons || []).filter((p) => p.id !== o?.person?.person)); }
  };
  const confirm = (c: T.TeamChange, title: string, hint: string) => setChange({ c, title, hint });
  return <>
    <PageHead title="Teams" titleRef={titleRef} lead="People on this server. Joining a team grants no access to chats or history." />
    <div className="space-y-3">
      <TeamStatus view={view} />
      {error && <p role="alert" className="text-danger">{error}</p>}
      {o?.person && view && view.status !== "unsupported" && <form onSubmit={create} className="space-y-2">
        <label className="block font-semibold">New team name<input className={input} value={name} maxLength={64} onChange={(e) => setName(e.target.value)} /></label>
        <Hint>You become its first member and manager. Others can join themselves.</Hint>
        <Button type="submit" disabled={busy || !name.trim()}>Create team</Button>
      </form>}
      <label className="block font-semibold">Search teams<input className={input} value={query} onChange={(e) => setQuery(e.target.value)} /></label>
      {(view?.teams || []).filter((t) => t.name.toLowerCase().includes(query.toLowerCase())).sort((a, b) => Number(a.archived) - Number(b.archived) || a.name.localeCompare(b.name)).map((t) => <Card key={t.id} className="p-3">
        <button type="button" className="w-full min-w-0 text-left" onClick={() => { setChosen(t.id); setName(""); }}>
          <span className="block break-words font-bold">{t.name}</span>
          <span className="text-sm">{t.members?.length || 0} members · {t.managers?.length || 0} managers</span>
          <span className="ml-2 inline-flex flex-wrap gap-1">{t.archived && <Tag>Archived</Tag>}{t.conflict && <Tag>Frozen</Tag>}{t.manager ? <Tag>You manage</Tag> : t.member && <Tag>Member</Tag>}</span>
        </button>
      </Card>)}
      {view && !view.teams?.length && view.status !== "unsupported" && <Hint>No teams yet.</Hint>}
      <Button variant="outline" onClick={() => void reload()}>Read teams again</Button>
    </div>
    <Sheet open={!!team} onOpenChange={(v) => { if (!v) setChosen(null); }} title={team?.name || "Team"}>
      {team && <div className="space-y-3">
        <TeamStatus view={view} />
        {team.conflict && <Hint>Frozen: nothing changes until the conflicting signed record is resolved on the server.</Hint>}
        <ul aria-label="Members" className="space-y-3">{(team.members || []).map((id) => <li key={id} className="min-w-0 rounded-xl bg-sunken p-3">
          <p className="break-words font-semibold">{personLabel(o, id)} {(team.managers || []).includes(id) && <Tag>Manager</Tag>}</p>
          {team.manager && !team.archived && !team.conflict && id !== o?.person?.person && <div className="mt-2 flex flex-wrap gap-2">
            <Button size="sm" variant="outline" disabled={busy} onClick={() => confirm({ team: team.id, op: (team.managers || []).includes(id) ? "manager-remove" : "manager-add", target: id }, "Change the manager role?", "Managers rename, archive and manage members. The server refuses removal of the last manager.")}>{(team.managers || []).includes(id) ? "Take manager role" : "Make manager"}</Button>
            <Button size="sm" variant="outline" disabled={busy} onClick={() => confirm({ team: team.id, op: "remove", target: id }, "Remove " + personLabel(o, id) + "?", "They can join again themselves. Nothing else changes for them.")}>Remove</Button>
          </div>}
        </li>)}</ul>
        {team.member && team.manager && team.managers?.length === 1 && <Hint>You are the only manager: the server refuses your leaving or losing the role until another manager exists.</Hint>}
        <div className="flex flex-wrap gap-2">
          {o?.person && !team.archived && !team.conflict && <Button disabled={busy} onClick={() => void act({ team: team.id, op: team.member ? "leave" : "join" })}>{team.member ? "Leave team" : "Join team"}</Button>}
          {team.manager && !team.conflict && <Button variant="outline" disabled={busy} onClick={() => void act({ team: team.id, op: team.archived ? "restore" : "archive" })}>{team.archived ? "Restore team" : "Archive team"}</Button>}
          {!team.archived && !team.conflict && !!team.members?.length && <Button variant="act" disabled={busy || !o?.groups} onClick={() => void select()}>Select for a conversation…</Button>}
        </div>
        {team.manager && !team.archived && !team.conflict && <form onSubmit={(e) => { e.preventDefault(); if (name.trim()) void act({ team: team.id, op: "rename", name: name.trim() }); }} className="space-y-2">
          <label className="block font-semibold">New name<input className={input} value={name} maxLength={64} onChange={(e) => setName(e.target.value)} /></label><Button type="submit" disabled={busy || !name.trim()}>Rename team</Button>
        </form>}
      </div>}
    </Sheet>
    <Confirm open={!!change} onOpenChange={(v) => { if (!v) setChange(null); }} title={change?.title || "Change team?"} ok="Confirm change" onOk={() => { if (change) void act(change.c); }}>
      <p>{change?.hint}</p>
    </Confirm>
    <Sheet open={draft !== null} onOpenChange={(v) => { if (!v) setDraft(null); }} title="New group" description="Review the people. Each receives an independent invitation and must accept.">
      {draft && <NewGroupForm initialPeople={draft} onBack={() => setDraft(null)} onCreated={() => { setDraft(null); setChosen(null); }} />}
    </Sheet>
  </>;
}

export function TeamPeople({ selected, onChange, excluded = [], disabled = false }: {
  selected: T.PersonRef[]; onChange: (p: T.PersonRef[]) => void; excluded?: string[]; disabled?: boolean;
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
    <legend className="px-1 font-semibold">Choose people from teams</legend>
    <TeamStatus view={view} />
    <label className="block text-sm">Team<select className={input} value={team} onChange={(e) => setTeam(e.target.value)}>
      <option value="">Choose a team</option>{(view?.teams || []).filter((t) => t.listed && !t.archived && !t.conflict).map((t) => <option key={t.id} value={t.id}>{t.name}</option>)}
    </select></label>
    <Button size="sm" type="button" disabled={!team || !view?.current || busy || disabled} onClick={() => void add()}>{busy ? "Reading people…" : "Add team’s people"}</Button>
    {error || detail ? <p role="alert" className="text-sm text-danger">{detail || error}</p> : null}
    {at > 0 && <Hint>Reviewed snapshot at {new Date(at * 1000).toLocaleString()}. Later team changes do not change this selection.</Hint>}
    <ul aria-label="Selected team people" className="space-y-1">{selected.map((p) => <li key={p.id} className="flex min-w-0 items-center gap-2">
      <span className="min-w-0 flex-1 break-words">{personLabel(o, p.id)}</span><Button variant="outline" size="sm" disabled={disabled || busy} onClick={() => onChange(selected.filter((x) => x.id !== p.id))}>Remove {personLabel(o, p.id)}</Button>
    </li>)}</ul>
    <Hint>This selection grants no access. Only the people you keep are invited, and each decides whether to join.</Hint>
  </fieldset>;
}
