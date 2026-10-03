// Agents: yours (this computer's agent, named agents, what they may do
// without asking you) and other people's agents seen in your chats. It says
// only what the daemon proves: a program is installed, never that it is
// signed in or working; a permission holds only as the server lists it.
import { useEffect, useState, type ReactNode } from "react";
import { Switch } from "@base-ui/react/switch";
import { IconChevronRight, IconDevices, IconPlus, IconSettings } from "@tabler/icons-react";
import { errorText, type T } from "../api";
import { useAgentNames, useApp } from "../context";
import { agentName, agentWhere, deviceKind, niceDevice, personName } from "../model";
import { useStore } from "../store";
import { AgentAvatar } from "../ui/Avatar";
import { Button } from "../ui/Button";
import { Tag } from "../ui/Tag";
import { ScreenTitle } from "./Approvals.title";
import { ConfirmSheet, Details, Row } from "./Approvals.sheets";
import { capital, deviceWords, isMine } from "./Approvals.words";
import { AgentSheet, type AgentSetup } from "./AgentsView.forms";
import { latestThreads, Permissions, useGrants } from "./AgentsView.grants";
import { eventKind } from "./Message.model";

type Load<V> = { v?: V; error?: string };

function useAgentData(o: T.Overview | null) {
  const store = useApp();
  const browser = store.host.platform === "browser";
  const [responder, setResponder] = useState<Load<T.ResponderView>>({});
  const [catalog, setCatalog] = useState<Load<T.AgentCatalogView>>({});
  const seq = o?.seq;
  useEffect(() => {
    if (browser || !o) return;
    let alive = true;
    store.api.responder().then((v) => alive && setResponder({ v }), (e) => alive && setResponder({ error: errorText(e) }));
    store.api.agents().then((v) => alive && setCatalog({ v }), (e) => alive && setCatalog({ error: errorText(e) }));
    return () => { alive = false; };
  }, [seq, browser]);
  return { browser, responder, catalog };
}

export function AgentsView() {
  const store = useApp();
  const o = useStore(store, (s) => s.overview);
  const data = useAgentData(o);
  const grants = useGrants();
  if (!o) return <div className="px-4 pt-5" aria-busy="true"><div className="h-7 w-28 rounded-lg bg-sunken" /><div className="mt-6 h-40 rounded-2xl border-[1.5px] border-hairline motion-safe:animate-pulse" /></div>;
  return (
    <div className="pb-10">
      <header className="px-4 pt-5 pb-1">
        <ScreenTitle>Agents</ScreenTitle>
        <p className="pt-2 text-[15px] font-semibold text-text-2">Yours, and the ones you work with</p>
      </header>

      <Section title="Your agents">
        {data.browser ? <BrowserNote /> : <MyAgent o={o} r={data.responder} />}
        <OtherDevices o={o} />
        {!data.browser && <NamedAgents o={o} catalog={data.catalog} />}
      </Section>

      <ReportsNote o={o} />

      {!data.browser && <Section title="What they may do without asking"><Permissions grants={grants} /></Section>}

      <Section title="Other people’s agents"><Others o={o} /></Section>
    </div>
  );
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="mt-6 px-4" aria-label={title}>
      <h2 className="pb-2.5 text-[13px] font-extrabold uppercase tracking-[0.07em] text-muted">{title}</h2>
      <div className="flex flex-col gap-3">{children}</div>
    </section>
  );
}

// ---- this computer's agent ----------------------------------------------------

function MyAgent({ o, r }: { o: T.Overview; r: Load<T.ResponderView> }) {
  const store = useApp();
  const v = r.v;
  const harness = v?.harness || o.me.responder;
  const state: "ready" | "problem" | "manual" | "unset" | "loading" = !v ? (r.error ? "problem" : "loading")
    : !v.chosen ? "unset" : v.manual ? "manual" : v.ready ? "ready" : "problem";
  const found = (v?.harnesses || []).find((h) => h.name === harness);
  const chats = (o.person?.agents || []).filter((a) => a.address === o.me.address && !a.agent_id).flatMap((a) => a.dms || []).filter((d) => d.state === "active");
  return (
    <article className="rounded-2xl bg-surface p-4 stroke shadow-pop-sm">
      <div className="flex items-center gap-3.5">
        <AgentAvatar seed={o.me.address} size={56} mood={state === "ready" ? "neutral" : "asleep"} device={deviceKind(o.me.address)} />
        <div className="min-w-0 flex-1">
          <p className="font-display text-[20px] font-bold leading-tight">Your agent</p>
          <p className="truncate text-[14px] font-medium text-text-2">On {niceDevice(o.me.address)}{harness && state !== "manual" && state !== "unset" ? " · " + capital(harness) : ""}</p>
        </div>
      </div>
      <p className="pt-3 flex items-center gap-2 text-[15px] font-bold">
        <Dot tone={state === "ready" ? "ok" : state === "problem" ? "bad" : "off"} />
        {{ ready: "Ready", problem: "Not ready", manual: "No automatic answers", unset: "Not set up yet", loading: "Checking…" }[state]}
      </p>
      <p className="pt-1 text-[15px] text-text-2">
        {state === "ready" && "Answers the questions you approve and does the tasks you allow."}
        {state === "problem" && (v?.problem || r.error || "Check its program and folder in Settings.")}
        {state === "manual" && "Every question and task waits for you."}
        {state === "unset" && "Until you choose a program for it, every question and task waits for you."}
      </p>
      {found && state !== "manual" && state !== "unset" && (
        <p className="pt-1 text-[13px] text-muted">{found.found ? capital(found.name) + " is installed here. Whether it’s signed in shows the first time it works." : capital(found.name) + " isn’t installed where AgentNet can find it."}</p>
      )}
      {chats.length > 0 && <Chats o={o} dms={chats} />}
      {v?.chosen && !v.manual && (
        <Details>
          <Row k="Works in"><span className="font-mono text-[14px] [overflow-wrap:anywhere]">{v.dir}</span></Row>
          {found?.path && <Row k="Program"><span className="font-mono text-[14px] [overflow-wrap:anywhere]">{found.path}</span></Row>}
          {!!v.timeout_seconds && <Row k="Time limit">{Math.round(v.timeout_seconds / 60)} min per question or task</Row>}
          {(v.context || []).length > 0 && <Row k="Always given">{(v.context || []).join(", ")}</Row>}
        </Details>
      )}
      <div className="mt-3 flex flex-wrap gap-2">
        <Button variant={state === "unset" || state === "problem" ? "act" : "outline"} size="sm" icon={<IconSettings size={18} />} onClick={() => store.showTab("settings", "assistant")}>
          {state === "unset" ? "Set it up in Settings" : "Change in Settings"}
        </Button>
      </div>
    </article>
  );
}

function BrowserNote() {
  return (
    <article className="rounded-2xl bg-surface p-4 stroke">
      <p className="font-bold">This browser doesn’t run agents</p>
      <p className="pt-1 text-[15px] text-text-2">Questions and tasks sent to you here wait for you. An agent runs on a computer with AgentNet installed.</p>
    </article>
  );
}

// ---- named agents on this computer ------------------------------------------------

function NamedAgents({ o, catalog }: { o: T.Overview; catalog: Load<T.AgentCatalogView> }) {
  const store = useApp();
  const [sheet, setSheet] = useState<null | { agent?: T.CatalogAgent }>(null);
  const [off, setOff] = useState<T.CatalogAgent | null>(null);
  const [unshared, setUnshared] = useState(false);
  const c = catalog.v;
  if (catalog.error) return <p className="px-1 text-[14px] text-muted">Named agents aren’t available here: {catalog.error}</p>;
  if (!c?.local) return null;
  const agents = c.agents || [];
  const change = async (body: T.AgentCatalogChange, ok: string) => {
    const r = await store.run((api) => api.changeAgents(body));
    if (!r) return false;
    setUnshared(!r.published);
    store.toast(r.published ? ok : ok + " Others can’t see the change yet.", r.published ? "ok" : undefined);
    return true;
  };
  const save = (s: AgentSetup) => sheet?.agent
    ? change({ action: "update", id: sheet.agent.record.id, harness: s.harness, dir: s.dir }, sheet.agent.record.label + " is on.")
    : change({ action: "create", label: s.label, harness: s.harness, dir: s.dir }, s.label + " is ready.");
  return (
    <>
      {agents.map((a) => <NamedAgent key={a.record.id} a={a} o={o} onToggle={(on) => (on ? setSheet({ agent: a }) : setOff(a))} />)}
      {unshared && (
        <p className="flex flex-wrap items-center gap-x-3 rounded-2xl bg-guest-bg px-3.5 py-2 text-[14px] font-semibold text-guest-ink">
          Saved here, but others can’t see the change yet.
          <button type="button" className="min-h-11 font-bold underline underline-offset-2" onClick={() => change({ action: "publish" }, "Shared.")}>Share again</button>
        </p>
      )}
      <button type="button" onClick={() => setSheet({})}
        className="flex min-h-14 items-center gap-3 rounded-2xl border-[1.5px] border-dashed border-ink/40 px-4 text-left font-bold hover:bg-sunken">
        <span className="grid size-9 place-items-center rounded-xl bg-agent text-agent-ink" aria-hidden="true"><IconPlus size={20} /></span>
        <span className="flex-1">
          New named agent
          {!agents.length && <span className="block text-[13px] font-medium text-muted">Its own name, program and folder, so others can pick it</span>}
        </span>
      </button>
      <AgentSheet open={!!sheet} onOpenChange={(v) => !v && setSheet(null)} harnesses={c.harnesses || []} agent={sheet?.agent} onSave={save} />
      <ConfirmSheet open={!!off} onOpenChange={(v) => !v && setOff(null)} title={"Turn off " + (off?.record.label || "") + "?"}
        body="It stops taking questions and tasks on this computer. Earlier messages stay. You can turn it on again."
        confirm="Turn off" tone="danger" onConfirm={() => void change({ action: "disable", id: off!.record.id }, (off?.record.label || "It") + " is off.")} />
    </>
  );
}

function NamedAgent({ a, o, onToggle }: { a: T.CatalogAgent; o: T.Overview; onToggle: (on: boolean) => void }) {
  const r = a.responder;
  const chats = (o.person?.agents || []).filter((x) => x.address === o.me.address && x.agent_id === a.record.id).flatMap((x) => x.dms || []).filter((d) => d.state === "active");
  const status = !a.enabled ? "Turned off" : !r ? "On" : r.ready ? capital(r.harness || "") + " · Ready" : capital(r.harness || "") + " · Not ready";
  return (
    <article className="rounded-2xl bg-surface p-3.5 stroke">
      <div className="flex items-center gap-3">
        <AgentAvatar seed={a.record.id} size={48} mood={a.enabled && r?.ready ? "neutral" : "asleep"} />
        <div className="min-w-0 flex-1">
          <p className="truncate text-[17px] font-bold leading-tight">{a.record.label}</p>
          <p className="pt-0.5 flex items-center gap-1.5 text-[14px] text-text-2"><Dot tone={!a.enabled ? "off" : r?.ready ? "ok" : "bad"} />{status}</p>
        </div>
        <label className="flex min-h-11 cursor-pointer items-center gap-2 pl-2">
          <span className="sr-only">{a.record.label} is {a.enabled ? "on" : "off"}</span>
          <Switch.Root checked={a.enabled} onCheckedChange={(on) => onToggle(on)}
            className="relative flex h-7 w-12 shrink-0 items-center rounded-full bg-sunken p-0.5 stroke transition-colors duration-200 ease-out-soft data-[checked]:bg-ok-ink before:absolute before:-inset-2 before:content-['']">
            <Switch.Thumb className="size-5 rounded-full bg-surface stroke transition-transform duration-200 ease-out-soft data-[checked]:translate-x-5" />
          </Switch.Root>
        </label>
      </div>
      {a.enabled && r && !r.ready && r.problem && <p className="pt-2 text-[14px] text-danger">{r.problem}</p>}
      {chats.length > 0 && <Chats o={o} dms={chats} />}
      {r && (
        <Details>
          <Row k="Works in"><span className="font-mono text-[14px] [overflow-wrap:anywhere]">{r.dir}</span></Row>
          <Row k="Program">{capital(r.harness || "")}</Row>
        </Details>
      )}
    </article>
  );
}

// ---- your other devices -------------------------------------------------------------

// Your agents on your other devices that this one talks with. A device that
// only sends reports here is not one: it is in ReportsNote.
function OtherDevices({ o }: { o: T.Overview }) {
  const store = useApp();
  const mine = latestThreads(o).filter((t) => isMine(t.peer, o) && t.peer !== o.me.address && !t.notice_only);
  if (!mine.length) return null;
  return (
    <ul className="flex flex-col gap-2">
      {mine.map((t) => (
        <li key={t.peer}>
          <LinkRow onOpen={() => void store.open({ kind: "thread", id: t.id })} seed={t.peer} device={deviceKind(t.peer)}
            title="Your agent" sub={"On " + niceDevice(t.peer)} />
        </li>
      ))}
    </ul>
  );
}

// ReportsNote: devices that tell this one when something waits for a person
// there. Their reports are in OKs; deciding happens on that device.
function ReportsNote({ o }: { o: T.Overview }) {
  const store = useApp();
  const from = latestThreads(o).filter((t) => t.notice_only).map((t) => capital(deviceWords(t.peer, o)));
  if (!from.length) return null;
  const who = from.length === 1 ? from[0] + " tells" : from.slice(0, -1).join(", ") + " and " + from[from.length - 1] + " tell";
  return (
    <Section title="Reports">
      <article className="flex items-start gap-3 rounded-2xl border-[1.5px] border-dashed border-ink/30 px-4 py-3">
        <IconDevices size={22} className="mt-0.5 shrink-0 text-text-2" aria-hidden="true" />
        <div className="min-w-0 flex-1">
          <p className="text-[15px] text-text-2">{who} this computer when something waits for a person there. The reports show in OKs; you decide on that device.</p>
          <Button variant="outline" size="sm" className="mt-2" onClick={() => store.showTab("oks")}>See them in OKs</Button>
        </div>
      </article>
    </Section>
  );
}

// ---- other people's agents -------------------------------------------------------------

interface Other { key: string; seed: string; address: string; agentId?: string; owner: T.PersonView | null; open: () => void; where: string; state: string; conv: string; pid: string }

function Others({ o }: { o: T.Overview }) {
  const store = useApp();
  const names = useAgentNames();
  const me = o.person;
  const dmTitle = (conv: string) => {
    const d = (o.dms || []).find((x) => x.id === conv);
    return !d ? "a chat" : d.kind === "group" ? d.title || "a group" : "your chat with " + personName(d.peer);
  };
  const rows: Other[] = [];
  for (const p of o.people || []) {
    for (const a of p.agents || []) {
      const dms = a.dms || [];
      const live = dms.find((d) => d.state === "active") || dms[0];
      rows.push({
        key: a.address + "#" + (a.agent_id || ""), seed: a.agent_id || a.address, address: a.address, agentId: a.agent_id, owner: p,
        open: () => live && void store.open({ kind: "dm", id: live.conv }),
        where: live ? "In " + dmTitle(live.conv) : "", state: live?.state || "", conv: live?.conv || "", pid: live?.pid || "",
      });
    }
  }
  for (const t of latestThreads(o).filter((t) => !t.notice_only && !isMine(t.peer, o) && !rows.some((r) => r.address === t.peer && !r.agentId))) {
    const p = (o.people || []).find((x) => x.address === t.peer || (x.devices || []).some((d) => d.address === t.peer)) || null;
    rows.push({ key: t.peer, seed: t.peer, address: t.peer, owner: p, open: () => void store.open({ kind: "thread", id: t.id }), where: "Talks with your agent directly", state: "active", conv: "", pid: "" });
  }
  const joined = useEverJoined(rows.filter((r) => r.state === "dismissed"));
  if (!rows.length) {
    return <p className="rounded-2xl border-[1.5px] border-dashed border-ink/25 px-4 py-4 text-[15px] text-text-2">No one else’s agents yet. When someone brings their agent into a chat with you, it shows up here.</p>;
  }
  return (
    <ul className="flex flex-col gap-2">
      {rows.map((r) => {
        const named = !!r.agentId && !!names[r.agentId];
        const name = r.owner ? agentName(r.agentId, names, r.owner, me) : named ? names[r.agentId!] : niceDevice(r.address);
        const sub = named && r.owner ? agentWhere(r.owner, r.address, me) : "On " + niceDevice(r.address);
        const tag = stateTag(r.state, joined[r.pid]);
        return (
          <li key={r.key}>
            <LinkRow onOpen={r.open} seed={r.seed} device={deviceKind(r.address)} title={name} sub={sub}
              extra={r.where && <span className="mt-1 flex flex-wrap items-center gap-x-1.5 gap-y-1 text-[13px] text-muted">{r.state !== "active" && tag && <span className="shrink-0 whitespace-nowrap"><Tag tone="muted">{tag}</Tag></span>}<span className="min-w-0">{r.where}</span></span>} />
          </li>
        );
      })}
    </ul>
  );
}

// An agent dismissed from a chat either joined first (it left) or was
// dismissed while still invited (its invite was cancelled). The overview
// doesn't say which; the chat's own participation records do.
function useEverJoined(rows: { conv: string; pid: string }[]): Record<string, boolean | undefined> {
  const store = useApp();
  const seq = useStore(store, (s) => s.overview?.seq);
  const [joined, setJoined] = useState<Record<string, boolean | undefined>>({});
  const key = rows.map((r) => r.conv + "/" + r.pid).join(",");
  useEffect(() => {
    let alive = true;
    const convs = [...new Set(rows.map((r) => r.conv).filter(Boolean))];
    Promise.all(convs.map((c) => store.api.dm(c).catch(() => null))).then((ts) => {
      if (!alive) return;
      const next: Record<string, boolean | undefined> = {};
      for (const r of rows) {
        const t = ts[convs.indexOf(r.conv)];
        if (t) next[r.pid] = (t.messages || []).some((m) => m.pid === r.pid && !!m.event && eventKind(m.event)?.kind === "joined");
      }
      setJoined(next);
    });
    return () => { alive = false; };
  }, [key, seq]);
  return joined;
}

/** The tag for an agent not in a chat now; a dismissal reads only once it is known whether it joined. */
const stateTag = (s: string, joined?: boolean) =>
  s === "dismissed" ? (joined === undefined ? "" : joined ? "Left" : "Invite cancelled")
    : ({ invited: "Invited", pending: "Joining", declined: "Declined", conflict: "Unclear" } as Record<string, string>)[s] || s;

// ---- small pieces -----------------------------------------------------------------

function LinkRow({ onOpen, seed, device, title, sub, extra }: { onOpen: () => void; seed: string; device?: "laptop" | "server" | "phone"; title: string; sub: string; extra?: ReactNode }) {
  return (
    <button type="button" onClick={onOpen} className="press flex w-full items-center gap-3 rounded-2xl bg-surface p-3 text-left stroke hover:bg-sunken">
      <AgentAvatar seed={seed} size={40} device={device} />
      <span className="min-w-0 flex-1">
        <span className="block truncate font-bold leading-tight">{title}</span>
        <span className="block truncate text-[13px] font-semibold text-agent-ink">{sub}</span>
        {extra}
      </span>
      <IconChevronRight size={20} className="shrink-0 text-muted" aria-hidden="true" />
    </button>
  );
}

function Chats({ o, dms }: { o: T.Overview; dms: T.AgentInDM[] }) {
  const store = useApp();
  return (
    <div className="mt-3">
      <p className="text-[13px] font-bold text-muted">Helping in</p>
      <div className="mt-1 flex flex-wrap gap-2">
        {dms.map((d) => {
          const s = (o.dms || []).find((x) => x.id === d.conv);
          const title = !s ? "A chat" : s.kind === "group" ? s.title || "A group" : personName(s.peer);
          return (
            <button key={d.pid} type="button" onClick={() => void store.open({ kind: "dm", id: d.conv })}
              aria-label={"Open " + (s && s.kind !== "group" ? "your chat with " : "") + title}
              className="inline-flex min-h-11 max-w-full items-center gap-1 rounded-full bg-agent px-3.5 text-[14px] font-bold text-agent-ink hover:brightness-95"><span className="truncate">
              {title}</span><IconChevronRight size={16} className="shrink-0" aria-hidden="true" />
            </button>
          );
        })}
      </div>
    </div>
  );
}

function Dot({ tone }: { tone: "ok" | "bad" | "off" }) {
  // Shape carries the meaning as well as colour: filled when ready, a ring when not.
  const cls = tone === "ok" ? "bg-online" : tone === "bad" ? "bg-danger-bg [box-shadow:inset_0_0_0_2.5px_var(--an-danger)]" : "[box-shadow:inset_0_0_0_2px_var(--an-muted)]";
  return <span aria-hidden="true" className={"inline-block size-3 shrink-0 rounded-full " + cls} />;
}
