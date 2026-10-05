// Browser parity for assistants' own reactions (envelope.AssistantReaction,
// protocol.CapAgentReaction): shape vectors, device-thread and DM admission,
// projection, removal, and own-linked history held for an older reader.
// Isolated real-key Engine world (as external_agent_engine_check.mjs); no
// real peer, model, browser or network. stdin: {vectors} from
// reaction_browser_test.go (envelope.TestAssistantReactionShapeVectors).
import assert from 'node:assert/strict';
import { Engine, memoryStore } from '../static/engine.mjs';
import * as wire from '../static/wire.mjs';
let checks = 0, sequence = 1;
const id = () => (sequence++).toString(16).padStart(32, '0');
const now = 1790000000123;
const check = (v, why) => { assert.ok(v, why); checks++; };
const refuses = async (fn, re) => { await assert.rejects(fn, re); checks++; };
const json = (v, status = 200) => new Response(v == null ? null : JSON.stringify(v), { status });
async function world() {
  const users = [], pubs = new Map(), profiles = new Map(), rosters = new Map(), catalogs = new Map(), blobs = new Map(), posts = [];
  let offline = false;
  const fetch = async (url, o = {}) => {
    if (offline) throw new TypeError('Failed to fetch');
    const p = new URL(url).pathname;
    if (p === '/v1/version') return json({ features: ['env2', 'env3', 'person2', 'caps', 'notify1'], realm_id: 'test-realm' });
    if (p.endsWith('/profile')) return json(profiles.get(p.slice(11, -8)));
    if (p.endsWith('/agent-catalog')) return json(catalogs.get(p.slice(11, -14)) || []);
    if (p.startsWith('/v1/persons/') && p.endsWith('/chain')) {
      const pid = p.slice(12, -6), after = Number(new URL(url).searchParams.get('after'));
      return json({ records: (rosters.get(pid) || []).filter((r) => r.seq > after), more: false });
    }
    if (/^\/v1\/agents\/[^/]+\/[^/]+$/.test(p)) return json({ public: JSON.parse(wire.marshalPublic(pubs.get(p.slice(11)))) });
    if (p === '/v1/messages') {
      const env = wire.parseEnvelope(o.body);
      await wire.verifyEnvelope(env, pubs.get(env.from).sign_key);
      check(!!o.headers['X-Agentnet-Sig'], 'existing signed transport');
      posts.push(o.body); return json({ state: 'custody' });
    }
    if (p.endsWith('/wait')) return json({ state: 'custody' });
    if (p.endsWith('/ack') || p === '/v1/caps') return json(null, 204);
    if (p === '/v1/blobs') { const b = JSON.parse(o.body); if (!blobs.has(b.id)) blobs.set(b.id, { ...b, received: 0, state: 'uploading', ct: new Uint8Array(b.size) }); return json(blobs.get(b.id)); }
    if (p.startsWith('/v1/blobs/')) {
      const b = blobs.get(p.split('/')[3]);
      if (p.endsWith('/data')) return new Response(b.ct);
      if (p.endsWith('/complete')) { b.state = 'stored'; return json(b); }
      if (o.method === 'PUT') { const bytes = new Uint8Array(o.body); b.ct.set(bytes, b.received); b.received += bytes.length; }
      return json(b);
    }
    throw new Error('unexpected synthetic path ' + p);
  };
  for (const name of ['alice', 'bob', 'charlie', 'mallory', 'dana']) { // dana: a second guest (guest audience block)
    const e = new Engine({ store: memoryStore(), now: () => now, base: 'https://synthetic.invalid', fetch });
    e.keys = await wire.newKeys(); e.address = name + '/desk'; e.pub = await wire.publicEntry(e.keys, e.address); e.fp = await wire.fingerprint(e.pub);
    e.roster = await wire.newRoster(e.keys, e.address, name); e.me = await e.personRecord([e.roster], 'self', null);
    await e.store.write([{ s: 'kv', k: 'identity', v: { keys: e.keys, address: e.address, fingerprint: e.fp } }, { s: 'kv', k: 'person', v: e.me }]);
    e.connected = true; pubs.set(e.address, e.pub); rosters.set(e.me.person, [JSON.parse(wire.rosterJSON(e.roster))]); users.push(e);
  }
  const caps = async (e, names = [wire.CapEnv2, wire.CapPerson, wire.CapControl, wire.CapAgentIdentity, wire.CapExternalParticipation]) => {
    const session = id(); profiles.set(e.address, { person: JSON.parse(wire.rosterJSON(e.roster)), live: true, sessions: [session], caps: [JSON.parse(wire.capsJSON(await wire.newCaps(e.keys, e.address, session, names)))] });
  };
  for (const e of users) { await caps(e); for (const other of users) {
    const p = await e.personRecord([other.roster], other === e ? 'self' : 'pinned', null);
    await e.store.write([{ s: 'persons', k: p.person, v: p }, { s: 'pins', k: other.address, v: { address: other.address, json: wire.marshalPublic(other.pub), fingerprint: other.fp, pending: null } }]);
  } }
  const [alice, bob, charlie, mallory, dana] = users;
  const agent = await wire.signAgent(charlie.keys, { id: id(), host: charlie.address, host_key: charlie.fp, label: 'Charlie agent', ts: Math.floor(now / 1000) });
  catalogs.set(charlie.address, [JSON.parse(wire.agentJSON(agent))]);
  const conv = await alice.newDM(bob.address), c = await alice.store.get('convs', conv);
  const receive = async (raw, e) => { const env = wire.parseEnvelope(raw); await e.admit(raw, env); return env.id; };
  const drain = async (e, predicate = () => true) => { await Promise.all(users.map(user => user.outboxPass)); for (const raw of posts) { const env = wire.parseEnvelope(raw); if (env.to === e.address && predicate(env) && !await e.store.get('inbox', env.id) && !await e.store.get('held', env.id)) await receive(raw, e); } };
  const from = async (sender, receiver, fields) => wire.seal({ v: 2, id: id(), from: sender.address, to: receiver.address, ts: Math.floor(now / 1000), conv, root: c.root, lid: id(), kind: 'message', body: '', ...fields }, sender.keys, receiver.pub);
  const decision = async (sender, pid, type = 'accept', prev) => {
    const { info } = await alice.agentConv(pid);
    return wire.signEvent(sender.keys, { conv, pid, type, prev: prev || info.invite, ts: Math.floor(now / 1000), author: sender.author() });
  };
  const accept = async (pid) => { const ev = await decision(charlie, pid); for (const e of [alice, bob, charlie]) await receive(await from(charlie, e, { sub: 'event', pid, body: wire.eventJSON(ev) }), e); return ev; };
  const sibling = async (owner, address) => {
    const e = new Engine({ store: memoryStore(), now: () => now, base: owner.base, fetch });
    e.keys = await wire.newKeys(); e.address = address; e.pub = await wire.publicEntry(e.keys, address); e.fp = await wire.fingerprint(e.pub);
    const join = await wire.joinConsent(e.keys, address, owner.me.person, owner.me.seq + 1, owner.me.hash);
    const next = await wire.nextRoster(owner.keys, owner.address, owner.roster, [...owner.me.devices.map((d) => pubs.get(d.address)), e.pub], join);
    await wire.verifyNext(next, owner.roster);
    const steps = [...await Promise.all(rosters.get(owner.me.person).map((r) => wire.parseRoster(r))), next];
    owner.me = await owner.personRecord(steps, 'self', owner.me); owner.roster = next;
    e.me = await e.personRecord(steps, 'self', null); e.roster = next;
    pubs.set(address, e.pub); rosters.set(owner.me.person, steps.map((r) => JSON.parse(wire.rosterJSON(r))));
    await owner.store.write([{ s: 'kv', k: 'person', v: owner.me }]); await owner.pinDevices(owner.me);
    await e.store.write([{ s: 'kv', k: 'person', v: e.me }, { s: 'kv', k: 'identity', v: { keys: e.keys, address, fingerprint: e.fp } },
      ...(await owner.store.all('persons')).filter((p) => p.person !== e.me.person).map((p) => ({ s: 'persons', k: p.person, v: p })),
      ...(await owner.store.all('pins')).map((p) => ({ s: 'pins', k: p.address, v: p }))]);
    await caps(e); await caps(owner); users.push(e); return e;
  };
  return { alice, bob, charlie, mallory, dana, users, conv, c, agent, posts, blobs, caps, catalogs, receive, drain, from, decision, accept, sibling, offline: (v) => { offline = v; } };
}


const { vectors, human_vectors: humanVectors } = JSON.parse(await new Promise((resolve) => { let s = ''; process.stdin.on('data', (d) => s += d).on('end', () => resolve(s)); }));
const held = async (e, rid) => (await e.store.get('held', rid))?.reason;
const reactionBody = (emoji, op = 'add', n = 1) => JSON.stringify({ emoji, op, n });
const control = (sender, receiver, fields) => wire.seal({ v: 3, id: id(), from: sender.address, to: receiver.address, ts: Math.floor(now / 1000), kind: 'message', sub: 'reaction', body: reactionBody('🎉'), ...fields }, sender.keys, receiver.pub);
const byOf = (m, emoji) => (m?.reactions || []).find((r) => r.emoji === emoji)?.by || [];

// The shared Go shape vectors, judged by the browser's checkVersion2.
const shapes = {};
for (const { name, inner } of vectors) {
  const n = { reply_to: '', status: '', conv: '', lid: '', sub: '', replica: false, origin: '', emotion: '', target: null, pid: '', fan: null, ref: null, agent_id: '', receiver_route: null, human: null,
    ...inner, root: inner.root ? JSON.stringify(inner.root) : '', attachments: inner.attachments || [] };
  try { await wire.checkVersion2(n); shapes[name] = true; } catch (e) { shapes[name] = false; }
}

// Device thread: a named executor's or the default responder's own reaction
// binds to this browser's exact request to that device key; never its host.
{
  const w = await world(), { alice: a, charlie: c, mallory: m } = w;
  const named = await a.api('/api/send', { to: c.address, kind: 'question', body: 'named question', agent_id: w.agent.id });
  const plain = await a.api('/api/send', { to: c.address, kind: 'question', body: 'default question' });
  const ok1 = await w.receive(await control(c, a, { ref: { id: named.id, fingerprint: a.fp }, agent_id: w.agent.id }), a);
  const ok2 = await w.receive(await control(c, a, { ref: { id: plain.id, fingerprint: a.fp }, origin: 'agent:claude' }), a);
  check(!!await a.store.get('inbox', ok1) && !!await a.store.get('inbox', ok2), 'named and default assistant reactions admitted');
  await w.receive(await control(c, a, { ref: { id: named.id, fingerprint: a.fp } }), a); // charlie's own human mark beside its assistant's
  const nt = await a.thread(named.id), nm = nt.messages.find((x) => x.id === named.id), by = byOf(nm, '🎉');
  const assistantKey = 'assistant:' + c.address + '/' + w.agent.id;
  check(by.length === 2 && JSON.stringify(by.find((b) => b.assistant)) === JSON.stringify({ id: assistantKey, label: c.address + ' assistant ' + w.agent.id.slice(0, 8), assistant: true, host: c.address, agent_id: w.agent.id }), 'named Reactor equals the native JSON');
  check(by.some((b) => !b.assistant && b.id === c.address && b.label === c.address), 'the host device mark stays the device own');
  const pm = (await a.thread(plain.id)).messages.find((x) => x.id === plain.id), dby = byOf(pm, '🎉');
  check(dby.length === 1 && JSON.stringify(dby[0]) === JSON.stringify({ id: 'assistant:' + c.address + '/default', label: c.address + ' assistant', assistant: true, host: c.address }), 'default responder Reactor equals the native JSON (harness not in key)');
  // Refused, never stored: a different executor, form, sender, key or request.
  for (const [what, raw] of [
    ['named on a default request', await control(c, a, { ref: { id: plain.id, fingerprint: a.fp }, agent_id: w.agent.id })],
    ['default on a named request', await control(c, a, { ref: { id: named.id, fingerprint: a.fp }, origin: 'agent:claude' })],
    ['another agent', await control(c, a, { ref: { id: named.id, fingerprint: a.fp }, agent_id: id() })],
    ['another device', await control(m, a, { ref: { id: named.id, fingerprint: a.fp }, agent_id: w.agent.id })],
    ['another sender key', await control(c, a, { ref: { id: named.id, fingerprint: c.fp }, agent_id: w.agent.id })]]) {
    const rid = await w.receive(raw, a);
    check(await held(a, rid) === 'invalid' && !await a.store.get('inbox', rid), what + ' refused');
  }
  const unrecorded = await a.api('/api/send', { to: c.address, kind: 'question', body: 'legacy row' });
  await a.outboxPass;
  const row = await a.store.get('outbox', unrecorded.id);
  delete row.fp; await a.store.write([{ s: 'outbox', k: row.id, v: row }]);
  const legacy = await w.receive(await control(c, a, { ref: { id: unrecorded.id, fingerprint: a.fp }, origin: 'agent:claude' }), a);
  check(await held(a, legacy) === 'invalid', 'a request without its recorded key proves nothing');
  // Shape refusals (checkVersion3): ids in the wrong place.
  for (const bad of [{ pid: id() }, { origin: 'agent:' }, { origin: 'agent:claude', agent_id: w.agent.id }, { origin: 'ui' }])
    await refuses(() => control(c, a, { ref: { id: named.id, fingerprint: a.fp }, ...bad }), /assistant reaction|nothing but|invalid origin/);
  // Removal by the same assistant resolves (higher counter).
  await w.receive(await control(c, a, { ref: { id: plain.id, fingerprint: a.fp }, origin: 'agent:claude', body: reactionBody('🎉', 'remove', 2) }), a);
  check(byOf((await a.thread(plain.id)).messages.find((x) => x.id === plain.id), '🎉').length === 0, 'assistant removal resolves');
}

// DM participation: the outside host's assistant reacts as itself on both
// members' copies, beside a member's own mark; a later linked device gets it
// as history only once it reads agr1 (besides apx1).
{
  const w = await world(), { alice: a, bob: b, charlie: c, mallory: m } = w;
  const selected = await a.sendDM({ conv: w.conv, body: 'context' }); await w.drain(b);
  const invite = await a.inviteAgent({ conv: w.conv, host: c.address, agent_id: w.agent.id }); await w.drain(b); await w.drain(c); await w.accept(invite.pid);
  const q = await a.askAgent({ pid: invite.pid, kind: 'question', body: 'react if you like' }); await w.drain(b); await w.drain(c);
  const bobOld = b.me.hash, phone = await w.sibling(b, 'bob/phone');
  const lid = id(), fan = [{ person: a.me.person, roster: a.me.hash }, { person: b.me.person, roster: bobOld }];
  const fields = { conv: w.conv, lid, pid: invite.pid, agent_id: w.agent.id, ref: { id: q.lid, fingerprint: a.fp }, fan };
  for (const e of [a, b]) {
    const rid = await w.receive(await control(c, e, fields), e);
    check(!!await e.store.get('inbox', rid), 'participation reaction admitted at ' + e.address);
  }
  await a.api('/api/message/react', { conv: w.conv, id: q.id, dir: 'out', emoji: '🎉' });
  const am = (await a.dm(w.conv)).messages.find((x) => x.lid === q.lid), aby = byOf(am, '🎉');
  check(aby.length === 2 && aby.some((x) => x.id === a.me.person && !x.assistant) && (am.reactions.find((r) => r.emoji === '🎉').mine === true), 'member own mark kept and mine: '+JSON.stringify({reactions:am.reactions,person:a.me.person}));
  check(JSON.stringify(aby.find((x) => x.assistant)) === JSON.stringify({ id: 'assistant:' + invite.pid, label: 'charlie assistant ' + w.agent.id.slice(0, 8), assistant: true, host: c.address, agent_id: w.agent.id, pid: invite.pid }), 'participation Reactor equals the native JSON (label: the host person, as client.assistantLabel): ' + JSON.stringify(aby));
  check(byOf((await b.dm(w.conv)).messages.find((x) => x.lid === q.lid), '🎉').some((x) => x.id === 'assistant:' + invite.pid), 'the other member sees the same assistant actor');
  const localMarks=(await a.store.all('outbox')).filter(r=>r.control&&r.conv===w.conv&&r.fp===a.fp&&r.sub===wire.SubReaction);
  check(localMarks.length>0&&localMarks.every(r=>r.person===a.me.person),'new control copies retain their author, apart from the recipient');
  // The previous P3 rows survive an upgrade. Correct their local author
  // cache, preserving exact ciphertext, and allow removing the own mark.
  await a.store.write(localMarks.map(r=>({s:'outbox',k:r.id,v:{...r,person:b.me.person}})));
  await a.load();
  for(const r of localMarks){const kept=await a.store.get('outbox',r.id);check(kept.person===a.me.person&&kept.envelope===r.envelope,'old recipient-as-author row repaired without changing ciphertext');}
  const restored=(await a.dm(w.conv)).messages.find(x=>x.lid===q.lid);
  check(restored.reactions.find(r=>r.emoji==='🎉').mine&&byOf(restored,'🎉').some(x=>x.id===a.me.person&&!x.assistant),'old own mark projects as mine after reload');
  await a.api('/api/message/react',{conv:w.conv,id:q.id,dir:'out',emoji:'🎉',remove:true});
  const removed=(await a.dm(w.conv)).messages.find(x=>x.lid===q.lid);
  check(!removed.reactions.find(r=>r.emoji==='🎉').mine&&byOf(removed,'🎉').length===1&&byOf(removed,'🎉')[0].assistant,'old own mark removable without changing the assistant mark');
  for (const [what, extra, sender] of [['another PID', { pid: id() }, c], ['another agent', { agent_id: id() }, c], ['another device', {}, m],
    ['a non-request', { ref: { id: selected.lid, fingerprint: a.fp } }, c], ['another request key', { ref: { id: q.lid, fingerprint: b.fp } }, c]]) {
    const rid = await w.receive(await control(sender, a, { ...fields, lid: id(), ...extra }), a);
    check(!!await held(a, rid) && !await a.store.get('inbox', rid), what + ' refused');
  }
  // Own-linked history: forwarded to bob's later phone, waiting while it reads no agr1.
  const copies = (await b.store.all('outbox')).filter((r) => r.to === phone.address && r.sub === 'history');
  check(copies.length === 1 && copies[0].required_cap === wire.CapExternalParticipation && wire.historyAssistantReaction(wire.parseHistory(copies[0].body)), 'forwarded as history keeping apx1');
  b.connected = true; await b.flushOutbox();
  check((await b.store.get('outbox', copies[0].id)).state === 'waiting', 'an older reader (no agr1) is not sent the assistant reaction');
  check(!w.posts.some((raw) => wire.parseEnvelope(raw).id === copies[0].id), 'nothing posted to the older reader');
  await w.caps(phone, [wire.CapEnv2, wire.CapPerson, wire.CapControl, wire.CapAgentIdentity, wire.CapExternalParticipation, wire.CapAgentReaction]);
  await b.flushOutbox();
  check(['custody', 'delivered'].includes((await b.store.get('outbox', copies[0].id)).state), 'released once agr1 is signed');
  await w.drain(phone);
  check((await phone.store.get('held', copies[0].id))?.reason === 'proof_pending', 'a missing participation proof waits, never refused');
  const dev = b.me.devices.find((d) => d.address === phone.address), bc = await b.store.get('convs', w.conv), bobRows = await b.store.all('inbox');
  for (const row of [bobRows.find((r) => r.pid === invite.pid && r.sub === 'event' && wire.parseEvent(r.body).type === 'invite'),
    bobRows.find((r) => r.pid === invite.pid && r.sub === 'event' && wire.parseEvent(r.body).type === 'accept'), bobRows.find((r) => r.lid === q.lid && !r.control)]) {
    const copy = await b.historyCopy(dev, bc, b.itemOf(row, false)); await w.receive(copy.envelope, phone);
  }
  await phone.retryHeld();
  const pm = (await phone.dm(w.conv)).messages.find((x) => x.lid === q.lid);
  check(byOf(pm, '🎉').some((x) => x.id === 'assistant:' + invite.pid && x.assistant), 'the later device shows the assistant actor from history once its proof is here');
  // Removal by the assistant resolves on the members.
  await w.receive(await control(c, a, { ...fields, lid: id(), body: reactionBody('🎉', 'remove', 2) }), a);
  check(!byOf((await a.dm(w.conv)).messages.find((x) => x.lid === q.lid), '🎉').some((x) => x.assistant), 'assistant removal resolves; member mark stays');
  check(wire.agentRequirement({ sub: 'history', body: copies[0].body }) === wire.CapAgentReaction, 'history requirement names agr1');
}

// Guest audience (envelope/human.go V3 branch; client.humanReactionRequest):
// the outside host's assistant reacts to a guest's request for that
// request's captured audience only. Shared Go vectors judged too.
if (humanVectors) {
  const ok = humanVectors.valid, norm = (inner) => ({ reply_to: '', status: '', conv: '', lid: '', sub: '', replica: false, origin: '', emotion: '', target: null, pid: '', fan: null, ref: null, agent_id: '', receiver_route: null, human: null,
    ...inner, root: inner.root ? JSON.stringify(inner.root) : '', attachments: inner.attachments || [] });
  check(wire.humanJSON(wire.parseHumanTurn(humanVectors.human)) === humanVectors.human_json, 'audience bytes equal native humanJSON');
  let valid = true; try { await wire.checkVersion2(norm({ ...ok, human: wire.parseHumanTurn(ok.human) })); } catch (e) { valid = false; }
  check(valid, 'native valid guest-audience assistant reaction accepted');
  for (const [name, inner] of Object.entries(humanVectors.invalid)) {
    let refused = false; try { await wire.checkVersion2(norm({ ...inner, human: wire.parseHumanTurn(inner.human) })); } catch (e) { refused = true; }
    check(refused, 'native refused shape refused: ' + name);
  }
}
{
  const w = await world(), { alice: a, bob: b, charlie: c, mallory: g, dana: d } = w;
  const full = [wire.CapEnv2, wire.CapPerson, wire.CapControl, wire.CapAgentIdentity, wire.CapExternalParticipation, wire.CapHumanParticipation, wire.CapAgentReaction];
  for (const e of [a, b, c, g, d]) await w.caps(e, full);
  const all = async () => { for (const e of [a, b, c, g, d]) await w.drain(e); };
  const invite = await a.inviteAgent({ conv: w.conv, host: c.address, agent_id: w.agent.id }); await all(); await w.accept(invite.pid);
  const gp = await a.changeHuman('invite', { conv: w.conv, host: g.address, note: 'PRIVATE GUEST NOTE' }); await all();
  await g.changeHuman('decide', { pid: gp.pid, accept: true }); await all(); await a.discloseHumanAudience(); await all();
  check((await g.dm(w.conv)).agents.find((x) => x.pid === invite.pid)?.can_ask === true, 'the accepted guest may address the assistant');
  const q = await g.askAgent({ pid: invite.pid, kind: 'question', body: 'guest asks the outside assistant' }); await all();
  const req = (await a.store.all('inbox')).find((r) => r.lid === q.lid && !r.control);
  const fan = [{ person: a.me.person, roster: a.me.hash }, { person: b.me.person, roster: b.me.hash }];
  const audience = { audience: req.human.audience, proof: req.human.proof }; // the request's captured audience, author the host
  const react = (to, extra = {}) => control(c, to, { conv: w.conv, lid: id(), pid: invite.pid, agent_id: w.agent.id, ref: { id: q.lid, fingerprint: g.fp }, fan, human: audience, ...extra });
  for (const e of [a, g]) check(!!await e.store.get('inbox', await w.receive(await react(e), e)), 'guest-audience reaction admitted at ' + e.address);
  const gm = (await g.dm(w.conv)).messages.find((x) => x.lid === q.lid), am = (await a.dm(w.conv)).messages.find((x) => x.lid === q.lid);
  for (const m of [gm, am]) check(byOf(m, '🎉').length === 1 && byOf(m, '🎉')[0].assistant && byOf(m, '🎉')[0].id === 'assistant:' + invite.pid, 'exact assistant actor shown, distinct from any person');
  // A guest who joined after the request: a broader audience is refused.
  const dp = await a.changeHuman('invite', { conv: w.conv, host: d.address }); await all();
  await d.changeHuman('decide', { pid: dp.pid, accept: true }); await all(); await a.discloseHumanAudience(); await all();
  const broader = await a.humanPlan(await a.store.get('convs', w.conv), '');
  check(broader.audience.length === 2, 'two guests active now');
  const forged = await w.receive(await react(a, { human: broader }), a);
  check(await held(a, forged) === 'invalid' && !await a.store.get('inbox', forged), 'an audience broader than the request is refused');
  check(await held(a, await w.receive(await react(a, { agent_id: id() }), a)) === 'invalid', 'another agent refused');
  check(await held(a, await w.receive(await control(g, a, { conv: w.conv, lid: id(), pid: invite.pid, agent_id: w.agent.id, ref: { id: q.lid, fingerprint: g.fp }, fan, human: audience }), a)) === 'invalid', 'another actor refused');
  check(await held(a, await w.receive(await react(a, { ref: { id: q.lid, fingerprint: b.fp } }), a)) === 'proof_pending', 'a request under another key is not here: pending, not stored');
  // Missing request: held as proof, admitted once the request is here.
  const q2 = await g.askAgent({ pid: invite.pid, kind: 'question', body: 'second guest question' });
  const early = await w.receive(await react(a, { ref: { id: q2.lid, fingerprint: g.fp } }), a);
  check(await held(a, early) === 'proof_pending', 'a reaction before its request waits');
  await w.drain(a); await a.retryHeld();
  check(!!await a.store.get('inbox', early), 'admitted once its request is here');
  // An ended guest is no reader any more.
  await g.changeHuman('leave', { pid: gp.pid }); await all();
  const late = await w.receive(await react(g), g);
  check(!!await held(g, late) && !await g.store.get('inbox', late), 'an ended guest takes no reaction');
  check(!JSON.stringify(audience).includes('PRIVATE GUEST NOTE') && (await d.store.all('inbox')).every((r) => !JSON.stringify(r).includes('PRIVATE GUEST NOTE')), 'no private invitation note rides the audience or reaches another guest');
  // Deleted here: the request does not come back with a later reaction.
  await a.deleteConversation({ conv: w.conv });
  await w.receive(await react(a, { lid: id(), body: reactionBody('👀') }), a);
  check(!(await a.dm(w.conv)).messages.some((x) => x.lid === q.lid), 'a deleted request stays deleted after a later assistant reaction');
}

// A linked browser's own click on a message its person's other device sent
// (an own replica, shown as yours): the exact stored row, referred to by its
// author device's key, never this browser's; ownership fences unchanged.
{
  const w = await world(), { alice: a, bob: b } = w;
  const later = await w.sibling(a, 'alice/later');
  const sent = await a.sendDM({ conv: w.conv, body: 'from the laptop' }); await w.drain(b); await w.drain(later);
  const shown = (await later.dm(w.conv)).messages.find((m) => m.body === 'from the laptop');
  check(shown?.dir === 'out' && (shown.can || []).includes('react') && !await later.store.get('outbox', shown.id), 'own replica shown as yours, reactable, kept in the inbox');
  await later.api('/api/message/react', { conv: w.conv, id: shown.id, dir: 'out', emoji: '👍' });
  const reacted = (await later.store.all('outbox')).filter((r) => r.control && r.sub === wire.SubReaction);
  check(reacted.length > 0 && reacted.every((r) => r.ref.id === sent.lid && r.ref.fingerprint === a.fp), 'the reaction refers to the author device key, never this browser');
  await w.drain(b);
  check(byOf((await b.dm(w.conv)).messages.find((m) => m.lid === sent.lid), '👍').some((x) => x.id === a.me.person && !x.assistant), 'the other member sees the person reacted');
  await later.api('/api/message/edit', { conv: w.conv, id: shown.id, dir: 'out', text: 'edited on the later device' });
  check((await later.store.all('outbox')).some((r) => r.control && r.sub === wire.SubRevision && r.ref.id === sent.lid && r.ref.fingerprint === a.fp), 'the same person edits it, by its author key');
  await refuses(() => later.api('/api/message/react', { conv: 'f'.repeat(64), id: shown.id, dir: 'out', emoji: '👍' }), /No such controllable message/);
  const bobs = await b.sendDM({ conv: w.conv, body: 'from bob' }); await w.drain(later);
  const theirs = (await later.dm(w.conv)).messages.find((m) => m.body === 'from bob');
  await refuses(() => later.api('/api/message/edit', { conv: w.conv, id: theirs.id, dir: 'out', text: 'not mine' }), /No such controllable message/);
  await refuses(() => later.api('/api/message/edit', { conv: w.conv, id: theirs.id, dir: 'in', text: 'not mine' }), /Only the sender's person/);
  check(!!bobs.id, 'another person keeps their own message');
}

// Published browser caps: agr1 (with hgp1 and clr1) is advertised.
{
  const puts = [], keys = await wire.newKeys(), pub = await wire.publicEntry(keys, 'bob/b');
  const e = new Engine({ store: memoryStore(), base: 'https://synthetic.invalid', fetch: async (url, o = {}) => {
    const path = new URL(url).pathname;
    if (path === '/v1/version') return json({ features: ['caps'], realm_id: 'test-realm' });
    if (path === '/v1/caps' && o.method === 'PUT') { puts.push(o.body); return json(null, 204); }
    throw Error('offline');
  } });
  e.keys = keys; e.address = 'bob/b'; e.session = '4'.repeat(32);
  await e.onConnect().catch(() => {});
  const rec = wire.parseCaps(puts[0]);
  await wire.verifyCaps(rec, pub.sign_key);
  check([wire.CapAgentReaction, wire.CapHumanParticipation, wire.CapConvClear].every((c) => rec.caps.includes(c)), 'the browser caps publish agr1, hgp1 and clr1');
}
console.log(JSON.stringify({ shapes, checks }));
