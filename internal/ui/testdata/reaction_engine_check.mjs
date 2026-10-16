// Browser parity for assistants' own reactions (envelope.AssistantReaction,
// protocol.CapAgentReaction): shape vectors, device-thread and DM admission,
// projection, removal, and own-linked history held for an older reader.
// Isolated real-key Engine world (as external_agent_engine_check.mjs); no
// real peer, model, browser or network. stdin: {vectors} from
// reaction_browser_test.go (envelope.TestAssistantReactionShapeVectors).
import assert from 'node:assert/strict';
import { Engine, memoryStore, chatTopicAssignments } from '../static/engine.mjs';
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
  const sibling = async (owner, address, human = false) => {
    const e = new Engine({ store: memoryStore(), now: () => now, base: owner.base, fetch });
    e.keys = await wire.newKeys(); e.address = address; e.pub = await wire.publicEntry(e.keys, address); e.fp = await wire.fingerprint(e.pub);
    const join = await wire.joinConsent(e.keys, address, owner.me.person, owner.me.seq + 1, owner.me.hash);
    const next = await wire.nextRoster(owner.keys, owner.address, owner.roster, [...owner.me.devices.map((d) => pubs.get(d.address)), e.pub], join, owner.roster.label, human ? [...await wire.rosterHumans(owner.roster), e.fp] : null);
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
  return { alice, bob, charlie, mallory, dana, users, conv, c, agent, posts, blobs, caps, profiles, catalogs, receive, drain, from, decision, accept, sibling, offline: (v) => { offline = v; } };
}


const { vectors, human_vectors: humanVectors,organization } = JSON.parse(await new Promise((resolve) => { let s = ''; process.stdin.on('data', (d) => s += d).on('end', () => resolve(s)); }));
const held = async (e, rid) => (await e.store.get('held', rid))?.reason;
const reactionBody = (emoji, op = 'add', n = 1) => JSON.stringify({ emoji, op, n });
const control = (sender, receiver, fields) => wire.seal({ v: 3, id: id(), from: sender.address, to: receiver.address, ts: Math.floor(now / 1000), kind: 'message', sub: 'reaction', body: reactionBody('🎉'), ...fields }, sender.keys, receiver.pub);
const byOf = (m, emoji) => (m?.reactions || []).find((r) => r.emoji === emoji)?.by || [];

// Social controls commit locally; only each sealed copy waits for its reader.
// The ordinary waiting turn is independent: ctl3 can be signed even when the
// same profile does not support the ordinary env2/person conversation copy.
for (const mode of ['no-sessions', 'unknown', 'old', 'offline-supported']) {
  const w = await world(), { alice: a, bob: b, mallory: m } = w;
  const phone = await w.sibling(a, 'alice/control-phone', true);
  const original = await a.sendDM({ conv: w.conv, body: 'control target ' + mode });
  await w.drain(b); await w.drain(phone); await a.outboxPass;
  a.queueOutbox = () => {}; // explicit existing flush boundaries, no background race
  const shown = (await a.dm(w.conv)).messages.find(r => r.lid === original.lid);
  const base = [wire.CapEnv2, wire.CapPerson, wire.CapAgentIdentity, wire.CapExternalParticipation];
  await w.caps(b, []);
  const ordinary = await a.sendDM({ conv: w.conv, body: 'independent waiting turn ' + mode });
  await a.flushOutbox();
  check((await a.store.all('outbox')).some(r => r.lid === ordinary.lid && r.to === b.address && r.state === 'waiting'), mode + ': independent ordinary copy waits');
  await w.caps(b, mode === 'offline-supported' ? [wire.CapControl] : base);
  const profile = w.profiles.get(b.address);
  if (mode === 'no-sessions') Object.assign(profile, { live: false, sessions: [], caps: [] });
  if (mode === 'unknown') profile.caps = [];
  if (mode === 'offline-supported') profile.live = false;
  const target = { conv: w.conv, id: shown.id, dir: 'out' };
  for (const [path, extra] of [['react', { emoji: '👍' }], ['edit', { text: 'local revision ' + mode }], ['react', { emoji: '👍', remove: true }], ['delete', {}]]) {
    const sent = await a.api('/api/message/' + path, { ...target, ...extra });
    const rows = (await a.store.all('outbox')).filter(r => r.control && r.ref?.id === original.lid);
    const latest = rows.filter(r => r.to === b.address).sort((x,y) => x.at - y.at);
    check(latest.length > 0 && rows.length === latest.length * 2, mode + ': every exact peer/own copy retained, none skipped');
    check(rows.filter(r => r.to === phone.address).every(r => r.state === 'custody'), mode + ': supported own recipient progresses');
    check(latest.every(r => r.state === (mode === 'offline-supported' ? 'custody' : 'waiting') && r.required_cap === wire.CapControl && r.recipient_fp === b.fp), mode + ': per-copy capability state and exact key retained');
    check(sent.state !== 'delivered' && sent.skipped.length === 0, mode + ': local action never claims delivered or drops a reader');
    const view = (await a.dm(w.conv)).messages.find(r => r.lid === original.lid);
    check(path === 'edit' ? view.text === extra.text : path === 'delete' ? view.deleted : byOf(view, '👍').some(r => r.id === a.me.person) === !extra.remove, mode + ': local projection updated durably');
  }
  const retained = (await a.store.all('outbox')).filter(r => r.control && r.to === b.address && r.ref?.id === original.lid);
  check(retained.length === 4, mode + ': one copy per action');
  if (mode !== 'offline-supported') {
    const reload = new Engine({ store: a.store, base: a.base, fetch: a.fetch, now: () => now }); await reload.load();
    check((await reload.dm(w.conv)).messages.find(r => r.lid === original.lid).deleted, mode + ': local projection survives engine reload');
    const wrong = await wire.newCaps(m.keys, b.address, id(), [...base, wire.CapControl]);
    w.profiles.set(b.address, { sessions: [wrong.session], caps: [JSON.parse(wire.capsJSON(wrong))] });
    await a.flushOutbox();
    check(retained.every(r => !w.posts.some(raw => wire.parseEnvelope(raw).id === r.id)), mode + ': wrong signing key cannot release copies');
    await w.caps(b, [...base, wire.CapControl]);
    const mixed = w.profiles.get(b.address), old = await wire.newCaps(b.keys, b.address, id(), base);
    mixed.sessions.push(old.session); mixed.caps.push(JSON.parse(wire.capsJSON(old)));
    await a.flushOutbox();
    check(retained.every(r => !w.posts.some(raw => wire.parseEnvelope(raw).id === r.id)), mode + ': one old open session keeps exact copies waiting');
    await w.caps(b); await a.flushOutbox(); await a.flushOutbox();
    for (const row of retained) {
      const released = await a.store.get('outbox', row.id);
      check(released.state === 'custody' && released.envelope === row.envelope && w.posts.filter(raw => wire.parseEnvelope(raw).id === row.id).length === 1, mode + ': signed support releases same ciphertext once to custody');
    }
  } else check((await a.store.get('outbox', ordinary.id)).state === 'waiting', 'supported offline controls do not wait behind the unsupported ordinary copy');
}

// Legacy device threads use the same delivery gate without relaxing decisions.
{
  const w=await world(),{alice:a,bob:b}=w;a.queueOutbox=()=>{};
  const original=await a.api('/api/send',{to:b.address,kind:'message',body:'device control target'});
  const guarded=await a.api('/api/send',{to:b.address,kind:'message',body:'device key guard target'});
  await w.caps(b,[]);
  for(const [path,extra] of [['react',{emoji:'👍'}],['edit',{text:'queued device edit'}],['delete',{}]]){
    const sent=await a.api('/api/message/'+path,{id:original.id,dir:'out',...extra}),row=await a.store.get('outbox',sent.id);
    check(sent.state==='waiting'&&row.required_cap===wire.CapControl&&row.recipient_fp===b.fp,'device '+path+' is durable and reports waiting');
    check(!w.posts.some(raw=>wire.parseEnvelope(raw).id===row.id),'device '+path+' stays off transport without signed support');
  }
  check((await a.thread(original.id)).messages.find(r=>r.id===original.id&&r.dir==='out').deleted,'device local deletion is projected before delivery');
  const retained=(await a.store.all('outbox')).filter(r=>r.control),pin=await a.store.get('pins',b.address),count=(await a.store.all('outbox')).length;
  await a.store.write([{s:'pins',k:b.address,v:{...pin,pending:{fingerprint:w.mallory.fp}}}]);
  await refuses(()=>a.api('/api/message/react',{id:guarded.id,dir:'out',emoji:'🎉'}),/key changed/);
  check((await a.store.all('outbox')).length===count,'device authority refusal queues no extra control');
  await a.store.write([{s:'pins',k:b.address,v:pin}]);await w.caps(b);
  await a.flushOutbox();await a.flushOutbox();
  for(const row of retained){const released=await a.store.get('outbox',row.id);check(released.state==='custody'&&released.envelope===row.envelope&&w.posts.filter(raw=>wire.parseEnvelope(raw).id===row.id).length===1,'device signed support releases exact sealed control once to custody');}
}

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

// Signed browser profile reads agr1/hgp1/clr1 through rm1; new caps stay explicit.
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
  const profile={sessions:[rec.session],caps:[JSON.parse(puts[0])]},basic=[wire.CapAgentReaction,wire.CapHumanParticipation,wire.CapConvClear],explicit=[wire.CapGroupHumanParticipation,wire.CapGroupInvitationManage,wire.CapReadSyncV2,wire.CapMentionReaction,wire.CapAgentOrganization];
  check(rec.caps.length<=wire.MaxAdvertisedCaps,'published record fits advertisement bound');
  for(const cap of [...basic,...explicit])check(await wire.profileSupports(profile,e.address,pub.sign_key,cap),'signed browser profile reads '+cap);
  const old=await wire.newCaps(keys,e.address,'5'.repeat(32),[wire.CapRoom]),mixed={sessions:[rec.session,old.session],caps:[...profile.caps,JSON.parse(wire.capsJSON(old))]};
  check(wire.capsReads(rec,wire.CapReadSync),'rd2 preserves the older read-sync reader');
  check(!wire.capsReads(old,wire.CapReadSync),'rm1 does not imply read synchronization');
  for(const cap of basic)check(await wire.profileSupports(mixed,e.address,pub.sign_key,cap),'rm1 preserves '+cap);
  for(const cap of explicit){
    check(rec.caps.includes(cap)&&!wire.capsReads(old,cap),'new cap stays explicit: '+cap);
    check(!await wire.profileSupports(mixed,e.address,pub.sign_key,cap),'old session blocks new cap: '+cap);
  }
}
// Ordinary canonical mentions grant attention, no inbound request. Exact
// selected context is still required for this admitted agent's signed mark.
{
 const w=await world(),{alice:a,bob:b,charlie:c,mallory:m}=w,pid=id();
 for(const e of [a,b,c])await w.caps(e,[wire.CapEnv2,wire.CapPerson,wire.CapControl,wire.CapAgentIdentity,wire.CapExternalParticipation,wire.CapMentionReaction,wire.CapAgentOrganization]);
 const original=await a.sendDM({conv:w.conv,body:'Thanks [@Agent](agentnet:agent/'+pid+') for the update.'});await w.drain(b);
 const event=await wire.signEvent(a.keys,{conv:w.conv,pid,type:'invite',ts:Math.floor(now/1000),author:a.author(),host:{person:c.me.person,address:c.address,fingerprint:c.fp,agent_id:w.agent.id},grant:[{lid:original.lid,fingerprint:a.fp}],audience:'conversation'});
 await a.sendConv(w.c,{kind:'message',sub:'event',pid,body:wire.eventJSON(event)});await w.drain(b);await w.drain(c);await w.accept(pid);
 const fields={conv:w.conv,lid:id(),pid,agent_id:w.agent.id,origin:wire.OriginMentionReaction,ref:{id:original.lid,fingerprint:a.fp}};
 for(const e of [a,b]){const rid=await w.receive(await control(c,e,fields),e);check(!!await e.store.get('inbox',rid),'ordinary mention reaction admitted at '+e.address);}
 let message=(await a.dm(w.conv)).messages.find(x=>x.lid===original.lid);
 check(byOf(message,'🎉').some(x=>x.id==='assistant:'+pid),'ordinary mention retains exact agent actor');
 check(!(await c.store.all('inbox')).some(x=>['question','task'].includes(x.kind)),'ordinary mention fabricates no inbound work');
 const phone=await w.sibling(b,'bob/mention-phone',true),dev=b.me.devices.find(d=>d.address===phone.address),bc=await b.store.get('convs',w.conv),rows=await b.store.all('inbox');
 await w.caps(phone,[wire.CapEnv2,wire.CapPerson,wire.CapControl,wire.CapRoom,wire.CapMentionReaction]);
 for(const row of [rows.find(r=>r.pid===pid&&r.sub==='event'&&wire.parseEvent(r.body).type==='invite'),rows.find(r=>r.pid===pid&&r.sub==='event'&&wire.parseEvent(r.body).type==='accept'),rows.find(r=>r.lid===original.lid&&!r.control),rows.find(r=>r.pid===pid&&r.control)]){const copy=await b.historyCopy(dev,bc,b.itemOf(row,false));await w.receive(copy.envelope,phone);}
 await phone.retryHeld();check(byOf((await phone.dm(w.conv)).messages.find(r=>r.lid===original.lid),'🎉').some(r=>r.id==='assistant:'+pid),'ordinary mention history converges under exact agent identity '+JSON.stringify((await phone.store.all('held')).map(r=>({reason:r.reason,detail_code:r.detail_code})))+' rows='+JSON.stringify((await phone.store.all('inbox')).map(r=>({sub:r.sub,pid:r.pid,origin:r.origin,control:r.control}))));
 check(!(await phone.store.all('inbox')).some(r=>['agent-waiting','task-waiting','running'].includes(r.state)),'new ordinary history starts no work');
 await w.receive(await control(c,a,{...fields,lid:id(),body:reactionBody('🎉','remove',2)}),a);
 check(byOf((await a.dm(w.conv)).messages.find(x=>x.lid===original.lid),'🎉').length===0,'ordinary mention remove resolves');
 for(const bad of [{host:m,key:a.fp},{host:c,key:m.fp}]){const rid=await w.receive(await control(bad.host,a,{...fields,lid:id(),ref:{id:original.lid,fingerprint:bad.key}}),a);check(await held(a,rid)===(bad.host===m?'invalid':'proof_pending')&&!await a.store.get('inbox',rid),'foreign host/key ordinary mark refused');}
 const plain=await a.sendDM({conv:w.conv,body:'plain @Agent label'});await w.drain(b);
 const forged=await w.receive(await control(c,a,{...fields,lid:id(),ref:{id:plain.lid,fingerprint:a.fp}}),a);check(await held(a,forged)==='invalid','plain label is neither canonical PID nor admitted selection');
 await a.dismissAgent(pid);await w.drain(b);await w.drain(c);
 const revoked=await w.receive(await control(c,a,{...fields,lid:id()}),a);check(await held(a,revoked)==='invalid','revoked agent cannot react to ordinary mention');
}

// The compact native-author proof and canonical JSON must agree with Go.
{
 const key=await wire.parsePublic(organization.public),p=organization.proof;
 await wire.verifyTopicRequestProof(p,key);check(new TextDecoder().decode(wire.topicRequestProofCanonical(p))===organization.canonical,'compact proof canonical agrees with Go');
 check(wire.topicEventJSON(wire.parseTopicEvent(organization.event))===organization.event_json,'extended topic event JSON agrees with Go');
 check(await wire.groupHistoryContentHash(organization.request.conv,organization.request)===organization.hash,'immutable private request hash agrees with Go');
 for(const changed of [{hash:'d'.repeat(64)},{pid:id()},{lid:id()},{target:{...p.target,agent_id:id()}},{author:{...p.author,address:'foreign/phone'}}])await refuses(()=>wire.verifyTopicRequestProof({...p,...changed},key),/proof|signature|author/);
 check(!wire.topicRequestJSON(organization.event.request).includes('SYNTHETIC_PRIVATE_REQUEST'),'compact event discloses no private body');
}

// Real remote human original; missing original waits and then recovers. A
// received move grants no new request, agent context or native execution.
{
 const w=await world(),{alice:a,bob:b,charlie:c,mallory:m}=w;
 const selected=await a.sendDM({conv:w.conv,body:'Related selected discussion'});await w.drain(b);
 const invite=await a.inviteAgent({conv:w.conv,host:c.address,agent_id:w.agent.id,share:[selected.id]});await w.drain(b);await w.drain(c);await w.accept(invite.pid);
 const q=await a.askAgent({pid:invite.pid,kind:'question',body:'SYNTHETIC_PRIVATE organize selected discussion'});await w.drain(b);await w.drain(c);
 const original=(await a.followupOriginal(w.conv,{id:q.lid,fingerprint:a.fp})).item,hash=await wire.groupHistoryContentHash(w.conv,original),source=await a.store.get('outbox',selected.id),topic=id();
 const fields={lid:id(),pid:invite.pid,agent_id:w.agent.id,origin:wire.OriginAgentOrganization,emotion:'neutral',topic,body:'Moved selected messages to this topic.',topic_event:{action:'move',moves:[{lid:selected.lid,author:a.fp,hash:await wire.groupHistoryContentHash(w.conv,a.itemOf(source,true))}],request:{lid:q.lid,author:a.fp,hash}}};
 b.queueOutbox=()=>{};b.caps=[wire.CapTopicOrganization,wire.CapAgentOrganization];
 const before=(await b.store.all('inbox')).filter(x=>['question','task'].includes(x.kind)).length;
 await b.followupOriginal(w.conv,{id:q.lid,fingerprint:a.fp});
 await b.agentOrganizationAdmission({...w.c,peer:a.me.person},{...fields,kind:"message",conv:w.conv,v:wire.Version2},c.address,c.fp,[]);
 const rid=await w.receive(await w.from(c,b,fields),b);check(!!await b.store.get('inbox',rid),'real foreign human proof authorizes exact agent move: '+JSON.stringify({reason:(await b.store.get('held',rid))?.reason,detail_code:(await b.store.get('held',rid))?.detail_code}));
 check(chatTopicAssignments(await b.convMessages(w.conv,await b.store.all('inbox'),await b.store.all('outbox'))).get(selected.lid)===topic,'browser projects selected original to new topic');
 check((await b.store.all('inbox')).filter(x=>['question','task'].includes(x.kind)).length===before,'move creates no inbound work');
 const phone=await w.sibling(b,'bob/organization-phone',true),dev=b.me.devices.find(d=>d.address===phone.address),bc=await b.store.get('convs',w.conv),rows=await b.store.all('inbox');
 await w.caps(phone,[wire.CapEnv2,wire.CapPerson,wire.CapControl,wire.CapRoom,wire.CapAgentOrganization,wire.CapTopicOrganization]);
 for(const row of [rows.find(r=>r.pid===invite.pid&&r.sub==='event'&&wire.parseEvent(r.body).type==='invite'),rows.find(r=>r.pid===invite.pid&&r.sub==='event'&&wire.parseEvent(r.body).type==='accept'),rows.find(r=>r.lid===selected.lid&&!r.control),rows.find(r=>r.lid===q.lid&&!r.control),rows.find(r=>r.id===rid)]){const copy=await b.historyCopy(dev,bc,b.itemOf(row,false));await w.receive(copy.envelope,phone);}
 await phone.retryHeld();check(chatTopicAssignments(await phone.convMessages(w.conv,await phone.store.all('inbox'),await phone.store.all('outbox'))).get(selected.lid)===topic,'exact agent organization converges through own-human inert history');
 check(!(await phone.store.all('inbox')).some(r=>['agent-waiting','task-waiting','running'].includes(r.state)),'organization history starts no work');
 const undo={conv:w.conv,lid:fields.lid,author:c.fp,id:id()},restored=await b.undoTopicOrganization(undo);
 check(restored.length===1,'human restores Main with one exact inverse');
 check(chatTopicAssignments(await b.convMessages(w.conv,await b.store.all('inbox'),await b.store.all('outbox'))).get(selected.lid)==='', 'inverse returns selected original to Main');
 check((await b.undoTopicOrganization(undo))[0].lid===restored[0].lid,'browser undo retry keeps same action');
 await refuses(()=>b.undoTopicOrganization({...undo,id:id()}),/moved again|changed|undo/i);
 await refuses(()=>b.previewTopicOrganization({conv:w.conv,ids:[selected.lid]}),/topic|choose|chat/i);

 for(const changed of [{topic_event:{...fields.topic_event,request:{...fields.topic_event.request,hash:'f'.repeat(64)}}},{agent_id:id()}]){const bad=await w.receive(await w.from(c,b,{...fields,...changed,lid:id()}),b);check(await held(b,bad)==='invalid','forged human binding/executor refused');}
 const missing=id(),pending={...fields,lid:id(),topic_event:{...fields.topic_event,request:{lid:missing,author:a.fp,hash:await wire.groupHistoryContentHash(w.conv,{...original,lid:missing,id:missing})}}};
 const pendingRaw=await w.from(c,b,pending),pendingID=await w.receive(pendingRaw,b);check(await held(b,pendingID)==='proof_pending','missing exact original waits explicitly');
 await w.receive(await w.from(a,b,{...original,id:missing,lid:missing,replica:true,v:wire.Version2}),b);await b.store.write([{s:'held',k:pendingID}]);await w.receive(pendingRaw,b);check(!!await b.store.get('inbox',pendingID),'actual original arrival recovers the held move');
}

// Mixed inert ciphertext uses both independent reader extensions, including
// expiry/repacking after the local classification plaintext is erased.
{
 const w=await world(),a=w.alice,phone=await w.sibling(a,'alice/archive-phone',true),dev=a.me.devices.find(d=>d.address===phone.address);
 const full=[wire.CapEnv2,wire.CapPerson,wire.CapControl,wire.CapRoom,wire.CapHistoryArchive,wire.CapTopicOrganization,wire.CapMentionReaction,wire.CapAgentOrganization];
 await w.caps(phone,full);a.queueOutbox=()=>{};a.runHistory=async()=>{};
 const refs={lid:id(),author:a.fp,hash:'a'.repeat(64),topic:id()},pid=id(),copies=[];
 for(const item of [{v:1,id:id(),lid:id(),from:a.address,from_key:a.fp,ts:1,at:1000,kind:'message',sub:wire.SubReaction,pid,origin:wire.OriginMentionReaction,ref:{id:id(),fingerprint:a.fp},body:reactionBody('👍','add',1),attachments:[]},{v:1,id:id(),lid:id(),from:a.address,from_key:a.fp,ts:1,at:1000,kind:'message',origin:'ui',body:'Restore Main',topic_event:{action:'move',moves:[refs]},attachments:[]}]){
   const body=wire.historyJSON(item),rid=id(),envelope=await wire.seal({v:2,id:rid,lid:rid,conv:w.conv,root:w.c.root,from:a.address,to:phone.address,ts:1,kind:'message',sub:'history',body,replica:true},a.keys,phone.pub);
   copies.push({id:rid,conv:w.conv,to:phone.address,recipient_fp:phone.fp,sub:'history',body,envelope,at:1000,state:'queued'});
 }
 await a.store.write(await a.archives().prepare(copies,[],[],dev));await a.archives().wake();
 let descriptors=(await a.store.all('outbox')).filter(r=>r.sub===wire.SubHistoryArchive),first=descriptors[0];
 check(descriptors.length===1&&first.mention_cap&&first.organization_cap,'mixed browser archive retains both independent requirements '+JSON.stringify((await a.store.all('outbox')).map(r=>({sub:r.sub,state:r.state,detail:r.detail,mention:r.mention_cap,organization:r.organization_cap})))+' jobs='+JSON.stringify((await a.store.prefix('kv','history-archive/out/')).map(j=>({state:j.state,error:j.error}))));
 a.connected=false;await a.dispatch('receipt',JSON.stringify({id:first.id,state:'expired',seq:1}));await a.archiveReceiptRun;await a.archiveRun;
 for(const row of copies){const current=await a.store.get('outbox',row.id);await a.store.write([{s:'outbox',k:row.id,v:{...current,body:''}}]);}
 a.connected=true;await a.archives().wake();descriptors=(await a.store.all('outbox')).filter(r=>r.sub===wire.SubHistoryArchive);const repacked=descriptors.find(r=>r.id!==first.id);
 check(!!repacked&&repacked.mention_cap&&repacked.organization_cap,'expired browser archive repacks without weakening either requirement');
 for(const missing of [wire.CapMentionReaction,wire.CapAgentOrganization]){await w.caps(phone,full.filter(c=>c!==missing));await a.postStored({...repacked,state:'queued'});const held=await a.store.get('outbox',repacked.id);check(held.state==='waiting'&&/read|update|support/i.test(held.detail),'browser archive independently waits for '+missing);}
 await w.caps(phone,full);await a.postStored({...repacked,state:'queued'});const released=await a.store.get('outbox',repacked.id);check(released.state==='custody'&&released.envelope===repacked.envelope,'browser signed support releases same sealed descriptor to custody');
}

console.log(JSON.stringify({ shapes, checks }));
