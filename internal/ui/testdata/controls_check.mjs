// Message controls in the browser engine, on the in-memory store and with
// no network: the legacy target chosen by direction (a received id can
// equal a sent one), retraction keeping bytes another live message still
// shows, resolution order, and an older peer refused by name. Run with
// node; nothing is joined or sent.
import { Engine, memoryStore, EXEC_RUNNING_MAX_AGE, heldDiagnosticCode } from "../static/engine.mjs";
import * as wire from "../static/wire.mjs";

let failed = 0;
const check = (ok, what) => { if (!ok) { failed++; console.error("FAIL: " + what); } };
const SELF = "11111111-22222222-33333333-44444444", PEER = "aaaaaaaa-bbbbbbbb-cccccccc-dddddddd";
const ID = "0123456789abcdef0123456789abcdef", ID2 = "fedcba9876543210fedcba9876543210", SHA = "a".repeat(64), SHA2 = "b".repeat(64);
const att = (sha, blob) => ({ blob: { id: blob, size: 10, sha256: "c".repeat(64) }, name: "f.txt", size: 5, sha256: sha });

async function fresh() {
  const store = memoryStore();
  const e = new Engine({ store, base: "http://test", fetch: async () => { throw new Error("no network in this check"); } });
  Object.assign(e, { address: "me/phone", fp: SELF, keys: {}, me: null });
  return { store, e };
}
const ctl = (id, sub, body, ref, from = "peer/desk") => ({ id, v: 3, control: true, from, fp: from === "me/phone" ? SELF : PEER, kind: "message", sub, body, ref, at: 1, read: true });

// 1. The same id received and sent: a valid reaction on the sent one is
// admitted against the sent row; one on the received one against that;
// an edit by the peer of my message is refused; a foreign key is held.
{
  const { store, e } = await fresh();
  await store.write([
    { s: "inbox", k: ID, v: { id: ID, v: 1, from: "peer/desk", fp: PEER, kind: "message", body: "theirs", at: 1 } },
    { s: "outbox", k: ID, v: { id: ID, v: 1, to: "peer/desk", kind: "message", body: "mine", at: 2, state: "delivered" } },
  ]);
  const pin = { fingerprint: PEER };
  const env = (id) => ({ id, from: "peer/desk", to: "me/phone" });
  const react = (n, fp) => ({ v: 3, conv: "", lid: "", replica: false, kind: "message", sub: wire.SubReaction, body: JSON.stringify({ emoji: "👍", op: "add", n }), ref: { id: ID, fingerprint: fp } });
  const ops1 = await e.admitControl(react(1, SELF), env("c1".padEnd(32, "0")), pin);
  check(ops1.length === 1 && ops1[0].s === "inbox" && ops1[0].v.ref.fingerprint === SELF, "a reaction on my sent message (same id as a received one) is admitted");
  const ops2 = await e.admitControl(react(1, PEER), env("c2".padEnd(32, "0")), pin);
  check(ops2.length === 1 && ops2[0].v.ref.fingerprint === PEER, "a reaction on their received message with that id is admitted too");
  let held = "";
  try { await e.admitControl(react(1, "99999999-99999999-99999999-99999999"), env("c3".padEnd(32, "0")), pin); } catch (err) { held = err.reason + ":" + err.message; }
  check(held.startsWith("proof_pending"), "a reaction naming a key that sent nothing here is held: " + held);
  let refused = "";
  try { await e.admitControl({ ...react(1, SELF), sub: wire.SubRevision, body: JSON.stringify({ rev: 1, text: "hijack" }) }, env("c4".padEnd(32, "0")), pin); } catch (err) { refused = err.reason + ":" + err.message; }
  check(refused.startsWith("invalid") && refused.includes("only the sender"), "the peer editing my message is refused: " + refused);
  const okEdit = await e.admitControl({ ...react(1, PEER), sub: wire.SubRevision, body: JSON.stringify({ rev: 1, text: "fixed" }) }, env("c5".padEnd(32, "0")), pin);
  check(okEdit.length === 1 && okEdit[0].v.sub === wire.SubRevision, "the peer editing their own message is admitted");
  // resolution on the thread view: the reaction sits on the right message; the edit on theirs only
  await store.write([...ops1, ...ops2, ...okEdit]);
  const th = await e.thread(ID);
  const mine = th.messages.find((m) => m.dir === "out"), theirs = th.messages.find((m) => m.dir === "in");
  check(mine.reactions && mine.reactions.length === 1 && mine.reactions[0].by[0].id === "peer/desk" && mine.reactions[0].by[0].label === "peer/desk" && !mine.edited, "my message shows their reaction (by = their address) and no edit: " + JSON.stringify(mine.reactions));
  check(theirs.edited && theirs.text === "fixed" && theirs.body === "theirs" && theirs.reactions.length === 1, "their message shows their edit and their reaction");
  check(mine.can.includes("edit") && !theirs.can.includes("edit") && theirs.can.includes("react"), "I may edit mine, react to theirs: " + JSON.stringify([mine.can, theirs.can]));
}

// 2. Retraction keeps bytes another live message still shows; the last
// reference lets them go; a history copy counts as a reference. Real
// keys: the shared file is kept encrypted to this device (keepSent) and
// the survivor must decrypt to exactly those bytes after the first
// retraction, and be gone only after the last.
{
  const { store, e } = await fresh();
  e.keys = await wire.newKeys();
  e.selfPub = await wire.publicEntry(e.keys, e.address);
  const plain = new TextEncoder().encode("shared bytes, sent twice\n");
  const sha = wire.hex(await wire.sha256(plain));
  await e.keepSent([{ bytes: plain, name: "f.txt" }]);
  check(!!(await store.get("files", "kept/" + sha)), "keepSent kept the file encrypted to this device");
  const a1 = { blob: { id: "blob1", size: 10, sha256: "c".repeat(64) }, name: "f.txt", size: plain.length, sha256: sha };
  const a2 = { ...a1, blob: { ...a1.blob, id: "blob2" } };
  await store.write([
    { s: "outbox", k: ID, v: { id: ID, v: 1, to: "peer/desk", kind: "message", body: "a", at: 1, attachments: [a1] } },
    { s: "outbox", k: ID2, v: { id: ID2, v: 1, to: "peer/desk", kind: "message", body: "b", at: 2, attachments: [a2] } },
    { s: "files", k: "ct/blob1", v: { ct: new Uint8Array([1]) } }, { s: "files", k: "ct/blob2", v: { ct: new Uint8Array([2]) } },
  ]);
  const opened = await e.openSent(ID2, 0);
  check(opened.bytes.length === plain.length && wire.hex(await wire.sha256(opened.bytes)) === sha && opened.name === "f.txt", "before any retraction the survivor opens to the exact bytes");
  const retr = (id) => ({ control: true, sub: wire.SubRetraction, ref: { id, fingerprint: SELF }, conv: "" });
  const ops1 = await e.dropCached(await store.get("outbox", ID), retr(ID));
  check(ops1.some((o) => o.k === "ct/blob1") && !ops1.some((o) => o.k === "kept/" + sha), "retracting one of two messages with the same bytes drops its own ciphertext but keeps the shared kept copy: " + JSON.stringify(ops1.map((o) => o.k)));
  await store.write([...ops1, { s: "outbox", k: "r1".padEnd(32, "0"), v: { id: "r1".padEnd(32, "0"), v: 3, control: true, to: "peer/desk", fp: SELF, sub: wire.SubRetraction, body: "{}", ref: { id: ID, fingerprint: SELF }, at: 3, aside: true } }]);
  const after = await e.openSent(ID2, 0);
  check(wire.hex(await wire.sha256(after.bytes)) === sha, "after the first retraction the survivor still decrypts to the exact bytes");
  let gone = "";
  try { await e.openSent(ID, 0); } catch (err) { gone = err.message; }
  check(gone === "That message was deleted.", "the retracted message's file is refused: " + gone);
  const ops2 = await e.dropCached(await store.get("outbox", ID2), retr(ID2));
  check(ops2.some((o) => o.k === "kept/" + sha) && ops2.some((o) => o.k === "ct/blob2"), "retracting the last message with those bytes lets the kept copy go: " + JSON.stringify(ops2.map((o) => o.k)));
  await store.write([...ops2, { s: "outbox", k: "r2".padEnd(32, "0"), v: { id: "r2".padEnd(32, "0"), v: 3, control: true, to: "peer/desk", fp: SELF, sub: wire.SubRetraction, body: "{}", ref: { id: ID2, fingerprint: SELF }, at: 4, aside: true } }]);
  check(!(await store.get("files", "kept/" + sha)), "no kept copy remains once the last reference is retracted");
  const SHA = sha; // the history-copy case below refers to the same bytes
  // a history copy in a conversation referencing the same sha keeps it: a
  // third sent message with those bytes, retracted while the copy is shown
  const ID3 = "3".repeat(32);
  await e.keepSent([{ bytes: plain, name: "f.txt" }]);
  await store.write([
    { s: "outbox", k: ID3, v: { id: ID3, v: 1, to: "peer/desk", kind: "message", body: "c", at: 5, attachments: [{ ...a1, blob: { ...a1.blob, id: "blob3" } }] } },
    { s: "inbox", k: "h1".padEnd(32, "0"), v: { id: "h1".padEnd(32, "0"), v: 2, conv: "d".repeat(64), lid: "e".repeat(32), from: "me/laptop", fp: "77777777-77777777-77777777-77777777", kind: "message", body: "hist", at: 4, history: true, attachments: [{ blob: null, name: "f.txt", size: plain.length, sha256: SHA }] } }]);
  const ops3 = await e.dropCached(await store.get("outbox", ID3), retr(ID3));
  check(!ops3.some((o) => o.k === "kept/" + SHA), "a history copy showing the same bytes keeps the kept copy");

  // Collision + shared bytes: a RECEIVED message with the same id as a sent
  // one shows the same bytes; retracting the sent one keeps the kept copy
  // and the received ciphertext, dropping only the sent row's own blob.
  const X = "4".repeat(32);
  await store.write([
    { s: "inbox", k: X, v: { id: X, v: 1, from: "peer/desk", fp: PEER, kind: "message", body: "theirs", at: 6, attachments: [{ ...a1, blob: { ...a1.blob, id: "blobP" } }] } },
    { s: "outbox", k: X, v: { id: X, v: 1, to: "peer/desk", kind: "message", body: "mine", at: 7, attachments: [{ ...a1, blob: { ...a1.blob, id: "blobM" } }] } },
    { s: "files", k: "ct/blobP", v: { ct: new Uint8Array([9]) } }, { s: "files", k: "ct/blobM", v: { ct: new Uint8Array([8]) } },
  ]);
  const ops4 = await e.dropCached(await store.get("outbox", X), retr(X));
  check(ops4.some((o) => o.k === "ct/blobM") && !ops4.some((o) => o.k === "ct/blobP") && !ops4.some((o) => o.k === "kept/" + SHA),
    "retracting my message keeps the received same-id message's bytes and the shared kept copy: " + JSON.stringify(ops4.map((o) => o.k)));
  const opsP = await e.dropCached(await store.get("inbox", X), { control: true, sub: wire.SubRetraction, ref: { id: X, fingerprint: PEER }, conv: "" });
  check(opsP.some((o) => o.k === "ct/blobP") && !opsP.some((o) => o.k === "ct/blobM"), "their retraction of the received same-id message drops only its own ciphertext: " + JSON.stringify(opsP.map((o) => o.k)));
}

// 3. Resolution order: the highest counter wins whatever the arrival order;
// remove after add hides; the author's latest revision shows.
{
  const { store, e } = await fresh();
  await store.write([{ s: "inbox", k: ID, v: { id: ID, v: 1, from: "peer/desk", fp: PEER, kind: "message", body: "theirs", at: 1 } }]);
  const rows = [
    ctl("k2".padEnd(32, "0"), wire.SubReaction, JSON.stringify({ emoji: "👍", op: "remove", n: 2 }), { id: ID, fingerprint: PEER }),
    ctl("k1".padEnd(32, "0"), wire.SubReaction, JSON.stringify({ emoji: "👍", op: "add", n: 1 }), { id: ID, fingerprint: PEER }),
    ctl("k3".padEnd(32, "0"), wire.SubRevision, JSON.stringify({ rev: 2, text: "second" }), { id: ID, fingerprint: PEER }),
    ctl("k4".padEnd(32, "0"), wire.SubRevision, JSON.stringify({ rev: 1, text: "first" }), { id: ID, fingerprint: PEER }),
  ];
  await store.write(rows.map((r) => ({ s: "inbox", k: r.id, v: r })));
  const m = (await e.thread(ID)).messages[0];
  check(!(m.reactions || []).length, "a removal with a higher counter wins over the earlier add, whatever the order");
  check(m.edited && m.text === "second" && m.revision === 2, "the highest revision shows: " + JSON.stringify([m.edited, m.text, m.revision]));
}

// 4. An older peer: no ctl3 in its signed capabilities, or a relay without
// env3, is refused by name; nothing is sent as an older version.
{
  const { store, e } = await fresh();
  await store.write([{ s: "inbox", k: ID, v: { id: ID, v: 1, from: "peer/desk", fp: PEER, kind: "message", body: "theirs", at: 1 } }]);
  e.sendKey = async () => ({ fingerprint: PEER });
  e.pubOf = async () => ({ sign_key: null });
  e.featureList = ["env2", "caps", "person2"];
  let why = "";
  try { await e.messageControl("react", { id: ID, dir: "in", emoji: "👍" }); } catch (err) { why = err.message; }
  check(why.includes("cannot carry"), "a relay without env3 refuses controls: " + why);
  e.featureList = ["env2", "caps", "person2", "env3"];
  e.profile = async () => ({ sessions: ["s1"], caps: [] });
  why = "";
  try { await e.messageControl("react", { id: ID, dir: "in", emoji: "👍" }); } catch (err) { why = err.message; }
  check(why.startsWith("peer_update: peer/desk cannot read reactions, edits or deletions yet"), "a peer without ctl3 is refused by name with an update cause: " + why);
  check((await store.all("outbox")).length === 0, "nothing was queued for the older peer");
  why = "";
  try { await e.messageControl("edit", { id: ID, dir: "in", text: "x" }); } catch (err) { why = err.message; }
  check(why.includes("Only the sender edits"), "editing their message is refused before any send: " + why);
}

// 5. Fan on version 3: allowed on a conversation control (validated as a
// turn's), refused on a device-thread control. Real keys, real seal.
{
  const keys = await wire.newKeys(), to = await wire.publicEntry(keys, "peer/desk");
  const base = { v: wire.Version3, id: ID, from: "me/phone", to: "peer/desk", ts: 1700000000, kind: "message", sub: wire.SubReaction,
    body: JSON.stringify({ emoji: "👍", op: "add", n: 1 }), ref: { id: ID2, fingerprint: PEER } };
  const conv = "d".repeat(64), lid = "e".repeat(32), fan = [{ person: "1".repeat(32), roster: "f".repeat(64) }, { person: "2".repeat(32), roster: "0".repeat(64) }];
  let ok = true; try { await wire.seal({ ...base, conv, lid, fan, replica: true }, keys, to); } catch (err) { ok = false; console.error(err.message); }
  check(ok, "a conversation control seals with a fan and as a replica");
  let bad = ""; try { await wire.seal({ ...base, fan }, keys, to); } catch (err) { bad = err.message; }
  check(bad.includes("no fan"), "a device-thread control with a fan is refused: " + bad);
  bad = ""; try { await wire.seal({ ...base, conv, lid, fan: [fan[0], fan[0]] }, keys, to); } catch (err) { bad = err.message; }
  check(bad.includes("invalid fan"), "a fan naming one person twice is refused: " + bad);
}

// 6. Retention: an authorized retraction blanks a plain message's text and
// its revision texts in every row of that scope; a question keeps its body
// (the admitted execution input); a forged retraction blanks nothing.
{
  const { store, e } = await fresh();
  const Q = "5".repeat(32);
  await store.write([
    { s: "inbox", k: ID, v: { id: ID, v: 1, from: "peer/desk", fp: PEER, kind: "message", body: "plain text", at: 1 } },
    { s: "inbox", k: Q, v: { id: Q, v: 1, from: "peer/desk", fp: PEER, kind: "question", body: "what time?", at: 2, state: "held" } },
    { s: "inbox", k: "rv".padEnd(32, "0"), v: ctl("rv".padEnd(32, "0"), wire.SubRevision, JSON.stringify({ rev: 1, text: "plain text v2" }), { id: ID, fingerprint: PEER }) },
  ]);
  const pin = { fingerprint: PEER }, env = (id) => ({ id, from: "peer/desk", to: "me/phone" });
  const retraction = (id, fp) => ({ v: 3, conv: "", lid: "", replica: false, kind: "message", sub: wire.SubRetraction, body: "{}", ref: { id, fingerprint: fp } });
  let forged = "";
  try { await e.admitControl(retraction(ID, SELF), env("f1".padEnd(32, "0")), pin); } catch (err) { forged = err.reason; }
  check(forged === "proof_pending" || forged === "invalid", "a retraction under a key that did not send it moves nothing: " + forged);
  check((await store.get("inbox", ID)).body === "plain text", "and the text is untouched");
  const ops = await e.admitControl(retraction(ID, PEER), env("r1".padEnd(32, "0")), pin);
  await store.write(ops);
  check((await store.get("inbox", ID)).body === "" && (await store.get("inbox", "rv".padEnd(32, "0"))).body === "", "the author's retraction blanks the text and its revision text");
  const opsQ = await e.admitControl(retraction(Q, PEER), env("r2".padEnd(32, "0")), pin);
  await store.write(opsQ);
  const q = await store.get("inbox", Q);
  check(q.body === "what time?", "a retracted question keeps its admitted text (disclosed under Details)");
  const vm = (await e.thread(ID)).messages.find((m) => m.id === ID), vq = (await e.thread(Q)).messages.find((m) => m.id === Q);
  check(vm.deleted && !vm.can.length && vq.deleted && vq.body === "what time?", "both show as deleted with no actions; the question's original stays in the view for Details");
}

// makeWorld builds Alice (laptop + phone, two roster steps) and Bob with
// real keys and rosters, a DM root by the laptop, and both Alice engines.
async function makeWorld() {
  const keysA = await wire.newKeys(), keysP = await wire.newKeys(), keysB = await wire.newKeys();
  const pubA = await wire.publicEntry(keysA, "alice/laptop"), pubP = await wire.publicEntry(keysP, "alice/phone"), pubB = await wire.publicEntry(keysB, "bob/desk");
  const fpA = await wire.fingerprint(pubA), fpP = await wire.fingerprint(pubP), fpB = await wire.fingerprint(pubB);
  const rA0 = await wire.newRoster(keysA, "alice/laptop", "Alice");
  const H0 = await wire.rosterHash(rA0);
  const join = await wire.joinConsent(keysP, "alice/phone", rA0.person, 1, H0);
  const rA1 = await wire.nextRoster(keysA, "alice/laptop", rA0, [pubA, pubP], join);
  const H1 = await wire.rosterHash(rA1);
  const rB0 = await wire.newRoster(keysB, "bob/desk", "Bob");
  const HB = await wire.rosterHash(rB0);
  const pinOf = (address, pub, fp) => ({ address, json: wire.marshalPublic(pub), fingerprint: fp, pending: null });
  const device = async (keys, address, fp) => {
    const store = memoryStore();
    const e = new Engine({ store, base: "http://test", fetch: async () => { throw new Error("no network in this check"); } });
    Object.assign(e, { address, fp, keys });
    e.me = { ...(await e.personRecord([rA0, rA1], "self", null)), state: "self" };
    const bob = { ...(await e.personRecord([rB0], "pinned", null)), state: "pinned" };
    await store.write([{ s: "kv", k: "person", v: e.me }, { s: "persons", k: bob.person, v: bob },
      { s: "pins", k: "bob/desk", v: pinOf("bob/desk", pubB, fpB) }, { s: "pins", k: "alice/laptop", v: pinOf("alice/laptop", pubA, fpA) }, { s: "pins", k: "alice/phone", v: pinOf("alice/phone", pubP, fpP) }]);
    return { store, e, bob };
  };
  const A = await device(keysA, "alice/laptop", fpA), P = await device(keysP, "alice/phone", fpP);
  const root = await wire.newRoot(keysA, { person: A.e.me.person, roster: H0, address: "alice/laptop", fingerprint: fpA }, { person: A.bob.person, roster: HB });
  const convId = await wire.rootID(root);
  const convRec = { id: convId, root: wire.rootJSON(root), peer: A.bob.person, created: root.created, creator: "alice/laptop" };
  for (const d of [A, P]) await d.store.write([{ s: "convs", k: convId, v: convRec }]);
  return { keysA, keysP, keysB, pubA, pubP, pubB, fpA, fpP, fpB, H0, H1, HB, A, P, convId, convRec, pinB: pinOf("bob/desk", pubB, fpB), rootJSON: wire.rootJSON(root) };
}

// An own-host capability check precedes confirmation, and is entirely inert.
{
 const {keysB,fpA,fpB,A}=await makeWorld(),e=A.e,store=A.store;
 e.me={state:'self',person:'a'.repeat(32),human_keys:[fpA],devices:[{address:e.address,fingerprint:fpA},{address:'bob/desk',fingerprint:fpB}]};
 e.featureList=['env2','env3','caps','person2'];
 const session='7'.repeat(32),body={host:'bob/desk',id:'8'.repeat(32),key:fpA,attempt:1,action:'resolve',expect:'needs_human',report:'',check:true};
 const profile=async caps=>({sessions:[session],caps:[wire.capsJSON(await wire.newCaps(keysB,'bob/desk',session,caps))]});
 e.profile=async()=>profile([wire.CapEnv2,wire.CapHeadless]);
 let refused='';try{await e.decide(body)}catch(x){refused=x.message}
 check(refused.includes('own requests')&&!refused.includes('reactions'),'own check names the relevant missing capability');
 e.profile=async()=>profile([wire.CapEnv2,wire.CapHeadless,wire.CapOwnSyncV3]);
 const checked=await e.decide(body);check(/Nothing has been sent/.test(checked.note),'supported check is not a decision');
 check((await store.all('outbox')).length===0,'preflight does not queue or grant');
 e.profile=async()=>profile([wire.CapEnv2,wire.CapHeadless]);
 refused='';try{await e.decide({...body,check:false,send_id:'9'.repeat(32)})}catch(x){refused=x.message}
 check(refused.includes('own requests'),'actual send rechecks after capability changed');
 check((await store.all('outbox')).length===0,'failed real send queues no decision');
 refused='';try{await e.decide({...body,action:'accept'})}catch(x){refused=x.message}
 check(/Only an own waiting/.test(refused),'check flag cannot bypass another action');
}

// 7. Stale-roster forwarding (0930av): Bob's control names Alice's OLD
// roster (before her phone was linked). Her laptop, which knows the new
// roster, forwards the control as history to the phone; the phone admits
// that real envelope and both devices resolve the same view. Real keys,
// real rosters, real seal/open; no network.
{
  const { keysA, keysP, keysB, pubA, pubP, pubB, fpA, fpP, fpB, H0, H1, HB, A, P, convId, convRec, pinB } = await makeWorld();
  void keysA; void keysP; void keysB; void pubA; void pubP; void pubB; void fpP;
  check(A.e.me.hash === H1 && A.e.me.steps.length === 2 && A.e.me.steps[0].hash === H0 && !A.e.me.steps[0].devices.some((d) => d.startsWith("alice/phone|")), "the laptop knows both roster steps; the old one lacks the phone");
  const L = "7".repeat(32), T = "8".repeat(32);
  const turn = { id: T, v: 2, conv: convId, lid: L, from: "bob/desk", fp: fpB, kind: "message", body: "hello from bob", at: 1, read: true, attachments: undefined };
  for (const d of [A, P]) await d.store.write([{ s: "inbox", k: T, v: turn }]);
  let seq = 0;
  const control = (sub, body, roster) => ({ v: 3, conv: convId, lid: (++seq).toString().padStart(32, "0"), replica: false, kind: "message", sub, body,
    ref: { id: L, fingerprint: fpB }, fan: [{ person: A.e.me.person, roster }, { person: A.bob.person, roster: HB }] });
  const env = () => ({ id: "e" + (seq).toString().padStart(31, "0"), from: "bob/desk", to: "alice/laptop", ts: 1700000000, attn: false, chan: "" });
  // deliver: the laptop admits Bob's control and forwards; the phone admits the forwarded envelope
  const relay = async (n) => {
    const ops = await A.e.admitControl(n, env(), pinB);
    await A.store.write(ops);
    const forwards = ops.filter((o) => o.s === "outbox");
    for (const f of forwards) {
      const e2 = wire.parseEnvelope(f.v.envelope);
      const popsP = await P.e.admitInner(f.v.envelope, e2);
      await P.store.write(popsP);
    }
    return forwards;
  };
  const same = async () => {
    const mA = (await A.e.dm(convId)).messages.find((m) => m.id === T), mP = (await P.e.dm(convId)).messages.find((m) => m.id === T);
    const pick = (m) => JSON.stringify({ reactions: m.reactions || [], edited: !!m.edited, text: m.text || "", deleted: !!m.deleted, body: m.body });
    return [pick(mA), pick(mP)];
  };
  // a control naming the CURRENT roster forwards nothing: Bob sends the
  // phone its own copy (delivered here directly, as the relay would)
  const cur = control(wire.SubReaction, JSON.stringify({ emoji: "👀", op: "add", n: 1 }), H1);
  const none = await relay(cur);
  check(none.length === 0, "a control naming the current roster is not forwarded (" + none.length + ")");
  await P.store.write(await P.e.admitControl({ ...cur, replica: false }, { ...env(), to: "alice/phone" }, pinB));
  // a reaction naming the OLD roster: forwarded to the phone as history
  const fw = await relay(control(wire.SubReaction, JSON.stringify({ emoji: "👍", op: "add", n: 1 }), H0));
  check(fw.length === 1 && fw[0].v.to === "alice/phone" && fw[0].v.sub === "history" && fw[0].v.aside, "the laptop forwards Bob's stale-roster reaction to the phone as history: " + JSON.stringify(fw.map((f) => [f.v.to, f.v.sub])));
  let [vA, vP] = await same();
  check(vA === vP && vP.includes('"emoji":"👍"') && vP.includes('"label":"Bob"'), "phone and laptop show the same reaction by Bob: " + vP);
  // an edit by Bob of his own turn, stale roster
  await relay(control(wire.SubRevision, JSON.stringify({ rev: 1, text: "hello from bob (edited)" }), H0));
  [vA, vP] = await same();
  check(vA === vP && vP.includes('"edited":true') && vP.includes("(edited)"), "phone and laptop show the same edit: " + vP);
  // Bob's retraction, stale roster: both deleted, text blanked on both (a plain message)
  await relay(control(wire.SubRetraction, "{}", H0));
  [vA, vP] = await same();
  check(vA === vP && vP.includes('"deleted":true') && vP.includes('"body":""'), "phone and laptop both show it deleted with the text blanked: " + vP);
  check((await P.store.get("inbox", T)).body === "" && (await A.store.get("inbox", T)).body === "", "the retracted text is gone from both stores");
  // a forged retraction (another person's key named as the author) forwarded as history moves nothing on the phone
  const forgedItem = { from: "bob/desk", from_key: fpB, id: "9".repeat(32), lid: "6".repeat(32), ts: 1700000001, at: 5, kind: "message", body: "{}", sub: wire.SubRetraction, ref: { id: L, fingerprint: fpA }, reply_to: "", status: "", origin: "", emotion: "", pid: "", target: null, attachments: [] };
  const forged = await A.e.historyCopy(A.e.me.devices.find((d) => d.address === "alice/phone"), convRec, forgedItem);
  await P.store.write(await P.e.admitInner(forged.envelope, wire.parseEnvelope(forged.envelope)));
  const mine = { id: "5".repeat(32), v: 2, conv: convId, lid: "4".repeat(32), to: "bob/desk", kind: "message", body: "my own turn", at: 6 };
  await P.store.write([{ s: "outbox", k: mine.id, v: mine }]);
  const forgedMine = { ...forgedItem, id: "3".repeat(32), lid: "2".repeat(32), ref: { id: mine.lid, fingerprint: fpP } };
  const fm = await A.e.historyCopy(A.e.me.devices.find((d) => d.address === "alice/phone"), convRec, forgedMine);
  await P.store.write(await P.e.admitInner(fm.envelope, wire.parseEnvelope(fm.envelope)));
  const mineView = (await P.e.dm(convId)).messages.find((m) => m.id === mine.id);
  check(!mineView.deleted && (await P.store.get("outbox", mine.id)).body === "my own turn", "Bob's history-carried retraction of Alice's own turn deletes nothing on the phone");
}

// 8. Headless (hdl1): a status is accepted only from the executor of a
// request THIS device sent; the highest counter wins; a terminal answer
// supersedes it; a decision is never admitted here (this browser hosts
// nothing); a version 2 report is read as a snapshot, actionable with its
// excerpt exactly as the host said; a decision needs what the host
// reported (see 10 for the operator flow).
{
  const { store, e } = await fresh();
  const Q = "6".repeat(32), OTHER = "7".repeat(32);
  await store.write([
    { s: "outbox", k: Q, v: { id: Q, v: 1, to: "peer/desk", kind: "question", body: "what time?", at: 1, state: "delivered" } },
    { s: "outbox", k: OTHER, v: { id: OTHER, v: 1, to: "third/box", kind: "task", body: "run it", at: 2, state: "delivered" } },
  ]);
  // The relay says "connected" (protocol.PresenceConnected); the clock is the
  // statuses' own, so a running word is current until EXEC_RUNNING_MAX_AGE.
  e.members = { listed: "listed", current: true, at: 1, list: [{ address: "peer/desk", presence: "connected" }], truncated: false };
  e.now = () => 1700000200_000;
  const pin = { fingerprint: PEER }, env = (id, from = "peer/desk") => ({ id, from, to: "me/phone" });
  const st = (state, n, at, id, fp = SELF, attempt = 4) => ({ v: 3, conv: "", lid: "", replica: false, kind: "message", sub: wire.SubStatus, body: JSON.stringify({ state, n, at, attempt, detail: state === "needs_human" ? "awaiting approval of a shell command" : "" }), ref: { id, fingerprint: fp } });
  let held = "";
  try { await e.admitControl(st("running", 1, 1700000000, OTHER), env("s0".padEnd(32, "0")), pin); } catch (err) { held = err.reason; }
  check(held === "proof_pending", "a status from a device that does not execute that request is held: " + held);
  held = "";
  try { await e.admitControl(st("running", 1, 1700000000, Q, PEER), env("s1".padEnd(32, "0")), pin); } catch (err) { held = err.reason; }
  check(held === "proof_pending", "a status whose ref names another requester key is held: " + held);
  await store.write(await e.admitControl(st("running", 2, 1700000100, Q), env("s2".padEnd(32, "0")), pin));
  await store.write(await e.admitControl(st("queued", 1, 1700000000, Q), env("s3".padEnd(32, "0")), pin)); // arrives late
  let m = (await e.thread(Q)).messages.find((x) => x.id === Q);
  check(m.exec && m.exec.state === "running" && m.exec.attempt === 4 && m.exec.host === "peer/desk" && m.exec.stale === false, "the highest counter wins independently of the signed attempt; the executor is named; online host = not stale: " + JSON.stringify(m.exec));
  e.now = () => (1700000100 + EXEC_RUNNING_MAX_AGE + 1) * 1000;
  m = (await e.thread(Q)).messages.find((x) => x.id === Q);
  check(m.exec.stale === true, "a running word older than the bound is old news even from a connected host (client ExecView.settle)");
  e.now = () => 1700000200_000;
  e.members = { ...e.members, list: [{ address: "peer/desk", presence: "offline" }] };
  m = (await e.thread(Q)).messages.find((x) => x.id === Q);
  check(m.exec.stale === true, "a disconnected host makes the last word stale");
  await store.write(await e.admitControl(st("needs_human", 3, 1700000200, Q), env("s4".padEnd(32, "0")), pin));
  m = (await e.thread(Q)).messages.find((x) => x.id === Q);
  check(m.exec.state === "needs_human" && m.exec.attempt === 4 && m.exec.detail.includes("shell command"), "a needs-human state carries its bounded detail");
  await store.write([{ s: "inbox", k: "an".padEnd(32, "0"), v: { id: "an".padEnd(32, "0"), v: 1, from: "peer/desk", fp: PEER, kind: "answer", reply_to: Q, body: "four", at: 3 } }]);
  m = (await e.thread(Q)).messages.find((x) => x.id === Q);
  check(!m.exec, "a terminal answer supersedes any status");
  let refused = "";
  try { await e.admitControl({ v: 3, conv: "", lid: "", replica: false, kind: "message", sub: wire.SubDecision, body: JSON.stringify({ action: "accept", expect: "awaiting", attempt: 1, text: "", report: "" }), ref: { id: Q, fingerprint: SELF } }, env("d1".padEnd(32, "0")), pin); } catch (err) { refused = err.reason + ":" + err.message; }
  check(refused.startsWith("invalid") && refused.includes("hosts no requests"), "a decision addressed to this browser is refused: " + refused);
  // a version 2 report notice: parsed as a snapshot, no excerpt shown here, never actionable
  const body = JSON.stringify({ v: 2, at: 1700000300, host: "hub/bot", items: [{ id: "c".repeat(32), from: "admin/laptop", key: PEER, kind: "task", state: "awaiting", blocker: "awaiting_acceptance", since: 1700000200, excerpt: "private text", actionable: true }] });
  await store.write([{ s: "inbox", k: "rp".padEnd(32, "0"), v: { id: "rp".padEnd(32, "0"), v: 1, from: "hub/bot", fp: PEER, kind: "message", status: "review_notice", body, at: 4 } }]);
  const items = e.reportItems(await store.all("inbox"));
  check(items.length === 1 && items[0].notice && items[0].report && items[0].report.host === "hub/bot" && items[0].report.items[0].blocker === "awaiting_acceptance" && items[0].report.items[0].actionable === true && items[0].report.items[0].excerpt === "private text" && items[0].report.items[0].key === PEER && items[0].report.items[0].attempt === 0,
    "a version 2 report is a snapshot: actionable with its excerpt as the host said, the requester key and attempt named: " + JSON.stringify(items[0].report));
  const quiet = JSON.stringify({ ...JSON.parse(body), items: [{ ...JSON.parse(body).items[0], excerpt: undefined, actionable: undefined }] });
  await store.write([{ s: "inbox", k: "rq".padEnd(32, "0"), v: { id: "rq".padEnd(32, "0"), v: 1, from: "hub/bot", fp: PEER, kind: "message", status: "review_notice", body: quiet, at: 5 } }]);
  const quietItem = e.reportItems(await store.all("inbox")).find((x) => x.id === "rq".padEnd(32, "0")).report.items[0];
  check(quietItem.actionable === false && !("excerpt" in quietItem), "a report to a device that is no operator there is read-only: no excerpt, not actionable");
  // a notice from one machine naming another as host is not that host's report (client.NoticeReport)
  await store.write([{ s: "inbox", k: "rf".padEnd(32, "0"), v: { id: "rf".padEnd(32, "0"), v: 1, from: "third/box", fp: PEER, kind: "message", status: "review_notice", body, at: 6 } }]);
  const forged = e.reportItems(await store.all("inbox")).find((x) => x.id === "rf".padEnd(32, "0"));
  check(forged && !forged.report && !forged.excerpt.includes("reported by") && !e.noticeLine({ from: "third/box", body }).includes("reported by"), "a report is believed only from its host: " + JSON.stringify(forged));
  let dec = "";
  try { await e.api("/api/operator/decide", { host: "hub/bot", id: "c".repeat(32), action: "accept" }); } catch (err) { dec = err.message; }
  check(dec.includes("sender's key"), "a decision without the request's key as reported is refused: " + dec);
  // the codec: a decision must be device-scoped; a status body is strict
  let bad = "";
  try { wire.parseControl(wire.SubStatus, JSON.stringify({ state: "flying", n: 1, at: 1 })); } catch (err) { bad = err.message; }
  check(bad === "malformed status", "an unknown state is refused");
}

// 10. A granted operator's browser (hdl1, client.Decide/statusAllowed/
// decisionResults): a version 2 report from a pinned host names someone
// else's waiting request, its key, the host's own state name, attempt, and
// says this device may decide it (as the host granted its exact key). The
// decision goes sealed to the host, bound to exactly that; the host's
// answer (a status naming the decision, report and attempt) is admitted
// only for the very decision sent, attached to that item, never taken as
// the request's execution state; mismatched and forged answers are
// refused; asking again before the answer sends nothing twice; a host
// without hdl1 is refused by name; the outcome is the answer, not delivery.
{
  const { keysA, keysB, pubA, pubB, fpA, fpB, A, pinB } = await makeWorld();
  void pubB; void fpA;
  const e = A.e, store = A.store;
  const D = "dave/box", fpD = "01234567-89abcdef-01234567-89abcdef"; // the requester, elsewhere: never pinned here, nothing of its request is here
  const REQ = "d1".padEnd(32, "0"), RP = "e1".padEnd(32, "0"), RP2 = "e2".padEnd(32, "0");
  const item = { id: REQ, from: D, key: fpD, kind: "task", state: "awaiting", blocker: "awaiting_acceptance", since: 1700000200, attempt: 1, excerpt: "deploy the thing", actionable: true };
  await store.write([{ s: "inbox", k: RP, v: { id: RP, v: 1, from: "bob/desk", fp: fpB, kind: "message", status: "review_notice", body: JSON.stringify({ v: 2, at: 1700000300, host: "bob/desk", items: [item] }), at: 4 } }]);
  const itemsOf = async () => e.reportItems(await store.all("inbox"), await store.all("outbox"));
  let it = (await itemsOf())[0].report.items[0];
  check(it.actionable === true && it.excerpt === "deploy the thing" && it.key === fpD && it.attempt === 1 && it.state === "awaiting" && !it.result, "an operator's report item: excerpt, key, host state, attempt, no result yet: " + JSON.stringify(it));
  e.featureList = ["env2", "caps", "person2", "env3"];
  const S1 = "5".repeat(32);
  const profileWith = async (caps) => ({ sessions: [S1], caps: [wire.capsJSON(await wire.newCaps(keysB, "bob/desk", S1, caps))] });
  e.profile = async () => profileWith([wire.CapEnv2, wire.CapControl]);
  const body = { host: "bob/desk", id: REQ, key: fpD, action: "accept", expect: "awaiting", attempt: 1, text: "", report: RP };
  let why = "";
  try { await e.api("/api/operator/decide", body); } catch (err) { why = err.message; }
  check(why.includes("cannot receive operator decisions"), "a host without hdl1 is refused by name: " + why);
  check((await store.all("outbox")).length === 0, "nothing was queued for it");
  const refuse = async (b) => { try { await e.api("/api/operator/decide", b); return ""; } catch (err) { return err.message; } };
  check((await refuse({ ...body, key: "nope" })).includes("sender's key"), "a decision needs the request's id and its sender's key");
  check((await refuse({ ...body, report: "9".repeat(32) })).includes("not one bob/desk sent"), "a decision names a report that host sent this device");
  check((await refuse({ ...body, action: "reply" })).includes("Write the answer"), "a reply needs its text");
  check((await refuse({ ...body, expect: "Awaiting!" })).includes("state and attempt"), "the state is the host's own token");
  check((await refuse({ ...body, host: "alice/laptop" })).includes("holds no requests"), "this browser is never the host");
  check((await store.all("outbox")).length === 0, "refusals queue nothing");
  e.profile = async () => profileWith([wire.CapEnv2, wire.CapControl, wire.CapHeadless]);
  const r1 = await e.api("/api/operator/decide", body);
  let out = await store.all("outbox");
  check(out.length === 1 && r1.sent === out[0].id && out[0].control && out[0].sub === wire.SubDecision && out[0].to === "bob/desk" && out[0].aside && /answer shows here/.test(r1.note) && /delivery decides nothing/.test(r1.note),
    "one decision queued for the host; the note promises its answer, never an outcome: " + r1.note);
  // What the host opens: a version 3 device-scoped decision from this key, bound exactly to what was reported.
  const opened = await wire.open(out[0].envelope, keysB, "bob/desk", { address: "alice/laptop", sign_key: pubA.sign_key });
  const made = wire.parseControl(wire.SubDecision, opened.body);
  check(opened.v === 3 && opened.kind === "message" && opened.sub === wire.SubDecision && !opened.conv && opened.ref.id === REQ && opened.ref.fingerprint === fpD && opened.from === "alice/laptop" && opened.to === "bob/desk"
    && made.action === "accept" && made.expect === "awaiting" && made.attempt === 1 && made.report === RP && made.text === "",
    "the sealed decision: v3, device-scoped, ref = request id + requester key, body bound to action/expect/attempt/report: " + JSON.stringify({ ...opened, body: made }));
  // Asked again before the answer: nothing is sent twice.
  const r2 = await e.api("/api/operator/decide", body);
  check(r2.sent === r1.sent && /Already sent/.test(r2.note) && (await store.all("outbox")).length === 1, "the same decision again waits for the answer instead of going twice: " + r2.note);
  // A different decision (decline, with a reason) is another one.
  const r3 = await e.api("/api/operator/decide", { ...body, action: "decline", text: "not today" });
  check(r3.sent !== r1.sent && (await store.all("outbox")).length === 2, "a different decision is sent as its own");
  // The host's answers: a status naming the decision, echoing report and attempt.
  const answer = (decision, extra, from = "bob/desk", ref = { id: REQ, fingerprint: fpD }) => ({ v: 3, conv: "", lid: "", replica: false, kind: "message", sub: wire.SubStatus, body: JSON.stringify({ state: "queued", n: 1, at: 1700000400, decision, report: RP, attempt: 1, ...extra }), ref });
  const env = (id, from = "bob/desk") => ({ id, from, to: "alice/laptop" });
  const held = async (n, id, pin = pinB, from = "bob/desk") => { try { await e.admitControl(n, env(id, from), pin); return "admitted"; } catch (err) { return err.reason + ": " + err.message; } };
  check((await held(answer("f".repeat(32), {}), "b1".padEnd(32, "0"))).startsWith("invalid: it answers a decision this device did not send"), "an answer naming a decision never sent is refused");
  check((await held(answer(r1.sent, { attempt: 2 }), "b2".padEnd(32, "0"))).startsWith("invalid: it answers a decision this device did not make"), "an answer echoing another attempt is refused");
  check((await held(answer(r1.sent, { report: RP2 }), "b3".padEnd(32, "0"))).startsWith("invalid: it answers a decision this device did not make"), "an answer echoing another report is refused");
  check((await held(answer(r1.sent, {}), "b4".padEnd(32, "0"), { fingerprint: "deadbeef-deadbeef-deadbeef-deadbeef" }, "carol/pc")).startsWith("invalid: it answers a decision this device did not send"), "an answer from a device the decision did not go to is refused");
  check((await held(answer(r1.sent, {}, "bob/desk", { id: REQ, fingerprint: fpB }), "b5".padEnd(32, "0"))).startsWith("invalid: it answers a decision this device did not send"), "an answer about another key's request is refused");
  check((await held({ ...answer(r1.sent, {}), conv: "c".repeat(64) }, "b6".padEnd(32, "0"))).startsWith("invalid"), "an answer in a conversation is refused");
  check((await held(answer("", { state: "awaiting", refused: "" }), "b7".padEnd(32, "0"))).startsWith("proof_pending"), "a plain status about someone else's request is not this device's to keep");
  await store.write(await e.admitControl(answer(r1.sent, { state: "queued" }), env("a1".padEnd(32, "0")), pinB));
  it = (await itemsOf())[0].report.items[0];
  check(it.result && it.result.decision === r1.sent && it.result.state === "queued" && !it.result.refused, "the host's answer is the item's result: " + JSON.stringify(it.result));
  await store.write(await e.admitControl(answer(r3.sent, { state: "not_run", refused: "the request is no longer as you saw it (now accepted, attempt 1)", at: 1700000401 }), env("a2".padEnd(32, "0")), pinB));
  it = (await itemsOf())[0].report.items[0];
  check(it.result.decision === r3.sent && it.result.refused.includes("no longer as you saw it"), "a later refusal of the other decision replaces it, with the host's reason: " + JSON.stringify(it.result));
  // Answered: the same decision asked again now goes again (the host decides on its state).
  const r4 = await e.api("/api/operator/decide", body);
  check(r4.sent !== r1.sent && (await store.all("outbox")).length === 3, "after the answer, asking again is a new decision for the host to judge");
  // Never exec: a request of someone else has no exec here, and answers never build one.
  const statuses = (await store.all("inbox")).filter((x) => x.control && x.sub === wire.SubStatus);
  check(statuses.length === 2 && e.execOn(statuses, "bob/desk", false) === null, "answers to decisions never stand in for a request's execution state");
  // A second report of the same request at a later attempt: results stay with the report and attempt they were made from.
  await store.write([{ s: "inbox", k: RP2, v: { id: RP2, v: 1, from: "bob/desk", fp: fpB, kind: "message", status: "review_notice", body: JSON.stringify({ v: 2, at: 1700000500, host: "bob/desk", items: [{ ...item, state: "failed", attempt: 2 }] }), at: 6 } }]);
  const later = (await itemsOf()).find((x) => x.id === RP2).report.items[0];
  check(!later.result && later.state === "failed" && later.attempt === 2, "an answer to a decision from the earlier report does not show on the later report's item");
  if (process.env.HEADLESS_VECTORS) { // sealed vectors for the core side: the decision as the host opens it, and its keys
    const fs = await import("node:fs");
    fs.writeFileSync(process.env.HEADLESS_VECTORS, JSON.stringify({ note: "browser operator decision: envelope sealed by alice/laptop (browser engine) to bob/desk; open with host_keys; expect inner below",
      host: { address: "bob/desk", public: wire.marshalPublic(pubB), keys: keysB }, operator: { address: "alice/laptop", public: wire.marshalPublic(pubA) },
      decision_envelope: out[0].envelope, decision_inner: { ...opened, body: made }, report: JSON.parse((await store.get("inbox", RP)).body) }, null, 1));
  }
}

// 11. A conversation's Drive space record (drive-space, drv1): a quiet
// version 2 message admitted like the core does. Bob publishes revision 1
// (owner: his person); revision 2 follows it (previous = its folder); a
// revision without its predecessor waits; a second record at the same
// revision is a fork; a later revision by another person is refused; the
// record never shows as a message; publishing from here goes only to
// devices that read it, the rest wait; the wire refuses a malformed one.
{
  const { keysA, keysB, pubA, pubB, fpB, A, convId, pinB, rootJSON } = await makeWorld();
  void keysB; void pubA; void pubB;
  const e = A.e, store = A.store;
  const bobPerson = A.bob.person;
  const space = (revision, folder, extra) => JSON.stringify({ conv: convId, folder, name: "Project", owner: bobPerson, revision, ...extra });
  let seq = 0;
  const rec = (body) => ({ v: 2, conv: convId, lid: (++seq).toString().padStart(32, "0"), replica: false, kind: "message", sub: wire.SubDriveSpace, body, reply_to: "", attachments: [], root: JSON.parse(rootJSON),
    fan: [{ person: A.e.me.person, roster: A.e.me.hash }, { person: bobPerson, roster: A.bob.hash }] });
  const env = () => ({ id: "f" + seq.toString().padStart(31, "0"), from: "bob/desk", to: "alice/laptop", ts: 1700000000, attn: false, chan: "" });
  const held = async (n) => { try { const ops = await e.admitControl ? await e.admitConv(n, env(), pinB, { id: env().id, from: "bob/desk", kind: "message", body: n.body, reply_to: "", at: 1, fp: fpB, read: false }) : null; await store.write(ops); return "admitted"; } catch (err) { return err.reason + ": " + err.message; } };
  const root = wire.parseRoot(await (async () => (await store.get("convs", convId)).root)());
  void root;
  check((await held({ ...rec(space(2, "F2", { previous: "F1" })) })).startsWith("proof_pending"), "revision 2 before revision 1 waits for its predecessor");
  check((await held({ ...rec(space(1, "F1", { owner: A.e.me.person })) })).startsWith("invalid"), "a first record naming another owner than its sender is refused");
  const adm1 = await held(rec(space(1, "F1")));
  check(adm1 === "admitted", "revision 1 from its owner is admitted: " + adm1);
  const latest1 = await e.latestSpace(convId);
  check(latest1 && latest1.revision === 1 && latest1.folder === "F1" && latest1.owner === bobPerson, "the space kept here is revision 1");
  check((await held(rec(space(1, "F9")))).startsWith("invalid"), "a second revision 1 with another folder is a fork");
  check((await held(rec(space(3, "F3", { previous: "F2" })))).startsWith("proof_pending"), "revision 3 waits for revision 2");
  check((await held(rec(space(2, "F2", { previous: "F0" })))).startsWith("invalid"), "revision 2 must follow the folder kept here");
  check((await held(rec(space(2, "F2", { previous: "F1" })))) === "admitted", "revision 2 following revision 1 is admitted");
  check((await e.latestSpace(convId)).revision === 2 && (await e.latestSpace(convId)).folder === "F2", "the space moved to revision 2");
  const dm = await e.dm(convId);
  check(!dm.messages.some((m) => m.sub === wire.SubDriveSpace || /"folder"/.test(m.body || "")), "space records are no messages of the DM");
  const ov = await e.overview();
  const d = (ov.dms || []).find((x) => x.id === convId);
  check(!d || (d.unread === 0 && !/folder/.test(d.last || "")), "space records neither count as unread nor title the DM");
  // publishing from here: the owner only; devices without drv1 get waiting copies
  e.featureList = ["env2", "caps", "person2", "env3"];
  e.supports = async () => [true, "", false];
  e.ctlSupport = async (address, pin, cap) => (cap === wire.CapDrive ? [false, address + " cannot read Drive space records yet"] : [true, ""]);
  e.refreshPerson = async (p) => p;
  e.post = async () => {};
  let why = "";
  try { await e.publishSpace({ conv: convId, folder: "F3", name: "Project", owner: bobPerson, revision: 3, previous: "F2" }); } catch (err) { why = err.message; }
  check(why.includes("owner only"), "only the owner's device publishes a later revision: " + why);
  const mineConv = await e.latestSpace(convId);
  void mineConv;
  const r = await e.publishSpace({ conv: convId, folder: "M1", name: "Mine", owner: A.e.me.person, revision: 1, previous: "" }).catch((err) => ({ error: err.message }));
  // the conversation already has bob's space (revision 2): a first revision by alice does not follow it
  check(r.error && /moved on|read it again/.test(r.error), "a record that does not follow the one kept here is not published: " + JSON.stringify(r));
  // the wire: a malformed record, or one for another conversation, is refused before sealing
  let bad = "";
  try { wire.parseDriveSpace(JSON.stringify({ conv: convId, folder: "bad folder!", name: "x", owner: "p", revision: 1 })); } catch (err) { bad = err.message; }
  check(bad.includes("invalid"), "a folder id outside the allowed characters is refused");
  bad = "";
  try { wire.parseDriveSpace(JSON.stringify({ conv: convId, folder: "F1", name: "x", owner: "p", revision: 1, token: "secret" })); } catch (err) { bad = err.message; }
  check(bad.includes("unknown field"), "a record carrying anything else (a token) is refused");
  const sealedBad = await wire.seal({ v: wire.Version2, id: "a".repeat(32), from: "alice/laptop", to: "bob/desk", ts: 1700000000, kind: "message", sub: wire.SubDriveSpace, body: space(1, "F1"), conv: "b".repeat(64), lid: "c".repeat(32), root: rootJSON, fan: null, attachments: [] }, keysA, await wire.parsePublic(JSON.parse(pinB.json))).catch((err) => "refused: " + err.message);
  check(typeof sealedBad === "string" && sealedBad.startsWith("refused"), "a record for another conversation than the message's is refused by the wire: " + String(sealedBad).slice(0, 80));
}

// 9. Ingestion order (0930be): once Bob's retraction of his turn is pinned,
// the turn arriving later (directly, sealed by Bob), a history copy of it,
// and a later revision are stored WITHOUT plain text; a retracted question
// keeps its admitted text; the view shows deleted either way.
{
  const { keysB, pubA, fpB, H0, HB, A, P, convId, pinB, rootJSON } = await makeWorld();
  const L = "9".repeat(32);
  const retraction = { v: 3, conv: convId, lid: "a1".padEnd(32, "0"), replica: false, kind: "message", sub: wire.SubRetraction, body: "{}", ref: { id: L, fingerprint: fpB },
    fan: [{ person: A.e.me.person, roster: H0 }, { person: A.bob.person, roster: HB }] };
  const env = (id, to = "alice/laptop") => ({ id, from: "bob/desk", to, ts: 1700000000, attn: false, chan: "" });
  // the retraction first, target absent: stored (conversation scope), nothing moves
  await A.store.write(await A.e.admitControl(retraction, env("c1".padEnd(32, "0")), pinB));
  // then the original turn, sealed by Bob, arrives directly
  const late = await wire.seal({ v: wire.Version2, id: "d1".padEnd(32, "0"), from: "bob/desk", to: "alice/laptop", ts: 1700000001, kind: "message", body: "late plain text",
    conv: convId, lid: L, root: rootJSON, fan: [{ person: A.e.me.person, roster: H0 }, { person: A.bob.person, roster: HB }] }, keysB, pubA);
  await A.store.write(await A.e.admitInner(late, wire.parseEnvelope(late)));
  const stored = await A.store.get("inbox", "d1".padEnd(32, "0"));
  check(stored && stored.body === "", "an original arriving after its retraction is stored without plain text: " + JSON.stringify(stored && stored.body));
  const view = (await A.e.dm(convId)).messages.find((m) => m.id === "d1".padEnd(32, "0"));
  check(view && view.deleted && view.body === "", "and shows as deleted");
  // a revision after the retraction keeps no text
  const revision = { ...retraction, lid: "a2".padEnd(32, "0"), sub: wire.SubRevision, body: JSON.stringify({ rev: 1, text: "sneaky revival" }) };
  await A.store.write(await A.e.admitControl(revision, env("e1".padEnd(32, "0")), pinB));
  const revRow = await A.store.get("inbox", "e1".padEnd(32, "0"));
  check(revRow && revRow.body === "" && !(await A.e.dm(convId)).messages.find((m) => m.id === "d1".padEnd(32, "0")).edited, "a revision arriving after the deletion is stored empty and shows nothing");
  // the phone: retraction (direct) then the turn as a history copy from the laptop
  await P.store.write(await P.e.admitControl({ ...retraction, lid: "a3".padEnd(32, "0") }, env("f1".padEnd(32, "0"), "alice/phone"), pinB));
  const item = { from: "bob/desk", from_key: fpB, id: "a2".padEnd(32, "0"), lid: L, ts: 1700000001, at: 7, kind: "message", body: "late plain text", reply_to: "", status: "", sub: "", origin: "", emotion: "", pid: "", target: null, attachments: [] };
  const copy = await A.e.historyCopy(A.e.me.devices.find((d) => d.address === "alice/phone"), { id: convId, root: rootJSON }, item);
  await P.store.write(await P.e.admitInner(copy.envelope, wire.parseEnvelope(copy.envelope)));
  const hist = await P.store.get("inbox", "a2".padEnd(32, "0"));
  check(hist && hist.body === "" && hist.history, "a history copy arriving after the deletion is stored without plain text");
  // a question keeps its admitted text even when retracted late
  const LQ = "8".repeat(31) + "1";
  await A.store.write(await A.e.admitControl({ ...retraction, lid: "a4".padEnd(32, "0"), ref: { id: LQ, fingerprint: fpB } }, env("c3".padEnd(32, "0")), pinB));
  const lateQ = await wire.seal({ v: wire.Version2, id: "d3".padEnd(32, "0"), from: "bob/desk", to: "alice/laptop", ts: 1700000002, kind: "question", body: "what time is it?",
    conv: convId, lid: LQ, root: rootJSON, fan: [{ person: A.e.me.person, roster: H0 }, { person: A.bob.person, roster: HB }] }, keysB, pubA);
  await A.store.write(await A.e.admitInner(lateQ, wire.parseEnvelope(lateQ)));
  const q = await A.store.get("inbox", "d3".padEnd(32, "0"));
  check(q && q.body === "what time is it?", "a question's admitted text stays on record");
  // my own deleted message cannot be edited from the page
  const mine = { id: "b2".padEnd(32, "0"), v: 2, conv: convId, lid: "e".repeat(32), to: "bob/desk", kind: "message", body: "mine", at: 9 };
  await A.store.write([{ s: "outbox", k: mine.id, v: mine }, { s: "outbox", k: "b3".padEnd(32, "0"), v: { id: "b3".padEnd(32, "0"), v: 3, control: true, conv: convId, lid: "e2".padEnd(32, "0"), to: "bob/desk", fp: A.e.fp, person: A.e.me.person, sub: wire.SubRetraction, body: "{}", ref: { id: mine.lid, fingerprint: A.e.fp }, at: 10, aside: true } }]);
  let why = "";
  try { await A.e.messageControl("edit", { conv: convId, id: mine.id, dir: "out", text: "revive" }); } catch (err) { why = err.message; }
  check(why.includes("was deleted"), "editing a deleted message is refused: " + why);
  // nor reacted to or deleted again (client TestControlsRefusedOnDeletedMessage)
  const queued = (await A.store.all("outbox")).length;
  for (const [what, extra] of [["react", { emoji: "👍" }], ["delete", {}]]) {
    why = "";
    try { await A.e.messageControl(what, { conv: convId, id: mine.id, dir: "out", ...extra }); } catch (err) { why = err.message; }
    check(why.includes("was deleted"), what + " on a deleted message is refused: " + why);
  }
  check((await A.store.all("outbox")).length === queued, "nothing was queued for a deleted message");
}

// A reaction composed here is one emoji, and taking off one that is not
// there is refused, both before anything is sent (client
// TestReactionIsOneEmoji, TestRemovingAbsentReactionRefused).
{
  const { store, e } = await fresh();
  await store.write([
    { s: "inbox", k: ID, v: { id: ID, v: 1, from: "peer/desk", fp: PEER, kind: "message", body: "theirs", at: 1 } },
    { s: "outbox", k: ID2, v: { ...ctl(ID2, wire.SubReaction, JSON.stringify({ emoji: "👍", op: "add", n: 1 }), { id: ID, fingerprint: PEER }, "me/phone"), to: "peer/desk", aside: true, state: "delivered" } },
    { s: "inbox", k: "c9".padEnd(32, "0"), v: ctl("c9".padEnd(32, "0"), wire.SubReaction, JSON.stringify({ emoji: "👀", op: "add", n: 1 }), { id: ID, fingerprint: PEER }) },
  ]);
  for (const emoji of ["$", "+", "€", "𠀀", "👍".repeat(12), "👍👍", "🇱🇻🇺🇸"]) {
    let why = "";
    try { await e.messageControl("react", { id: ID, dir: "in", emoji }); } catch (err) { why = err.message; }
    check(why === "A reaction is one emoji.", JSON.stringify(emoji) + " is refused as a reaction: " + why);
  }
  for (const emoji of ["🎉", "👀"]) { // never added here; the peer's own is not mine to take off
    let why = "";
    try { await e.messageControl("react", { id: ID, dir: "in", emoji, remove: true }); } catch (err) { why = err.message; }
    check(why.includes("no " + emoji + " reaction of yours"), "removing " + emoji + " that is not mine is refused: " + why);
  }
  let why = "";
  try { await e.messageControl("react", { id: ID, dir: "in", emoji: "👍", remove: true }); } catch (err) { why = err.message; }
  check(!why.includes("reaction of yours"), "removing my own reaction goes on to be sent: " + why);
  check((await store.all("outbox")).length === 1, "nothing was queued for a refused reaction");
  for (const [emoji, ok] of [["👍🏽", true], ["🇱🇻", true], ["✓", true], ["$", false], ["👍👍", false], ["𠀀", false],
    ["\u25fb\ufe0f", true], ["\u25fe", true], ["\u2934\ufe0f", true], ["\u2935\ufe0f", true], ["\u2800", false], ["\u2a00", false]]) check(wire.oneEmoji(emoji) === ok, "oneEmoji(" + JSON.stringify(emoji) + ")");
  check(wire.validEmoji("$") && wire.validEmoji("👍".repeat(12)), "what peers already sent stays readable");
}

// My own reaction added under the older rule (any validEmoji) shows as mine,
// so it can be taken off, though it is no longer composed here (client
// TestOwnOlderRuleReactionRemovable).
{
  const { store, e } = await fresh();
  const older = ["$", "👍👍"];
  await store.write([
    { s: "inbox", k: ID, v: { id: ID, v: 1, from: "peer/desk", fp: PEER, kind: "message", body: "theirs", at: 1 } },
    ...older.map((emoji, i) => {
      const id = ("d" + i).padEnd(32, "0");
      return { s: "outbox", k: id, v: { ...ctl(id, wire.SubReaction, JSON.stringify({ emoji, op: "add", n: 1 }), { id: ID, fingerprint: PEER }, "me/phone"), to: "peer/desk", aside: true, state: "delivered" } };
    }),
  ]);
  for (const emoji of older) {
    check(await e.reactedHere("", { id: ID, fingerprint: PEER }, emoji), JSON.stringify(emoji) + " shows as mine");
    let why = "";
    try { await e.messageControl("react", { id: ID, dir: "in", emoji }); } catch (err) { why = err.message; }
    check(why === "A reaction is one emoji.", JSON.stringify(emoji) + " is not added again: " + why);
    why = "";
    try { await e.messageControl("react", { id: ID, dir: "in", emoji, remove: true }); } catch (err) { why = err.message; }
    check(!why.includes("one emoji") && !why.includes("reaction of yours"), "removing my own " + JSON.stringify(emoji) + " goes on to be sent: " + why);
  }
}

// Text that shows nothing (white space and default ignorable characters
// only) is no message and no edit (client TestInvisibleTextRefused).
{
  const { store, e } = await fresh();
  await store.write([{ s: "outbox", k: ID, v: { id: ID, v: 1, to: "peer/desk", kind: "message", body: "mine", at: 2, state: "delivered" } }]);
  for (const text of ["ZWSP", "IDEOGRAPHIC_ZWSP", "JOINERS", "BRAILLE_BLANK"]) {
    const s = { ZWSP: String.fromCodePoint(0x200b), IDEOGRAPHIC_ZWSP: String.fromCodePoint(0x3000, 0x200b), JOINERS: " " + String.fromCodePoint(0x200d, 0x2060, 0xfeff), BRAILLE_BLANK: String.fromCodePoint(0x2800) }[text];
    let why = "";
    try { await e.sendDM({ conv: "c".repeat(64), body: s }); } catch (err) { why = err.message; }
    check(why === "Write a message or add a file first.", text + " is no message: " + why);
    why = "";
    try { await e.sendDirect({ to: "peer/desk", kind: "message", body: s }); } catch (err) { why = err.message; }
    check(why === "Write a message or add a file first.", text + " is no device message: " + why);
    why = "";
    try { await e.messageControl("edit", { id: ID, dir: "out", text: s }); } catch (err) { why = err.message; }
    check(why.startsWith("An edit is 1 to"), text + " is no edit: " + why);
  }
  check(!wire.blank("a" + String.fromCodePoint(0x200b) + "b") && !wire.blank(String.fromCodePoint(0x2800) + "x") && wire.blank(""), "visible text with a zero-width space or a braille blank is not blank");
}

// Equal counters from two devices of one person: the tie is broken on the
// control's logical id, the same in every copy, never on the id of the copy
// this device holds (client TestConcurrentControlsResolveAlikeOnEveryDevice).
// Two devices hold the same four controls under different copy ids.
{
  const { e } = await fresh();
  const T = "7".repeat(32), id = (c) => c.repeat(32);
  const row = (copy, lid, sub, pay) => ({ id: copy, lid, v: 3, control: true, conv: "c".repeat(64), from: "peer/desk", fp: PEER, person: "P", kind: "message", sub, body: JSON.stringify(pay), ref: { id: T, fingerprint: PEER }, at: 1 });
  for (const ids of [[id("f"), id("2"), id("5"), id("3")], [id("6"), id("9"), id("c"), id("7")]]) {
    const rows = [row(ids[0], id("1"), wire.SubRevision, { rev: 1, text: "from the desk" }), row(ids[1], id("e"), wire.SubRevision, { rev: 1, text: "from the phone" }),
      row(ids[2], id("4"), wire.SubReaction, { emoji: "👍", op: "add", n: 1 }), row(ids[3], id("d"), wire.SubReaction, { emoji: "👍", op: "remove", n: 1 })];
    const v = e.controlsOn(rows, "P", (x) => x.person || "", () => "", () => false);
    check(v.edited && v.text === "from the phone" && !(v.reactions || []).length, "equal counters resolve on the logical id, whatever the copy ids " + ids.map((x) => x[0]).join("") + ": " + JSON.stringify(v));
  }
}

// A device removed from its person's roster (client
// TestControlOfRemovedDeviceMessage; t5-held): its person still edits and
// deletes what it sent, from a current device; another person's edit of
// it is refused with the specific code; a key no one is known to have had
// is refused without claiming another person sent it.
{
  const { keysA, keysB, pubA, pubB, fpP, fpB, A, convId, pinB } = await makeWorld();
  const oldKeys = await wire.newKeys(), oldPub = await wire.publicEntry(oldKeys, "bob/old-phone"), fpOld = await wire.fingerprint(oldPub);
  const rB0 = await wire.parseRoster(A.bob.json);
  const rB1 = await wire.nextRoster(keysB, "bob/desk", rB0, [pubB, oldPub], await wire.joinConsent(oldKeys, "bob/old-phone", rB0.person, 1, await wire.rosterHash(rB0)), rB0.label, [fpB, fpOld]);
  const rB2 = await wire.nextRoster(keysB, "bob/desk", rB1, [pubB], null);
  await wire.verifyNext(rB1, rB0); await wire.verifyNext(rB2, rB1);
  const bob = { ...(await A.e.personRecord([rB0, rB1, rB2], "pinned", A.bob)), state: "pinned" };
  // Alice removes her phone too: its key stays hers.
  const rA1 = await wire.parseRoster(A.e.me.json), rA2 = await wire.nextRoster(keysA, "alice/laptop", rA1, [pubA], null);
  await wire.verifyNext(rA2, rA1);
  A.e.me = { ...(await A.e.personRecord([rA2], "self", A.e.me)), state: "self" };
  await A.store.write([{ s: "persons", k: bob.person, v: bob }, { s: "kv", k: "person", v: A.e.me }]);
  check(!bob.devices.some((d) => d.fingerprint === fpOld) && bob.known.some((d) => d.fingerprint === fpOld) && !A.e.me.devices.some((d) => d.fingerprint === fpP), "fixture: both removed devices stay known to their persons");
  const T = "6".repeat(32), L = "5".repeat(32);
  await A.store.write([{ s: "inbox", k: T, v: { id: T, v: 2, conv: convId, lid: L, from: "bob/old-phone", fp: fpOld, kind: "message", body: "typed on the old phone", at: 1, read: true } }]);
  let seq = 0;
  const control = (sub, body, fp, ref = L) => ({ v: 3, conv: convId, lid: "f" + (++seq).toString().padStart(31, "0"), replica: false, kind: "message", sub, body, ref: { id: ref, fingerprint: fp } });
  const env = () => ({ id: "d" + seq.toString().padStart(31, "0"), from: "bob/desk", to: "alice/laptop", ts: 1700000000, attn: false, chan: "" });
  const tryAdmit = async (n) => { try { return await A.e.admitControl(n, env(), pinB); } catch (err) { return err; } };
  const edit = await tryAdmit(control(wire.SubRevision, JSON.stringify({ rev: 1, text: "fixed on the desk" }), fpOld));
  check(Array.isArray(edit) && edit.some((o) => o.s === "inbox" && o.v.sub === wire.SubRevision), "an edit of the removed own device's message is admitted: " + (edit.reason || "") + " " + (edit.message || ""));
  await A.store.write(edit);
  const shown = (await A.e.dm(convId)).messages.find((m) => m.id === T);
  check(shown?.edited && shown.text === "fixed on the desk", "the edit shows on the removed device's message: " + JSON.stringify(shown && { edited: shown.edited, text: shown.text }));
  const other = await tryAdmit(control(wire.SubRevision, JSON.stringify({ rev: 1, text: "hijack" }), fpP, "4".repeat(32)));
  check(other.reason === "invalid" && heldDiagnosticCode(other.message) === "control_target_person_mismatch", "an edit of another person's removed device's message is refused with its code: " + other.reason + " " + other.message);
  const unknown = await tryAdmit(control(wire.SubRetraction, "{}", "00000000-11111111-22222222-33333333", "3".repeat(32)));
  // As client controlAuthorized: a key no member's (yet) waits for the roster
  // step that adds it, with its own code; it is never called another person's.
  check(unknown.reason === "proof_pending" && heldDiagnosticCode(unknown.message) === "control_target_unknown_key", "an unknown key waits, with its code: " + unknown.reason + " " + unknown.message);
}

if (failed) process.exit(1);
console.log("controls ok");
