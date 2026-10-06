// Synthetic source regressions: no requests, real people, sessions or grants.
import { candidates } from "../web/src/features/Composer.mentions";
import { catalogHosts, pool } from "../web/src/features/InviteSheet.candidates";
import type { T } from "../web/src/api";

const check = (ok: boolean, message: string) => { if (!ok) throw new Error(message); };
const me = { person: "me", label: "Me", address: "me/phone", state: "self", devices: [{ address: "me/phone" }, { address: "me/desk" }] };
const peer = { person: "peer", label: "Vitalii", address: "peer/desk", state: "pinned", devices: [{ address: "peer/desk" }] };
const overview = { person: me, people: [peer], agents: true, agent_devices: ["me/desk", "peer/desk"], me: { address: "me/phone", agent: false } } as unknown as T.Overview;
const thread = { id: "chat", role: "member", peer, agents: [], guests: [], messages: [] } as unknown as T.DMThread;
check(candidates(thread, overview, {}).some(c => c.kind === "person" && c.id === "peer"), "Verified DM peer missing from mentions");
const thin = { ...thread, peer: { label: "Vitalii", address: "peer/desk", state: "" } } as T.DMThread;
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
console.log("PASS Comic candidates: verified peer mentions, listed-claim refusal, own remote agents and capability limits");
