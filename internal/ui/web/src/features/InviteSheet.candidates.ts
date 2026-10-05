// Who can be brought into a conversation: people known here who are not in
// it yet, and agents their owners publish (plus this computer's own default
// agent). Nothing is suggested by guesswork: a person is marked only for a
// stated local reason, a mention in the recent messages.
import { useEffect, useState } from "react";
import type { Api, T } from "../api";
import { agentName, agentWhere, bringInDevices, deviceKind, personName, runsAgent, timeOf, type DeviceKind } from "../model";
import { isMine, online, speaker } from "./RoomPanel.model";

export type Route = "agent" | "guest" | "group";

export interface Candidate {
  key: string;                 // "p:<person>" | "a:<host>#<agent id or empty>"
  kind: "person" | "agent";
  name: string;
  subtitle: string;
  seed: string;
  online: boolean | null;
  device?: DeviceKind;
  reason?: string;             // "Mentioned by Vitalii · 10:31"
  reasonAt?: string;
  unavailable?: string;        // why it cannot be chosen now, in words
  person?: T.PersonView;
  host?: string;               // the agent's device address
  ownerName?: string;          // whose agent it is, for "waiting for Vitalii’s OK"
  agentId?: string;
  mine?: boolean;
}

const LIVE = new Set(["active", "invited", "pending", "conflict"]);
const isHex32 = (s: string) => /^[0-9a-f]{32}$/.test(s);

/** readCatalog lists the agents a device publishes, refusing records that do not belong to it. */
export async function readCatalog(api: Api, address: string): Promise<T.AgentRecord[]> {
  const v = await api.agents(address);
  const list = v.agents || [];
  if (v.host !== address || list.some((a) => !a.record || a.record.host !== address || !isHex32(a.record.id)))
    throw new Error("The agent list does not match that computer.");
  return list.filter((a) => a.enabled !== false).map((a) => a.record);
}

interface Catalogs { local: T.CatalogAgent[] | null; remote: Record<string, T.AgentRecord[]>; loading: boolean }

/** useCatalogs loads this computer's agents and those published by each device
 *  given, once per opening; an unreachable list just offers nothing from it. */
export function useCatalogs(api: Api, addresses: string[], enabled: boolean): Catalogs {
  const [c, setC] = useState<Catalogs>({ local: null, remote: {}, loading: true });
  const want = addresses.join(" ");
  useEffect(() => {
    if (!enabled) return;
    let alive = true;
    setC((x) => ({ ...x, loading: true }));
    const local = api.agents().then((v) => (v.local ? v.agents || [] : [])).catch(() => [] as T.CatalogAgent[]);
    const remote = Promise.all(addresses.map((a) => readCatalog(api, a).then((r) => [a, r] as const).catch(() => [a, []] as const)));
    Promise.all([local, remote]).then(([l, r]) => { if (alive) setC({ local: l, remote: Object.fromEntries(r), loading: false }); });
    return () => { alive = false; };
  }, [want, enabled]);
  return enabled ? c : { ...c, loading: false };
}

/** The devices whose published agents are worth asking about here: only
 *  devices that run an agent (a phone or a browser publishes none). */
export function catalogHosts(t: T.DMThread, o: T.Overview): string[] {
  const out = new Set<string>();
  const people = [...(t.members || []), t.peer, ...(o.people || []).filter((p) => p.state === "pinned")];
  for (const p of people) {
    if (!p || isMine(o, p.address)) continue;
    for (const d of p.devices?.length ? p.devices : [{ address: p.address }]) if (!isMine(o, d.address) && runsAgent(d.address, o)) out.add(d.address);
  }
  return [...out].sort();
}

export interface Pool { candidates: Candidate[]; peopleNote: string; pick: string }

/** pool lists the candidates for t, people first, each with what an invite
 *  would do. invitations are this computer's group invitations (for "already
 *  invited"); platform is the host's ("browser": no agent of its own to offer). */
export function pool(t: T.DMThread, o: T.Overview, cat: Catalogs, names: Record<string, string>, invitations: T.GroupInvitationView[] = [], platform = "daemon"): Pool {
  const me = o.person;
  const group = t.kind === "group";
  const admin = group && (t.members || []).some((m) => m.person && m.person === me?.person && m.admin);
  const inside = new Set<string>([...(t.members || []), t.peer, me].flatMap((p) => (p?.person ? [p.person] : [])));
  for (const g of t.guests || []) if (LIVE.has(g.state) && g.host.person) inside.add(g.host.person);
  const guestsHere = (t.guests || []).filter((g) => g.state === "active" || g.state === "invited").length;

  // A group's pending invitations: that person is already asked.
  const asked = new Map<string, string>();
  for (const i of invitations) if (group && i.conv === t.id && i.direction === "out" && (i.status === "pending" || i.status === "accepted")) asked.set(i.target, i.status);

  const out: Candidate[] = [];
  let peopleNote = "";
  if (group && !admin) peopleNote = "Only a group admin can add people here. You can still bring in an agent.";
  else {
    for (const p of o.people || []) {
      if (p.state !== "pinned" || !p.person || inside.has(p.person)) continue;
      const on = online(o, p.address);
      const was = asked.get(p.person);
      out.push({
        key: "p:" + p.person, kind: "person", name: personName(p), seed: p.person, online: on, person: p,
        subtitle: [group && "Becomes a member", on === true ? "Online" : on === false ? "Offline" : ""].filter(Boolean).join(" · ").replace(" · O", " · o"),
        unavailable: was === "pending" ? "Invited · waiting for them to accept" : was ? "Accepted · not added yet"
          : !group && guestsHere >= 16 ? "This chat already has 16 guests." : undefined,
      });
    }
  }

  if (o.agents && !cat.loading) { // agents appear together, once every list is in
    // One row per agent (its device and id; a device's default agent has no id), none already here.
    const seen = new Set((t.agents || []).filter((a) => LIVE.has(a.state)).map((a) => a.host.address + "#" + (a.agent_id || "")));
    const add = (c: Candidate) => { const k = c.host + "#" + (c.agentId || ""); if (!seen.has(k)) { seen.add(k); out.push(c); } };
    // This computer's named agents, then its default agent, which is always offered next to them.
    const self: T.PersonView = me || { person: "this computer", label: "", address: o.me.address, state: "" }; // "Your agent"
    for (const a of (cat.local || []).filter((a) => a.enabled)) add({
      key: "a:" + a.record.host + "#" + a.record.id, kind: "agent", name: a.record.label || agentName(a.record.id, names, self, self), seed: a.record.id,
      subtitle: agentWhere(self, a.record.host, self), online: true, device: deviceKind(a.record.host),
      host: a.record.host, agentId: a.record.id, mine: true,
      unavailable: a.responder && !a.responder.ready ? "Not set up on this computer yet." : undefined,
    });
    if (platform !== "browser") add({ // a browser runs no agent: nothing of its own to bring in
      key: "a:" + o.me.address + "#", kind: "agent", name: agentName(undefined, names, self, self), seed: o.me.address,
      subtitle: agentWhere(self, o.me.address, self), online: true, device: deviceKind(o.me.address), host: o.me.address, mine: true,
      unavailable: o.me.responder ? undefined : "Not set up on this computer yet.",
    });
    // Members' agents: each published agent, or a device's own default agent
    // when it runs one and publishes none (model.bringInDevices).
    const members = (group ? t.members || [] : [t.peer]).filter((p) => p && !isMine(o, p.address));
    const known = new Map<string, T.PersonView>();
    for (const p of [...members, ...(o.people || [])]) for (const d of p.devices?.length ? p.devices : [{ address: p.address }]) if (!known.has(d.address)) known.set(d.address, p);
    for (const [address, records] of Object.entries(cat.remote)) {
      const p = known.get(address);
      if (!p) continue;
      for (const r of records) add({
        key: "a:" + address + "#" + r.id, kind: "agent", name: r.label || agentName(r.id, names, p, me), seed: r.id,
        subtitle: agentLine(p, me, address, online(o, address)), online: online(o, address), device: deviceKind(address), host: address, agentId: r.id, ownerName: personName(p),
      });
    }
    for (const p of members) {
      for (const address of bringInDevices(p, o, cat.remote)) add({
        key: "a:" + address + "#", kind: "agent", name: agentName(undefined, names, p, me), seed: address,
        subtitle: agentLine(p, me, address, online(o, address)), online: online(o, address), device: deviceKind(address), host: address, ownerName: personName(p),
      });
    }
  }

  // A stated local reason: a person named in the last messages here.
  const recent = (t.messages || []).filter((m) => !m.event && !m.deleted).slice(-20).reverse();
  let pick = "";
  for (const c of out) {
    if (c.kind !== "person" || !c.person) continue;
    const m = recent.find((m) => mentions(m.text ?? m.body, c.person!));
    if (!m) continue;
    const who = speaker(m, t, o, names);
    c.reason = "Mentioned by " + (who.me ? "you" : who.name) + " · " + timeOf(m.at);
    c.reasonAt = m.at;
    if (!c.unavailable && (!pick || (out.find((x) => x.key === pick)?.reasonAt || "") < m.at)) pick = c.key;
  }
  return { candidates: out, peopleNote, pick };
}

const agentLine = (p: T.PersonView, me: T.PersonView | undefined, address: string, on: boolean | null) =>
  agentWhere(p, address, me) + (on === true ? " · online" : on === false ? " · offline" : "");

const escape = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");

/** mentions: an exact @mention of this person, or their name as a whole word. */
function mentions(text: string | undefined, p: T.PersonView): boolean {
  if (!text) return false;
  if (text.includes("agentnet:person/" + p.person + ")")) return true;
  const name = (p.label || "").trim();
  return name.length >= 2 && new RegExp("(?<![\\p{L}\\p{N}])" + escape(name) + "(?![\\p{L}\\p{N}])", "iu").test(text);
}

/** routeFor: which invitation a candidate takes in t. */
export const routeFor = (c: Candidate, t: T.DMThread): Route => (c.kind === "agent" ? "agent" : t.kind === "group" ? "group" : "guest");

/** How many earlier messages each invitation may carry. */
export const capFor = (r: Route) => (r === "group" ? 64 : 200);

// ---- who may give an invited agent tasks without asking (AgentInvite.tasks_from) ----

/** The most member keys one invitation may name (protocol.MaxTaskKeys; pinned by TestComicParityRoutes). */
export const TASK_KEYS_MAX = 16;

/** One person who may be allowed to give the agent tasks without asking: their
 *  member keys in this conversation, the only keys the invitation may name.
 *  name is the row's label ("You" for yourself); face is the person's own
 *  name, for their avatar as drawn everywhere else. */
export interface TaskPerson { key: string; name: string; face: string; seed: string; me: boolean; keys: string[] }

/** taskPeople: the conversation's members, one row each (never a device): in a
 *  two-person chat you and them, in a group its members. Their keys are the
 *  devices their signed records list now; a device added later is not covered. */
export function taskPeople(t: T.DMThread, o: T.Overview): TaskPerson[] {
  const me = o.person;
  const holders: T.PersonView[] = t.kind === "group" ? t.members || [] : [me, t.peer].filter((p): p is T.PersonView => !!p);
  const seen = new Set<string>();
  const out: TaskPerson[] = [];
  for (const p of holders) {
    if (!p.person || seen.has(p.person)) continue;
    seen.add(p.person);
    const keys = [...new Set((p.devices?.length ? p.devices.map((d) => d.fingerprint) : [p.fingerprint || ""]).filter(Boolean))];
    const mine = !!me?.person && p.person === me.person;
    out.push({ key: p.person, name: mine ? "You" : personName(p), face: personName(p), seed: p.person, me: mine, keys });
  }
  return out.sort((a, b) => Number(b.me) - Number(a.me));
}

/** taskKeys: the member keys of the people chosen, each once. */
export const taskKeys = (people: TaskPerson[], chosen: Set<string>) =>
  [...new Set(people.filter((p) => chosen.has(p.key)).flatMap((p) => p.keys))];
