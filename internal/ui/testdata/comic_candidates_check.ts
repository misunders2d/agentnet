// Synthetic source regressions: no requests, real people, sessions or grants.
import { agentTargets, decode, encode, shift, candidates } from "../web/src/features/Composer.mentions";
import { catalogHosts, pool, routeFor } from "../web/src/features/InviteSheet.candidates";
import { agentRejoinState, pendingGroupInvites } from "../web/src/features/RoomPanel.model";
import type { T } from "../web/src/api";

const check = (ok: boolean, message: string) => { if (!ok) throw new Error(message); };
const me = { person: "me", label: "Me", address: "me/phone", state: "self", devices: [{ address: "me/phone" }, { address: "me/desk" }] };
const peer = { person: "peer", label: "Morgan Vale", address: "peer/desk", state: "pinned", devices: [{ address: "peer/desk" }] };
const overview = { person: me, people: [peer], agents: true, agent_devices: ["me/desk", "peer/desk"], me: { address: "me/phone", agent: false } } as unknown as T.Overview;
const thread = { id: "chat", role: "member", peer, agents: [], guests: [], messages: [] } as unknown as T.DMThread;
check(candidates(thread, overview, {}).some(c => c.kind === "person" && c.id === "peer"), "Verified DM peer missing from mentions");
const thin = { ...thread, peer: { label: "Morgan Vale", address: "peer/desk", state: "" } } as T.DMThread;
check(candidates(thin, overview, {}).some(c => c.id === "peer"), "Exact-device verified peer fallback missing");
check(!candidates(thin, { ...overview, people: [{ ...peer, person: undefined, state: "listed" }] }, {}).length, "Unverified directory claim became person mention");
check(!candidates({ ...thread, peer: me } as T.DMThread, overview, {}).length, "Local self became DM mention");
check(catalogHosts(thread, overview).includes("me/desk"), "Own other device's agents never requested");
check(!catalogHosts(thread, overview).includes("me/phone"), "Browser/phone offered as agent host");
const ownAgent = { id: "00000000000000000000000000000001", host: "me/desk", label: "My helper" } as T.AgentRecord;
const found = pool(thread, overview, { local: [], remote: { "me/desk": [ownAgent], "peer/desk": [] }, loading: false }, {}, [], "browser");
check(found.candidates.some(c => c.host === "me/desk" && c.agentId === ownAgent.id && c.mine), "Own published agent missing from browser picker");
check(!found.candidates.some(c => c.host === "me/phone"), "Browser's nonexistent default agent offered");
check(!catalogHosts(thread, { ...overview, agent_devices: ["peer/desk"] }).includes("me/desk"), "Non-agent capability bypassed");
const group = { ...thread, kind: "group", peer: undefined, members: [{ ...me, admin: false }], guests: [] } as unknown as T.DMThread;
const catalog = { local: [], remote: {}, loading: false };
const guestPool = pool(group, overview, catalog, {}, [], "browser");
const guest = guestPool.candidates.find(c => c.person?.person === peer.person);
check(!!guest && guest.subtitle.startsWith("Joins as a guest"), "Non-admin group member cannot bring in a guest");
check(routeFor(guest!, group) === "guest", "Default group human invite silently grants permanent membership");
check(!pool(group, overview, catalog, {}, [], "browser", "member").candidates.some(c => c.kind === "person"), "Non-admin offered permanent membership invite");
const adminGroup = { ...group, members: [{ ...me, admin: true }] } as unknown as T.DMThread;
const member = pool(adminGroup, overview, catalog, {}, [], "browser", "member").candidates.find(c => c.person?.person === peer.person);
check(!!member && routeFor(member, adminGroup, "member") === "group", "Explicit admin membership invite lost");
check(routeFor({ ...guest!, kind: "agent" }, group) === "agent", "Guest choice changes agent invitation authority");
const invitations = [
  { id: "old", conv: group.id, direction: "out", target: peer.person, status: "stale", can_refresh: true },
  { id: "fresh", conv: group.id, direction: "out", target: peer.person, status: "pending", can_cancel: true, can_refresh: true },
] as unknown as T.GroupInvitationView[];
const pending = pendingGroupInvites(group, { ...overview, group_invitations: invitations });
check(pending.length === 1 && pending[0].id === "fresh" && pending[0].canCancel, "Old stale invitation hides current retract action");
check(pendingGroupInvites(group, { ...overview, group_invitations: [{ ...invitations[1], status: "cancelled" }] }).length === 0, "Cancelled invitation still shown pending");
console.log("PASS Comic candidates: verified peer mentions, listed-claim refusal, own remote agents and capability limits");

const addressed = decode("[@Same](agentnet:agent/pidA) and [@Same](agentnet:agent/pidB) then [@Same](agentnet:agent/pidA)");
check(JSON.stringify(agentTargets(addressed.text,addressed.spans)) === JSON.stringify(["pidA","pidB"]), "Multiple exact targets deduplicate by PID, not name");
check(agentTargets("@Same",[]).length===0,"Plain labels cannot route");
check(agentTargets("",[],"legacyPID")[0]==="legacyPID","Legacy single target retained");
const removed=addressed.text.replace(" and @Same"," and someone"), kept=shift(addressed.spans,addressed.text,removed).kept;
check(JSON.stringify(agentTargets(removed,kept))===JSON.stringify(["pidA"]),"Removing one exact target preserves the other");
const reopened=decode(encode(addressed.text,addressed.spans));
check(JSON.stringify(agentTargets(reopened.text,reopened.spans))===JSON.stringify(["pidA","pidB"]),"Draft reload preserves multiple targets");

const endedAgent = { pid: "past", state: "dismissed", agent_id: "codex", host: { address: "peer/desk", fingerprint: "keyA" } };
const nextAgent = { ...endedAgent, pid: "new", state: "invited" };
const rejoinThread = (a: object[]) => ({ ...thread, agents: a }) as unknown as T.DMThread;
check(agentRejoinState(rejoinThread([endedAgent]), "past") === "", "Ended-only agent cannot return");
check(agentRejoinState(rejoinThread([endedAgent,nextAgent]), "past") === "Rejoin pending", "Pending rejoin leaves stale action enabled");
check(agentRejoinState(rejoinThread([nextAgent,endedAgent]), "past") === "Rejoin pending", "Reordered participation changes identity");
check(agentRejoinState(rejoinThread([endedAgent,{...nextAgent,state:"active"}]), "past") === "Already in this chat", "Active rejoin leaves stale action enabled");
check(agentRejoinState(rejoinThread([endedAgent,{...nextAgent,agent_id:"claude"}]), "past") === "", "Different named agent incorrectly grouped");
check(agentRejoinState(rejoinThread([endedAgent,{...nextAgent,host:{...nextAgent.host,fingerprint:"keyB"}}]), "past") === "", "Changed host key incorrectly grouped");
check(agentRejoinState(rejoinThread([endedAgent,{...nextAgent,state:"dismissed"}]), "past") === "", "Ended rejoin blocks a new invitation");
