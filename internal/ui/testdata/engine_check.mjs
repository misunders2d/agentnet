// The browser device's engine for engine_test.go: runs static/engine.mjs in
// node against a real test Hub (trusted by its own certificate through
// NODE_EXTRA_CA_CERTS), with an in-memory store, answering one JSON request
// per line on stdin with one JSON line on stdout.
import { Engine, HubError, memoryStore, probeStore, sameOrigin, autoNameTries, inviteDays } from "../static/engine.mjs";
import * as wire from "../static/wire.mjs";
import { createInterface } from "node:readline";

// stats counts what a catch-up costs (backlog_test.go): requests by method
// and path shape, store reads and writes, and page changes. latency delays
// every request as a phone's network does; writeDelay every store write as
// a phone's strict IndexedDB commit does.
const stats = { req: {}, reads: 0, rows: 0, scans: {}, writes: 0, changed: 0, renders: 0, pushed: 0, acking: 0, acksAtOnce: 0 };
const scanned = (s, v) => { stats.reads++; stats.rows += v.length; stats.scans[s] = (stats.scans[s] || 0) + v.length; return v; };
let latency = 0, writeDelay = 0;
let conflictSend = "", sendConflicts = 0;
let ackGate = null; // holdAcks: receipts wait here until released
// page stands in for an open skin: on a change it reads the overview and
// the open DM again, one read at a time (a change meanwhile reads again
// once), as the Classic skin's refetch does.
let pageConv = "", pageBusy = false, pageAgain = false;
async function pageRefetch() {
  if (pageBusy) { pageAgain = true; return; }
  pageBusy = true;
  try {
    do { pageAgain = false; stats.renders++; await engine.api("/api/overview"); await engine.api("/api/dm?id=" + pageConv); } while (pageAgain);
  } catch (e) { /* the next change reads again */ } finally { pageBusy = false; }
}
const onChange = () => { stats.changed++; if (pageConv) pageRefetch(); };
// counting is the engine with the messages its stream pushes counted.
const counting = (e) => { const d = e.dispatch.bind(e); e.dispatch = (event, data) => { if (event === "message") stats.pushed++; return d(event, data); }; e.listen(onChange); return e; };
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const counted = (st) => ({ ...st,
  all: async (s) => scanned(s, await st.all(s)),
  after: async (s, k, n) => scanned(s, await st.after(s, k, n)),
  prefix: async (s, p) => scanned(s, await st.prefix(s, p)),
  async write(ops, checks) {
    stats.writes++; if (writeDelay) await sleep(writeDelay);
    if (conflictSend && ops.some(o => o.s === "outbox" && o.v?.conv === conflictSend && o.v.sub === "event")) {
      const key = "room-memberships/" + conflictSend;
      if (!checks?.some(c => c.s === "kv" && c.k === key && c.v === undefined)) throw Error("Fixture expected absent room ledger check.");
      conflictSend = ""; sendConflicts++;
      // A concurrent empty ledger snapshot changes storage, not authority.
      // The real checked write must abort before any send is committed.
      await st.write([{s:"kv", k:key, v:[]}]);
    }
    return st.write(ops, checks);
  } });
const raw = memoryStore(); // the test's own reads (count) are not counted
let store = counted(raw);
let engine = null;
let offline = false, dropPosts = false, receiptReads = 0;
let firstStreamGate=null;const streamRuns=[];
const realFetch = globalThis.fetch.bind(globalThis);
const activeFetches=new Map(),recentFetches=[];
// A network that can be switched off, as a phone on a train, or that
// loses only the messages posted.
const fetchNet = async (url, opts) => {
  const path=(opts?.method||"GET")+" "+new URL(url).pathname.replace(/[0-9a-f]{32,}/g,":id");
  activeFetches.set(path,(activeFetches.get(path)||0)+1);
  let status="network_error";
  let streamRun=null;
  if(new URL(url).pathname==="/v1/stream"){
    streamRun={session:engine.session,waitingLink:!!engine.waitingLink,membersHeader:null};streamRuns.push(streamRun);
    if(firstStreamGate&&streamRuns.length===1)await firstStreamGate.promise;
  }
  try {
    if(offline||(dropPosts&&opts?.method==="POST"&&url.endsWith("/v1/messages")))throw new TypeError("fetch failed");
    const response=await realFetch(url,opts);status=response.status;if(streamRun)streamRun.membersHeader=response.headers.get("Agentnet-Members");return response;
  } finally {
    const n=activeFetches.get(path)-1;if(n)activeFetches.set(path,n);else activeFetches.delete(path);
    recentFetches.push({path,status});if(recentFetches.length>16)recentFetches.shift();
  }
};
// notifyCalls stands in for the relay's notification API (NOTIFY.md §3)
// until the Hub has it: the relay then lists notify1, and every call to it
// is recorded with its signature headers present.
let notifyCalls = null;
const json = (v, status = 200) => new Response(JSON.stringify(v), { status, headers: { "Content-Type": "application/json" } });
// forged stands in for a server that lies: for a person id, a different
// (validly signed) record at a step already pinned, served as that
// person's chain and in its device's profile. An honest Hub never does.
const forged = new Map(); // person id -> { record, address }
async function fetchImpl(url, opts = {}) {
  const u = new URL(url);
  const shape = (opts.method || "GET") + " " + u.pathname.replace(/[0-9a-f]{32,}/g, ":id");
  stats.req[shape] = (stats.req[shape] || 0) + 1;
  if (shape.endsWith("/ack") && shape.startsWith("POST /v1/messages/")) { // receipts sent at once, at most
    stats.acksAtOnce = Math.max(stats.acksAtOnce, ++stats.acking);
    try { if (ackGate) await ackGate.held; if (latency) await sleep(latency); return await fetchNet(url, opts); } finally { stats.acking--; }
  }
  if (latency) await sleep(latency);
  if ((opts.method || "GET") === "GET" && u.pathname.startsWith("/v1/messages/")) receiptReads++;
  for (const [person, f] of forged) {
    if (u.pathname === "/v1/persons/" + person + "/chain") return json({ records: [JSON.parse(f.record)], more: false });
    if (u.pathname === "/v1/agents/" + f.address + "/profile") {
      const real = await (await fetchNet(url, opts)).json();
      return json({ ...real, person: JSON.parse(f.record) });
    }
  }
  if (notifyCalls && u.pathname.startsWith("/v1/notify")) {
    notifyCalls.push({ method: opts.method || "GET", path: u.pathname, body: opts.body ? JSON.parse(opts.body) : null,
      signed: !!(opts.headers && opts.headers["X-Agentnet-Agent"]) });
  }
  return fetchNet(url, opts);
}
// A browser's push service, as the page's adapter gives it to the engine:
// a subscription of the right shape (an FCM endpoint, a P-256 key, 16
// bytes of auth). Nothing is ever pushed to it in the tests.
const b64url = (b) => Buffer.from(b).toString("base64url");
const fakePush = { supported: () => true, subscribed: 0,
  async subscribe(key) {
    this.subscribed++;
    this.key = key;
    if (!this.sub) {
      const kp = await crypto.subtle.generateKey({ name: "ECDH", namedCurve: "P-256" }, true, ["deriveBits"]);
      this.sub = { endpoint: "https://fcm.googleapis.com/fcm/send/agentnet-test-" + wire.newID(),
        p256dh: b64url(new Uint8Array(await crypto.subtle.exportKey("raw", kp.publicKey))), auth: b64url(crypto.getRandomValues(new Uint8Array(16))) };
    }
    return this.sub;
  },
  async current(key) { return this.subscribe(key); } };

async function handle(req) {
  switch (req.op) {
  case "init":
    if (req.notify) notifyCalls = [];
    engine = counting(new Engine({ store, base: req.base, fetch: fetchImpl, push: fakePush }));
    await engine.load();
    await probeStore(store);
    return { joined: engine.joined };
  case "join":
    return { address: await engine.join(req.code, req.name) };
  case "mel736OneProbes": {
    const mem=memoryStore(),e=new Engine({store:mem,base:"https://fixture.invalid",fetch:()=>{throw Error("unexpected network")}}),keys=await wire.newKeys();
    e.keys=keys;e.address="peer/browser";e.fp=await wire.fingerprint(await wire.publicEntry(keys,e.address));
    const mine=await wire.newRoster(keys,e.address,"Peer");e.me=await e.personRecord([mine],"self",null);
    await mem.write([{s:"kv",k:"person",v:e.me}]);
    const rosters=await Promise.all(req.rosters.map(wire.parseRoster));for(const r of rosters)await wire.verifyFirst(r);
    const ps=await Promise.all(rosters.map(r=>e.personRecord([r],"pinned",null))),[old,next,third]=ps;
    const save=async p=>mem.write([{s:"persons",k:p.person,v:p}]);await save(old);await save(next);
    const leave=async p=>{for(const d of p.devices)await mem.write([{s:"kv",k:"revoked-device/"+d.address+"/"+d.fingerprint,v:{address:d.address,fingerprint:d.fingerprint}}]);await mem.write([{s:"kv",k:"continuity-head/"+p.person,v:{person:p.person,hash:p.hash}}]);};
    let checks=0;const assert=(ok,why)=>{if(!ok)throw Error("MEL736: "+why);checks++;};
    assert((await e.logicalPersons()).get(old.person)===old.person,"live aliases");
    await leave(old);assert((await e.logicalPersons()).get(old.person)===next.person,"Bohdan split remains");
    const roots=[];for(const p of [old,next]){const r=await wire.newRoot(keys,{person:e.me.person,roster:e.me.hash,address:e.address,fingerprint:e.fp},{person:p.person,roster:p.hash}),id=await wire.rootID(r);roots.push({id,root:wire.rootJSON(r),peer:p.person,created:r.created,creator:e.address});await mem.write([{s:"convs",k:id,v:roots.at(-1)}]);}
    const original=wire.newID(),lid=wire.newID(),other=wire.newID();await mem.write([{s:"inbox",k:original,v:{id:original,lid,conv:roots[0].id,kind:"message",body:"old history",at:1,read:true,fp:old.fingerprint}},{s:"inbox",k:other,v:{id:other,lid:other,conv:roots[1].id,kind:"message",body:"new history",at:2,read:false,fp:next.fingerprint}}]);
    assert((await e.continuityDMs(roots[0].id)).length===2,"DM union");assert(await e.currentDM(roots[0].id)===roots[1].id,"current root routing");
    assert(await e.logicalReply(roots[1].id,lid,true,"Conversation",true)===lid,"cross-root reply");
    let strict=false;try{await e.logicalReply(roots[1].id,lid,true,"Conversation");}catch(_){strict=true;}assert(strict,"native context crossed roots");
    const read=await e.readView(roots[0].id),rows=await e.messagingMessages(roots[0].id,read.inbox,read.outbox,read);
    assert(rows.some(r=>r.id===original&&r.read)&&rows.some(r=>r.id===other&&!r.read),"read state lost");
    const family=await e.continuityDMs(roots[0].id);await e.continuityNotices(family[0].id,family);assert((await mem.all("inbox")).filter(r=>r.sub==="continuity").length===1,"notice duplicated");
    const reopened=new Engine({store:mem,base:e.base});reopened.me=e.me;assert((await reopened.logicalPersons()).get(old.person)===next.person,"restart split");
    await mem.write([{s:"persons",k:next.person,v:{...next,state:"conflict",email_claim:true}}]);
    e.chain=async id=>[rosters[id===old.person?0:1]];e.directory=async address=>({revoked:address===old.devices[0].address,fingerprint:address===old.devices[0].address?old.fingerprint:next.fingerprint});
    await e.refreshContinuity();assert((await mem.get("persons",next.person)).state==="pinned"&&(await e.logicalPersons()).get(old.person)===next.person,"frozen claim did not retry on evidence");
    await save(third);assert((await e.logicalPersons()).get(old.person)===old.person,"ambiguous candidate aliases");await leave(next);assert((await e.logicalPersons()).get(old.person)===third.person,"second recovery");
    const secondFamily=await e.continuityDMs(roots[0].id);await e.continuityNotices(secondFamily[0].id,secondFamily);await e.continuityNotices(secondFamily[0].id,secondFamily);assert((await mem.all("inbox")).filter(r=>r.sub==="continuity").length===2,"second recovery notice count");
    // Stale head marker and directory failures never publish completeness.
    await mem.write([{s:"kv",k:"continuity-head/"+old.person,v:{person:old.person,hash:"0".repeat(64)}}]);assert((await e.logicalPersons()).get(old.person)===old.person,"stale head aliases");
    e.chain=async()=>[rosters[0]];e.directory=async()=>{throw new HubError(503,"unavailable","fixture unavailable")};let failed=false;try{await e.continuityHead(old);}catch(_){failed=true;}assert(failed,"directory failure swallowed");
    e.directory=async()=>({revoked:true,fingerprint:old.devices[0].fingerprint});await e.continuityHead(old);assert((await e.logicalPersons()).get(old.person)===third.person,"directory retry failed");
    const m=p=>({person:p.person,roster:p.hash,admission:{conv:"",realm:"",person:p.person,roster:p.hash,seq:0,prev:"",history:[],by:""}}),packet={state:{members:[m(old),m(third)]},withdrawals:[]};
    assert((await e.logicalGroupMembers(packet)).length===1,"hand-added group duplicates");
    const current=await e.groupPeople(packet);assert(current.length===1&&current[0].person===third.person&&current[0].devices[0].fingerprint===third.fingerprint,"group uses recovered device");packet.state.members=[];assert((await e.logicalGroupMembers(packet)).length===0,"removed group resurrected");
    for(const withdrawal of [false,true]){
      const conv="0".repeat(64),realm=wire.newID(),member=(p,seq)=>({...m(p),admission:{...m(p).admission,conv,realm,seq}}),ma=member(old,0),mb=member(next,1);
      const initial={conv,realm,seq:0,members:[ma]},restored={conv,realm,seq:1,members:[ma,mb]},removed={conv,realm,seq:2,members:[ma]};
      const p=withdrawal?{proof:[initial],state:restored,withdrawals:[{conv,realm,person:next.person,admission:await wire.groupAdmissionHash(mb.admission)}]}:{proof:[initial,restored],state:removed,withdrawals:[]};
      assert((await e.logicalGroupMembers(p)).length===0,"second recovery resurrected "+(withdrawal?"withdrawn":"removed")+" family");
      p.proof.push(p.state);p.state={...p.state,seq:p.state.seq+1,members:[...p.state.members,member(third,p.state.seq+1)]};
      const admitted=await e.logicalGroupMembers(p);assert(admitted.length===1&&admitted[0].person===third.person,"later explicit family re-admission rejected");
    }
    // Recovery carries ordinary messaging membership, never administrator role.
    const adminStore=memoryStore(),adminEngine=new Engine({store:adminStore,base:e.base}),me={...next,state:"self"};
    adminEngine.me=me;adminEngine.address=next.devices[0].address;adminEngine.fp=next.devices[0].fingerprint;
    await adminStore.write([{s:"persons",k:old.person,v:old},{s:"kv",k:"person",v:me},{s:"kv",k:"continuity-head/"+old.person,v:{person:old.person,hash:old.hash}},...old.devices.map(d=>({s:"kv",k:"revoked-device/"+d.address+"/"+d.fingerprint,v:{address:d.address,fingerprint:d.fingerprint}}))]);
    const adminPacket={root:{},proof:[],state:{conv:"1".repeat(64),realm:wire.newID(),seq:0,title:"Admin boundary",members:[{...m(old),admin:true}]},withdrawals:[]};
    adminEngine.groupTurnEvidence=async()=>({packet:adminPacket,g:{}});
    adminEngine.managedGroupInvitation=async()=>({status:"pending",proposal:{state:adminPacket.state,target:third.person}});
    adminEngine.call=async()=>{throw Error("unexpected administrator network call")};
    assert((await adminEngine.groupPeople(adminPacket))[0].admin===false,"projected administrator role inherited");
    assert((await adminEngine.groupTarget(adminPacket.state,[],[])).person===next.person,"ordinary recovered membership lost");
    const denied=async(fn,message)=>{let error="";try{await fn();}catch(e){error=e.message;}assert(error===message,"recovered admin gate: "+error);};
    const rename=()=>adminEngine.manageGroup({conv:adminPacket.state.conv,action:"rename",title:adminPacket.state.title});
    await denied(rename,"Current person administrator required.");
    await denied(()=>adminEngine.cancelGroup({id:wire.newID()}),"Current inviting administrator required.");
    await denied(()=>adminEngine.leaveGroup(adminPacket.state.conv),"Exact current group membership required.");
    const admittedAdmin={...m(next),admin:false};adminPacket.state.members.push(admittedAdmin);
    await denied(rename,"Current person administrator required.");
    admittedAdmin.admin=true;
    assert((await rename()).queued===false&&(await adminEngine.groupPeople(adminPacket))[0].admin===true,"explicit administrator grant rejected");
    const own=new Engine({store:mem,base:e.base});own.me={...third,state:"self"};assert((await own.logicalPersons()).get(old.person)===third.person,"recovered own person");
    e.groupRecord=async()=>({});assert((await e.continuityNotices("held-group")).length===0,"unavailable group context blocks notices instead of history");
    const ordinary=new Engine({store:memoryStore(),base:e.base});ordinary.me=e.me;ordinary.groupRecord=async()=>{throw Error("ordinary group notice requested current context")};assert((await ordinary.continuityNotices("ordinary-held-group")).length===0,"ordinary held group cannot render");
    const background=new Engine({store:memoryStore(),base:e.base});let attempts=0;
    background.syncContinuityGroupsOnce=async()=>{if(++attempts===1)await background.store.write([{s:"kv",k:"conflict-probe",v:1}],[{s:"kv",k:"conflict-probe",v:"stale"}]);};
    await background.syncContinuityGroups();assert(attempts===2,"background storage conflict did not retry");
    let release,passes=0;const gate=new Promise(r=>release=r);background.refreshContinuityOnce=async()=>{if(++passes===1)await gate;};
    const firstRefresh=background.refreshContinuity(),secondRefresh=background.refreshContinuity();release();await Promise.all([firstRefresh,secondRefresh]);assert(passes===2,"coalesced evidence wake was lost");
    return {passed:true,checks};
  }
  case "googleReviewProbes": {
    const ownerKeys = await wire.newKeys(), ownerAddress = "g-fixture/laptop", email = "person@example.com";
    const head = await wire.newRoster(ownerKeys, ownerAddress, "Person", email), hash = await wire.rosterHash(head);
    const approver = await wire.publicEntry(ownerKeys, ownerAddress), approverFP = await wire.fingerprint(approver);
    for (const scenario of ["first-race", "expired", "roster_stale", "too_many_devices", "bad_step", "network", "server", "address_taken", "landed-expired"]) {
      const mem = memoryStore(), keys = await wire.newKeys(), address = "g-fixture/browser";
      const oldFirst = await wire.newRoster(keys, address, "Person", email);
      const link = { email, person: head.person, seq: head.seq, roster: hash, approver: { address: ownerAddress, fingerprint: approverFP },
        expires: Math.floor(Date.now()/1000) + (scenario.includes("expired") || scenario === "expired" ? -1 : 600), offer: wire.newID(),
        join: await wire.joinConsent(keys, address, head.person, head.seq+1, hash) };
      await mem.write([{s:"kv",k:"google_joining",v:{keys,address,email,first:scenario === "first-race" ? wire.rosterJSON(oldFirst) : null,link:scenario === "first-race" ? null : link}}]);
      const e = new Engine({store:mem,base:"https://fixture.invalid",fetch:()=>{throw Error("external fetch forbidden")}});
      let attempts = 0;
      e.call = async (method,path,body) => {
        if(path.endsWith("prepare")) return {address,email,name:"Person",head:wire.rosterJSON(head),approver:JSON.parse(wire.marshalPublic(approver)),enrolled:scenario === "landed-expired"};
        if(!path.endsWith("join")) throw Error("unexpected request " + path);
        if(typeof body === "string") body = JSON.parse(body);
        attempts++;
        if(scenario === "expired" && body.link.expires <= Math.floor(Date.now()/1000)) throw Error("expired consent reused");
        if(scenario === "landed-expired" && body.link.expires !== link.expires) throw Error("landed consent rewritten");
        if(attempts === 1 && !["expired","landed-expired"].includes(scenario)) {
          const code = scenario === "first-race" ? "roster_stale" : scenario;
          throw new HubError(scenario === "network" ? 0 : scenario === "server" ? 503 : code === "bad_step" ? 400 : 409,code,"fixture refusal");
        }
        if(!body.link) throw Error("fresh retry did not use current head");
        return {};
      };
      try { await e.joinGoogle("mock", "browser"); } catch(error) {
        const saved = await mem.get("kv","google_joining"), definite = ["first-race","roster_stale","too_many_devices","bad_step"].includes(scenario);
        if(definite && (saved.link || saved.first)) throw Error(scenario + " intent not cleared");
        if(!definite && !saved.link) throw Error(scenario + " exact intent lost");
        await e.joinGoogle("mock","browser");
      }
      if(!e.joined) throw Error(scenario + " retry not joined");
    }
    const mem=memoryStore(), e=new Engine({store:mem,base:"https://fixture.invalid",fetch:()=>{throw Error("external fetch forbidden")}});
    const spoofKeys=await wire.newKeys(), spoof=await wire.newRoster(spoofKeys,"g-spoof/phone","Spoof",email);
    e.chain=async id=>id===head.person?[head]:[spoof];
    await e.pinChain(head.person);
    let blocked=false;try { await e.pinChain(spoof.person); } catch { blocked=true; }
    const conflicting=await mem.get("persons",spoof.person);
    if(!blocked || conflicting.state!=="conflict" || e.personView(conflicting).email) throw Error("duplicate email displayed as pinned person");
    // MEL-736: while the server still lists the earlier device, the claim
    // stays frozen; once it revoked every one, the person who joined afresh
    // with that email is pinned and what it held is retried.
    const headFP=(await mem.get("persons",head.person)).devices[0].fingerprint;
    let revoked=false;e.retryHeld=async()=>{};e.endRecipient=async()=>{};
    e.directory=async address=>{if(address!==ownerAddress)throw Error("unexpected directory "+address);return {fingerprint:headFP,revoked};};
    await mem.write([{s:"held",k:"held-1",v:{id:"held-1",from:"g-spoof/phone",reason:"identity_conflict",envelope:"{}",at:1}}]);
    let still;try { still=await e.pinChain(spoof.person); } catch { still=null; }
    if((still && still.state!=="conflict") || (await mem.get("persons",spoof.person)).state!=="conflict") throw Error("claim released while its holder is live");
    revoked=true;
    const rejoined=await e.pinChain(spoof.person);
    if(rejoined.state!=="pinned" || e.personView(rejoined).email!==email) throw Error("rejoined person not pinned with its email: "+rejoined.state);
    if((await mem.get("persons",head.person)).state!=="pinned") throw Error("earlier person not kept");
    if(!(await mem.get("kv","revoked-device/"+ownerAddress+"/"+headFP))) throw Error("earlier device not recorded as revoked");
    if((await mem.get("held","held-1")).reason!=="proof_pending") throw Error("held message not released to the proof retry");
    return {passed:true};
  }
  case "googleKeys": {
    const nonce = await engine.googleNonce();
    const pending = await store.get("kv", "google_joining");
    return { nonce, public: wire.marshalPublic(await wire.publicEntry(pending.keys, "google/" + req.name)) };
  }
  case "googleJoin":
    return { address: await engine.joinGoogle(req.token, req.name) };
  case "joinLink": // a device link from another device of the person (its QR's text)
    return { address: await engine.joinAndLink(req.code, req.name) };
  case "joinAuto": // under an automatic name: base, base-2 … while taken
    return { address: await engine.joinAuto(req.code, req.base) };
  case "joinLinkAuto": // a device link, under an automatic name
    return { address: await engine.joinAndLinkAuto(req.code, req.base) };
  case "tunables": // the engine's copies of Go's tunables, for parity tests
    return { autoNameTries, inviteDays };
  case "decodeInvite": // wire.decodeInvite, for parity with Go
    return { v: wire.decodeInvite(req.code) };
  case "person":
    return await engine.createPerson(req.label);
  case "holdFirstStream": {
    if(streamRuns.length)throw Error("first stream already started");
    let release;const promise=new Promise(r=>release=r);firstStreamGate={promise,release};return {};
  }
  case "releaseFirstStream":
    if(!firstStreamGate)throw Error("first stream was not held");firstStreamGate.release();return {};
  case "streamState":
    return {session:engine.session,generation:engine.typing.generation,streams:streamRuns,...(req.profile?{profile:await engine.profile(engine.address)}:{})};
  case "reconnectStream":
    engine.abort?.abort();return {session:engine.session,generation:engine.typing.generation,streams:streamRuns.length};
  case "start":
    engine.start();
    return {};
  case "testHumanCaps": { // signed old/new reader ads, only on this test device
    // The stream's connected/member flags precede onConnect's capability
    // publication. Wait for that session's ad before modifying it; never
    // fabricate an old reader from an absent record.
    let old;
    const deadline = Date.now() + 20000;
    while (!old) {
      const profile = await engine.profile(engine.address);
      old = (profile.caps || []).map(wire.parseCaps).find(c => c.session === engine.session);
      if (old) break;
      if (Date.now() >= deadline) throw Error("own session capabilities were not published");
      await sleep(100);
    }
    const caps = old.caps.filter(c => ![wire.CapHumanParticipation, wire.CapRoom].includes(c));
    if (req.supported) caps.push(wire.CapHumanParticipation);
    const clock = Date.now;
    let record;
    try { Date.now = () => (old.ts + 1) * 1000; record = await wire.newCaps(engine.keys, engine.address, engine.session, caps); }
    finally { Date.now = clock; }
    await engine.call("PUT", "/v1/caps", wire.capsJSON(record));
    return {};
  }
  case "receiptReads": return { count: receiptReads };
  case "holdAcks": // receipts wait until released (on: false)
    if (req.on && !ackGate) { let open; ackGate = { held: new Promise((r) => { open = r; }) }; ackGate.open = open; }
    if (!req.on && ackGate) { ackGate.open(); ackGate = null; }
    return {};
  case "stats": { // what was counted since the last reset; latency and writeDelay are set for what follows
    const out = structuredClone(stats);
    if (req.reset) Object.assign(stats, { req: {}, reads: 0, rows: 0, scans: {}, writes: 0, changed: 0, renders: 0, pushed: 0, acksAtOnce: stats.acking }); // acking: those under way
    if (req.latency !== undefined) latency = req.latency;
    if (req.writeDelay !== undefined) writeDelay = req.writeDelay;
    if (req.page !== undefined) pageConv = req.page;
    return out;
  }
  case "heldRetryProbes": {
    // Reuse the real signed over-page carriers, but make lookup cost
    // deterministic: one negative lookup per device per evidence pass,
    // not one per envelope. New Fay evidence arrives during the first
    // pass, after its negative response was captured.
    const results = [];
    for (const status of [200, 404]) {
      const mem = memoryStore();
      await mem.write([
        ...await Promise.all(["identity", "person"].map(async k => ({s:"kv",k,v:await raw.get("kv",k)}))),
        ...(await raw.all("pins")).map(v => ({s:"pins",k:v.address,v})),
        ...(await raw.all("held")).map(v => ({s:"held",k:v.id,v})),
      ]);
      const e = new Engine({store:mem,base:"https://fixture.invalid",fetch:()=>{throw Error("external fetch forbidden")}});
      await e.load();
      let available = false, gusProfiles = 0, fayProfiles = 0;
      e.call = async (method, path) => {
        if (method === "GET" && path === "/v1/agents/" + req.unproven + "/profile") {
          gusProfiles++;
        } else if (method === "GET" && path === "/v1/agents/" + req.address + "/profile") {
          fayProfiles++;
          if (available) return {person:req.person};
          available = true;
          e.retryHeld(); // evidence wake while the old negative result is in flight
        } else if (method === "GET" && path.startsWith("/v1/persons/" + req.person.person + "/chain?")) {
          return {records:[req.person],more:false};
        } else if (method === "POST" && path.endsWith("/ack")) {
          return {};
        } else throw Error("unexpected held proof request " + method + " " + path);
        if (status === 404) throw new HubError(404, "not_found", "person unavailable");
        return {};
      };
      await e.retryHeld();
      const held = await mem.all("held"), inbox = await mem.all("inbox");
      results.push({status,gusProfiles,fayProfiles,held:held.length,
        gusOnly:held.every(h => h.from === req.unproven && h.reason === "proof_pending"),
        admitted:inbox.length,body:inbox[0]?.body});
    }
    return {results};
  }
  case "status":
    return { connected: engine.connected, revoked: engine.revoked, members: engine.members.current, link: engine.link ? engine.link.state : "" };
  case "receiveState": { // Bounded timeout evidence; no keys, plaintext or encrypted carriers.
    const rows=async s=>(await raw.all(s)).map(r=>({id:r.id,lid:r.lid,conv:r.conv,kind:r.kind,sub:r.sub,ts:r.ts,at:r.at,state:r.state,reason:r.reason,detail_code:r.detail_code})).sort((a,b)=>(a.at||0)-(b.at||0));
    const pending=await raw.prefix("kv","receive-pending/"),identity=await raw.get("kv","identity"),own=await raw.get("kv","person");
    const inbox=await rows("inbox"),held=await rows("held"),receipts=await rows("receipts");
    return {connected:engine.connected,running:engine.running,closing:engine.closing,revoked:engine.revoked,generation:engine.receiveGeneration,
      retryActive:!!engine.receiveRetryRun,retryAgain:!!engine.receiveRetryAgain,receiving:[...engine.receiving.keys()].slice(0,32),cursor:await raw.get("kv","receive-retry-cursor"),
      identityCurrent:identity?.address===engine.address&&identity?.fingerprint===engine.fp,ownState:own?.state,ownRecipient:!!own?.devices.some(d=>d.address===engine.address&&d.fingerprint===engine.fp),
      pendingCount:pending.length,pending:pending.slice(0,32).map(r=>({id:r.id,at:r.at,currentRecipient:r.address===engine.address&&r.fingerprint===engine.fp})),
      inboxCount:inbox.length,inbox:inbox.slice(0,32),heldCount:held.length,held:held.slice(0,32),receiptCount:receipts.length,receipts:receipts.slice(0,32),
      pushed:stats.pushed,requests:stats.req,activeFetches:Object.fromEntries(activeFetches),recentFetches};
  }
  case "api":
    return { v: await engine.api(req.path, req.body) };
  case "reminderState": // terminal facts as well as the page's pending reminders
    return { rows: await engine.reminders(true), marks: await store.prefix("kv", "reminder-mark/" + engine.me.person + "/") };
  case "replayOwnState": { // fresh, signed quiet carriers made by the test's own linked native key
    const env = wire.parseEnvelope(req.envelope);
    if (env.v !== 2 || env.kind !== "message" || env.attn || env.to !== engine.address) throw Error("Expected an exact quiet own-state fixture carrier.");
    await engine.admit(req.envelope, env); // normal key, roster and generation admission
    if (engine.readSyncRun) await engine.readSyncRun;
    if (engine.reminderSyncRun) await engine.reminderSyncRun;
    return { held: (await store.get("held", env.id)) || null, inbox: (await store.get("inbox", env.id)) || null };
  }
  case "conflictSend":
    if (req.conv) conflictSend = req.conv;
    return { fired: sendConflicts };
  case "offline":
    offline = req.on;
    if (offline) engine.offline();
    else engine.online();
    return {};
  case "forge": // a lying server: another record for req.person, as its chain and in req.address's profile
    forged.set(req.person, { record: req.record, address: req.address });
    return {};
  case "dropPosts":
    dropPosts = req.on;
    return {};
  case "flush": // what a ping or a connection does with the kept messages
    if (req.connected) { // as a connection does before its member list has come
      engine.connected = true;
      try { await engine.flushOutbox(); } finally { engine.connected = false; }
    } else {
      await engine.flushOutbox();
    }
    return {};
  case "stopStream": // no events come: the next change is seen only by asking
    engine.stop();
    return {};
  case "outbox": { // a kept message; a file's ciphertext as its length
    let rec = null;
    if (req.readSync) { // exact private fixture selector; at most one carrier returned
      const q=req.readSync,matches=[];
      wire.parseReadSync(JSON.stringify({v:2,person:engine.me.person,roster:engine.me.hash,refs:[],marks:[q.mark]}));
      for(const row of await store.outboxSub(wire.SubReadSync)) {
        if(row.to!==q.to||row.recipient_fp!==q.fingerprint||row.read_owner!==engine.me.person||row.required_cap!==wire.CapReadSyncV2)continue;
        const r=wire.parseReadSync(row.body);
        if(r.v===2&&r.person===engine.me.person&&r.marks.some(m=>["conv","fingerprint","lid","state","rev","writer"].every(k=>m[k]===q.mark[k])))matches.push(row);
        if(matches.length===2)throw Error("Ambiguous exact read-sync fixture carrier");
      }
      rec=matches[0]||null;
    } else rec = (await store.get("outbox", req.id)) || null;
    if (rec && rec.files) rec.files = rec.files.map((f) => ({ ...f, ct: f.ct ? f.ct.length : null }));
    return { rec };
  }
  case "reload": { // the page is reloaded: a new engine over the same stored data
    engine.stop();
    engine = counting(new Engine({ store, base: req.base, fetch: fetchImpl, push: fakePush }));
    await engine.load();
    engine.start();
    return { joined: engine.joined };
  }
  case "tamperPin": { // the pin of address now names another key (as if it changed); returns the one it named
    const pin = await store.get("pins", req.address);
    await store.write([{ s: "pins", k: req.address, v: { ...pin, json: req.json, fingerprint: req.fingerprint, pending: null } }]);
    engine.pubs.clear();
    return { json: pin.json, fingerprint: pin.fingerprint };
  }
  case "sameOrigin":
    sameOrigin(req.hub, req.base);
    return {};
  case "notifyCalls": { // the notification API calls since the last look
    const out = notifyCalls || [];
    notifyCalls = notifyCalls ? [] : null;
    return { calls: out, subscribed: fakePush.subscribed, key: fakePush.key || "" };
  }
  case "channel":
    return { chan: await wire.notifyChannel(req.conv, req.fp || engine.fp) };
  case "sendFiles": { // a DM message with files, as the page gives File objects
    const files = req.files.map((f) => { const bytes = new Uint8Array(Buffer.from(f.b64, "base64")); return { name: f.name, size: bytes.length, bytes }; });
    return { v: await engine.api("/api/dm/send", { conv: req.conv, body: req.body || "", files }) };
  }
  case "holdUploads": { // uploads wait for releaseUploads; the start of one is awaited by sendFilesLater
    const orig = engine.uploadBlob.bind(engine);
    held = {};
    held.gate = new Promise((r) => { held.open = r; });
    held.started = new Promise((r) => { held.start = r; });
    engine.uploadBlob = async (...args) => { held.start(); await held.gate; return orig(...args); };
    return {};
  }
  case "sendFilesLater": { // sendFiles, answered once its first upload is under way
    const files = req.files.map((f) => { const bytes = new Uint8Array(Buffer.from(f.b64, "base64")); return { name: f.name, size: bytes.length, bytes }; });
    // The stream is stopped to isolate profile reads; a simulated live
    // connection still lets MEL-547's background outbox begin the upload.
    if (req.connected) engine.connected = true;
    held.send = engine.api("/api/dm/send", { conv: req.conv, body: req.body || "", files });
    let timer;
    try {
      await Promise.race([
        held.started,
        held.send.then(() => new Promise(() => {})), // durable return precedes the upload
        new Promise((_, reject) => { timer = setTimeout(() => reject(Error("held upload did not start")), 5000); }),
      ]);
    } finally { clearTimeout(timer); }
    return {};
  }
  case "releaseUploads": {
    held.open();
    const saved = await held.send;
    await engine.outboxPass; // observe the post-upload gate, not just the local save
    return { v: saved };
  }
  case "hubStatus": // what the relay says of a message this device sent
    return { v: await engine.call("GET", "/v1/messages/" + req.id) };
  case "convRoot":
    return { root: (await store.get("convs", req.conv)).root };
  case "openFile": {
    const f = await engine.api("/api/file?id=" + req.id + "&i=" + req.i + (req.dir ? "&dir=" + req.dir : ""));
    return { name: f.name, size: f.size, image: f.image, b64: Buffer.from(f.bytes).toString("base64") };
  }
  case "notifyPrefs": // what the relay holds for this device
    return await engine.call("GET", "/v1/notify/prefs");
  case "inboxRec":
    return { rec: (await store.get("inbox", req.id)) || null };
  case "count":
    return { n: (await raw.all(req.store)).length };
  case "keys": {
    const id = await store.get("kv", "identity");
    let exported = true;
    try { await crypto.subtle.exportKey("pkcs8", id.keys.box); } catch (e) { exported = false; }
    return { extractable: id.keys.sign.extractable || id.keys.box.extractable, exported, fingerprint: id.fingerprint,
      public: wire.marshalPublic(await wire.publicEntry(id.keys, id.address)) };
  }
  }
  throw new Error("unknown op " + req.op);
}

let held = null; // holdUploads
const rl = createInterface({ input: process.stdin });
for await (const line of rl) {
  let out;
  try {
    out = await handle(JSON.parse(line));
  } catch (e) {
    out = { error: e.message };
  }
  process.stdout.write(JSON.stringify(out) + "\n");
}
process.exit(0);
