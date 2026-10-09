// Isolated Alice/Bob/Carol human Engine world; source-driven transport, no real peer/model/cloud.
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
  const users = [], pubs = new Map(), profiles = new Map(), rosters = new Map(), catalogs = new Map(), blobs = new Map(), posts = [], groups = new Map();
  let offline = false;
  const fetch = async (url, o = {}) => {
    if (offline) throw new TypeError('Failed to fetch');
    const p = new URL(url).pathname;
    if (p === '/v1/version') return json({ features: ['env2', 'person2', 'caps', 'notify1'], realm_id: 'f'.repeat(32) });
    if (p.startsWith('/v1/groups/') && p.endsWith('/chain')) { const records=groups.get(p)||[]; if(o.method==='POST'){const record=JSON.parse(o.body);records[record.seq]=record;groups.set(p,records);return json({seq:record.seq,hash:record.hash});} const after=Number(new URL(url).searchParams.get('after')||-1);return json({records:records.filter(r=>r.seq>after),more:false}); }
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
  for (const name of ['alice', 'bob', 'carol', 'mallory']) {
    const e = new Engine({ store: memoryStore(), now: () => now, base: 'https://synthetic.invalid', fetch });
    e.keys = await wire.newKeys(); e.address = name + '/desk'; e.pub = await wire.publicEntry(e.keys, e.address); e.fp = await wire.fingerprint(e.pub);
    e.roster = await wire.newRoster(e.keys, e.address, name); e.me = await e.personRecord([e.roster], 'self', null);
    await e.store.write([{ s: 'kv', k: 'identity', v: { keys: e.keys, address: e.address, fingerprint: e.fp } }, { s: 'kv', k: 'person', v: e.me }]);
    e.connected = false; pubs.set(e.address, e.pub); rosters.set(e.me.person, [JSON.parse(wire.rosterJSON(e.roster))]); users.push(e);
  }
  const caps = async (e, names = [wire.CapEnv2, wire.CapPerson, wire.CapAgentIdentity, wire.CapExternalParticipation, wire.CapHumanParticipation]) => {
    const session = id(); profiles.set(e.address, { person: JSON.parse(wire.rosterJSON(e.roster)), live: true, sessions: [session], caps: [JSON.parse(wire.capsJSON(await wire.newCaps(e.keys, e.address, session, names)))] });
  };
  for (const e of users) { await caps(e); for (const other of users) {
    const p = await e.personRecord([other.roster], other === e ? 'self' : 'pinned', null);
    await e.store.write([{ s: 'persons', k: p.person, v: p }, { s: 'pins', k: other.address, v: { address: other.address, json: wire.marshalPublic(other.pub), fingerprint: other.fp, pending: null } }]);
  } }
  const [alice, bob, carol, mallory] = users;
  const agent = await wire.signAgent(carol.keys, { id: id(), host: carol.address, host_key: carol.fp, label: 'Carol agent', ts: Math.floor(now / 1000) });
  catalogs.set(carol.address, [JSON.parse(wire.agentJSON(agent))]);
  const conv = await alice.newDM(bob.address), c = await alice.store.get('convs', conv);
  const receive = async (raw, e) => { const env = wire.parseEnvelope(raw); await e.admit(raw, env); return env.id; };
  // Sending saves first (P12); this isolated network posts the captured
  // queued copies before delivering them. No real connection/polling runs.
  const drain = async (e, predicate = () => true) => {
    for (const sender of users) {
      const rows=(await sender.store.all('outbox')).sort((a,b)=>(a.send_order??a.at)-(b.send_order??b.at));
      for(const row of rows) if(row.to===e.address && ['queued','waiting'].includes(row.state)) await sender.post(row);
    }
    for (const raw of posts) { const env = wire.parseEnvelope(raw); if (env.to === e.address && predicate(env) && !await e.store.get('inbox', env.id) && !await e.store.get('held', env.id)) await receive(raw, e); }
  };
  const from = async (sender, receiver, fields) => wire.seal({ v: 2, id: id(), from: sender.address, to: receiver.address, ts: Math.floor(now / 1000), conv, root: c.root, lid: id(), kind: 'message', body: '', ...fields }, sender.keys, receiver.pub);
  const decision = async (sender, pid, type = 'accept', prev) => {
    const { info } = await alice.agentConv(pid);
    return wire.signEvent(sender.keys, { conv, pid, type, prev: prev || info.invite, ts: Math.floor(now / 1000), author: sender.author() });
  };
  const accept = async (pid) => { const ev = await decision(carol, pid); for (const e of [alice, bob, carol]) await receive(await from(carol, e, { sub: 'event', pid, body: wire.eventJSON(ev) }), e); return ev; };
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
  return { alice, bob, carol, mallory, users, conv, c, agent, posts, blobs, caps, catalogs, receive, drain, from, decision, accept, sibling, offline: (v) => { offline = v; } };
}

// Same root, invitation without disclosure, exact acceptance, ordinary guest
// text/files/replies, both ending actors, no jobs or stale file handover.
for (const ending of ['member', 'guest']) {
  const worldState = await world(), { alice: a, bob: b, carol: c, mallory: stranger, conv } = worldState;
  const rootBefore = (await a.store.get('convs', conv)).root;
  const hidden = await a.sendDM({ conv, body: 'PRIVATE BEFORE INVITE' });
  const selected = await a.sendDM({ conv, body: 'SELECTED CONTEXT', files: [{ name: 'chosen.txt', size: 6, bytes: new TextEncoder().encode('CHOSEN') }] });
  await worldState.drain(b);
  const invited = await a.changeHuman('invite', { conv, host: c.address, share: [selected.lid] });
  await worldState.drain(b); await worldState.drain(c);
  check((await c.dm(conv)).role === 'human_guest', 'explicit scoped guest role, not member');
  check((await c.dm(conv)).guests[0].can_decide, 'exact browser guest can decide');
  check(!(await c.dm(conv)).messages.some(m => m.body === 'SELECTED CONTEXT' || m.body === 'PRIVATE BEFORE INVITE'), 'no selected text/files before consent');
  await refuses(() => c.sendDM({ conv, pid: invited.pid, body: 'BEFORE CONSENT' }), /active|audience/i);
  await c.changeHuman('decide', { pid: invited.pid, accept: true });
  await worldState.drain(a); await worldState.drain(b);
  await a.recoverHumanExcerpts(); await worldState.drain(c);
  const excerpt = (await c.dm(conv)).messages.find(m => m.excerpt_pid === invited.pid);
  check(excerpt?.body === 'SELECTED CONTEXT', 'only selected context delivered after acceptance');
  check(new TextDecoder().decode((await c.openFile(excerpt.id, 0, 'in')).bytes) === 'CHOSEN', 'exact selected file bytes');
  check(!(await c.dm(conv)).messages.some(m => m.id === hidden.id || m.body === 'PRIVATE BEFORE INVITE'), 'unselected history absent');
  check((await a.dm(conv)).agents.length === 0 && (await c.dm(conv)).agents.length === 0, 'human never displayed as agent');
  await refuses(() => a.askAgent({ pid: invited.pid, body: 'EXECUTE' }), /Human|ordinary/i);
  await refuses(() => a.dismissAgent(invited.pid), /human participation/i);
  await refuses(() => c.changeHuman('decide', { pid: invited.pid, accept: true }), /once|host/i);
  await refuses(() => a.changeHuman('invite', { conv, host: c.address }), /already|limit/i);
  await refuses(() => stranger.changeHuman('decide', { pid: invited.pid, accept: true }), /No such/i);
  const message = await a.sendDM({ conv, body: 'HUMAN AUDIENCE', files: [{ name: 'during.txt', size: 6, bytes: new TextEncoder().encode('DURING') }] });
  await worldState.drain(b); await worldState.drain(c);
  const firstCopy = (await a.store.all('outbox')).find(m => m.lid === message.lid && m.to === c.address);
  const duplicateID = id(), opened = await wire.open(firstCopy.envelope, c.keys, c.address, a.pub);
  await worldState.receive(await wire.seal({ ...opened, id: duplicateID }, a.keys, c.pub), c);
  check(!await c.store.get('held', duplicateID), 'second physical copy with the same signed logical content is valid');
  check((await c.dm(conv)).messages.filter(m => m.body === 'HUMAN AUDIENCE').length === 1, 'one logical row for multiple encrypted copies');
  const received = (await c.dm(conv)).messages.find(m => m.body === 'HUMAN AUDIENCE');
  check(new TextDecoder().decode((await c.openFile(received.id, 0, 'in')).bytes) === 'DURING', 'ordinary guest file access');
  await c.sendDM({ conv, pid: invited.pid, reply_to: received.id, body: 'CAROL REPLY', files: [{ name: 'reply.txt', size: 5, bytes: new TextEncoder().encode('REPLY') }] });
  await worldState.drain(a); await worldState.drain(b);
  const replies = (await a.dm(conv)).messages.filter(m => m.body === 'CAROL REPLY');
  check(replies.length === 1 && replies[0].from === c.address && replies[0].kind === 'message', 'exact human provenance, ordinary kind');
  check(new TextDecoder().decode((await a.openFile(replies[0].id, 0, 'in')).bytes) === 'REPLY', 'guest-authored bytes');
  check(replies[0].reply_to === (await a.dm(conv)).messages.find(m => m.lid === message.lid).id && (await c.store.all('outbox')).filter(r => r.body === 'CAROL REPLY').every(r => r.reply_to === message.lid), 'guest reply names the parent LID on every copy, never its own copy id');
  const elsewhere = await a.sendDM({ conv: await a.newDM(stranger.address), body: 'OTHER CONVERSATION' });
  const carolCopy = (await c.store.all('outbox')).find(r => r.body === 'CAROL REPLY' && r.to === a.address);
  const wrong = await worldState.receive(await worldState.from(c, a, { body: 'WRONG PARENT', human: carolCopy.human, pid: invited.pid, reply_to: elsewhere.lid }), a);
  check((await a.store.get('held', wrong))?.reason === 'invalid', 'a parent LID from another conversation is held');
  await refuses(() => c.sendDM({ conv, pid: invited.pid, reply_to: 'f'.repeat(32), body: 'UNKNOWN PARENT' }), /stays within its conversation/);
  const genuine = (await a.store.all('outbox')).find(r => r.lid === message.lid && r.to === c.address);
  const forged = await worldState.receive(await worldState.from(stranger, a, { body: 'FORGED HUMAN AUTHOR', human: genuine.human, pid: genuine.pid }), a);
  check((await a.store.get('held', forged))?.reason === 'invalid', 'uninvited sender cannot reuse a captured human audience');
  const broken = structuredClone(genuine.human); broken.proof[1].sig[0] ^= 1;
  const tampered = await worldState.receive(await worldState.from(a, b, { body: 'BAD ACCEPT SIGNATURE', human: broken, pid: '' }), b);
  check((await b.store.get('held', tampered))?.reason === 'invalid', 'root signer cannot forge host acceptance');
  check(!(await b.dm(conv)).messages.some(m => m.body === 'BAD ACCEPT SIGNATURE'), 'bad proof never shown/delivered');
  const copies = (await a.store.all('outbox')).filter(r => r.lid === message.lid);
  check(new Set(await Promise.all(copies.map(r => wire.groupHistoryContentHash(conv, r)))).size === 1, 'same logical author/audience/hash in all physical copies');
  check(copies.every(r => r.fan.length === 2 && r.pid === '' && r.human.audience[0].pid === invited.pid), 'Fan unchanged, member author PID unchanged');
  const queued = copies.find(r => r.to === c.address); // requeue its already encrypted copy as an isolated retry probe
  queued.state = 'queued'; queued.files[0].uploaded = false; queued.files[0].ct = new Uint8Array([1]);
  await a.store.write([{ s: 'outbox', k: queued.id, v: queued }]);
  const ender = ending === 'member' ? a : c;
  await ender.changeHuman('end', { pid: invited.pid });
  await worldState.drain(a); await worldState.drain(b); await worldState.drain(c);
  check((await a.dm(conv)).guests[0].state === 'dismissed' && !(await c.dm(conv)).guests[0].can_send, 'member end or exact guest leave dominates');
  const count = worldState.posts.length, blobs = worldState.blobs.size;
  await a.post(queued);
  check(worldState.posts.length === count && worldState.blobs.size === blobs && !queued.files[0].uploaded, 'ended queued copy refused BEFORE file upload');
  await refuses(() => c.sendDM({ conv, pid: invited.pid, body: 'AFTER END' }), /active|captured/i);
  await a.sendDM({ conv, body: 'PRIVATE AFTER END', files: [{ name: 'private.txt', size: 7, bytes: new TextEncoder().encode('PRIVATE') }] });
  await worldState.drain(b); await worldState.drain(c);
  check((await b.dm(conv)).messages.some(m => m.body === 'PRIVATE AFTER END'), 'original private flow returns');
  check(!(await c.dm(conv)).messages.some(m => m.body === 'PRIVATE AFTER END'), 'ended guest excluded from new text and file grants');
  check([a, b, c].every(e => e !== undefined) && (await a.store.get('convs', conv)).root === rootBefore && (await c.store.get('convs', conv)).root === rootBefore, 'same conversation ID/root on all three devices');
  const bobPhone = await worldState.sibling(b, 'bob/phone');
  const historySource = (await b.store.all('inbox')).find(m => m.body === 'CAROL REPLY');
  const historical = b.itemOf(historySource, false), historyCopy = await b.historyCopy(b.me.devices.find(d => d.address === bobPhone.address), await b.store.get('convs', conv), historical);
  check(wire.humanJSON(wire.parseHistory(historyCopy.body).human) === wire.humanJSON(historySource.human), 'history keeps captured descriptor after ending, without rebuilding');
  await worldState.receive(historyCopy.envelope, bobPhone);
  const imported = (await bobPhone.store.all('inbox')).find(m => m.body === 'CAROL REPLY');
  check(imported?.history && imported.kind === 'message' && wire.humanJSON(imported.human) === wire.humanJSON(historySource.human), 'original member linked history retains inert guest provenance');
  const carolPhone = await worldState.sibling(c, 'carol/phone');
  await refuses(async () => c.historyCopy(c.me.devices.find(d => d.address === carolPhone.address), await c.store.get('convs', conv), c.itemOf((await c.store.all('inbox')).find(m => m.body === 'HUMAN AUDIENCE'), false)), /outside host|room history/i);
  const next = await a.changeHuman('invite', { conv, host: c.address });
  check(next.pid !== invited.pid && next.state === 'invited', 'fresh consent for reinvitation, never revive old PID');
}
// Older sessions keep the exact invitation locally until signed readers update.
{
  const w = await world(); await w.caps(w.carol, [wire.CapEnv2, wire.CapPerson, wire.CapAgentIdentity, wire.CapExternalParticipation]);
  const invited = await w.alice.changeHuman('invite', {conv:w.conv,host:w.carol.address});
  const waiting=(await w.alice.store.all('outbox')).filter(m=>m.pid===invited.pid&&m.to===w.carol.address);
  check(waiting.length>0&&waiting.every(m=>m.state==='waiting'&&m.required_cap===wire.CapHumanParticipation),'exact unsupported guest invitation waits locally, never falls back');
}
// End wins over delayed acceptance; selected context remains undisclosed.
{
  const w = await world(), invited = await w.alice.changeHuman('invite', { conv: w.conv, host: w.carol.address });
  await w.drain(w.carol); await w.drain(w.bob);
  await w.alice.changeHuman('end', { pid: invited.pid });
  await w.carol.changeHuman('decide', { pid: invited.pid, accept: true });
  await w.drain(w.alice); await w.drain(w.bob); await w.drain(w.carol);
  check((await w.alice.agentConv(invited.pid)).info.state === 'dismissed', 'end dominates delayed valid acceptance');
  await refuses(() => w.carol.sendDM({ conv: w.conv, pid: invited.pid, body: 'DELAYED ACCEPT MUST NOT REVIVE' }), /active|captured/i);
}
// A locally committed end during seal/signature work invalidates the captured
// read set before any new ordinary copy is committed.
{
  const w = await world(), a = w.alice, invite = await a.changeHuman('invite', { conv: w.conv, host: w.carol.address });
  await w.drain(w.carol); await w.drain(w.bob); await w.carol.changeHuman('decide', { pid: invite.pid, accept: true }); await w.drain(a); await w.drain(w.bob);
  // Inject while verifying the captured audience, before the serialized batch
  // commit. Starting another local send inside store.write waits on itself.
  const profile = a.profile.bind(a); let inserted = false;
  a.profile = async address => { if (!inserted) { inserted = true; await a.changeHuman('end', { pid: invite.pid }); } return profile(address); };
  await refuses(() => a.sendDM({ conv: w.conv, body: 'RACING LOCAL END', files: [{ name: 'race.txt', size: 4, bytes: new TextEncoder().encode('RACE') }] }), /ended or is held/i);
  check(inserted && !(await a.store.all('outbox')).some(m => m.body === 'RACING LOCAL END'), 'no captured ordinary copy after authority read-set race');
}
// Other accepted senders learn only the signed end; their previously captured
// turns stop unchanged, and subsequent text/files omit the ended host.
for (const ending of ['original', 'self']) {
  const w = await world(), a = w.alice, b = w.bob, c = w.carol, d = w.mallory;
  const cp = await a.changeHuman('invite', {conv:w.conv,host:c.address}); await w.drain(c); await w.drain(b); await c.changeHuman('decide',{pid:cp.pid,accept:true}); await w.drain(a); await w.drain(b);
  const dp = await a.changeHuman('invite', {conv:w.conv,host:d.address}); await w.drain(d); await w.drain(b); await d.changeHuman('decide',{pid:dp.pid,accept:true}); await w.drain(a); await w.drain(b);
  await a.sendDM({conv:w.conv,body:'SHARED TWO-GUEST TURN'}); await w.drain(c); await w.drain(d); await w.drain(b);
  const subject=(await a.agentConv(dp.pid)).info, acceptance=(await a.convEvents(w.conv)).find(x=>x.hash===subject.decision).e;
  const direct=await w.receive(await w.from(d,c,{pid:dp.pid,sub:'event',body:wire.eventJSON(acceptance)}),c);
  check((await c.store.get('held',direct))?.reason==='invalid',ending+': a guest never passes its own record to another guest; only originals share');
  const unknown=await wire.signEvent(a.keys,{conv:w.conv,pid:id(),type:'dismiss',prev:'0'.repeat(64),ts:Math.floor(now/1000),author:a.author()}), missing=await w.receive(await w.from(a,c,{pid:unknown.pid,sub:'event',body:wire.eventJSON(unknown)}),c);
  check((await c.store.get('held',missing))?.reason==='proof_pending',ending+': unknown end subject remains held, not invented');
  const transport = c.fetch; c.fetch = async (url, options) => { if (/^\/v1\/(messages|blobs)(\/|$)/.test(new URL(url).pathname)) throw new TypeError('Delivery offline'); return transport(url, options); };
  const queued = await c.sendDM({conv:w.conv,pid:cp.pid,body:'CAPTURED BEFORE OTHER END',files:[{name:'queued.txt',size:4,bytes:new TextEncoder().encode('HELD')}]}); c.fetch = transport;
  const old = (await c.store.all('outbox')).find(r=>r.lid===queued.lid&&r.to===d.address), captured=wire.humanJSON(old.human), encrypted=old.envelope;
  const ender = ending === 'original' ? a : d; await ender.changeHuman('end',{pid:dp.pid}); await w.drain(c); await w.drain(a); await w.drain(b); await w.drain(d);
  check((await c.dm(w.conv)).guests.find(g=>g.pid===dp.pid)?.state==='dismissed',ending+': remaining guest observes signed end');
  const before = w.blobs.size; await c.post(old); const stopped=await c.store.get('outbox',old.id);
  check(stopped.state==='failed'&&/active|ended|captured/i.test(stopped.detail),ending+': remaining sender queued retry refused');
  check(stopped.envelope===encrypted&&wire.humanJSON(stopped.human)===captured,ending+': captured tuple/encrypted copy not rebuilt');
  check(w.blobs.size===before,ending+': refusal happens before queued file upload');
  const next=await c.sendDM({conv:w.conv,pid:cp.pid,body:'REMAINING GUEST AFTER END',files:[{name:'after.txt',size:5,bytes:new TextEncoder().encode('AFTER')}]});
  const copies=(await c.store.all('outbox')).filter(r=>r.lid===next.lid);
  check(copies.every(r=>r.to!==d.address),ending+': remaining text/file has zero ended-recipient copy');
  check(copies.every(r=>r.human.author_pid===cp.pid&&r.human.audience.length===1),ending+': fresh snapshot keeps exact live author, excludes ended scope');
  await w.drain(a); await w.drain(b);
  check((await a.store.all('inbox')).some(r=>r.body==='REMAINING GUEST AFTER END'&&r.attachments?.length),ending+': original receives remaining guest text/file');
  const ends=(await ender.store.all('outbox')).filter(r=>r.pid===dp.pid&&r.sub==='event'&&wire.parseEvent(r.body).type==='dismiss').map(r=>({id:r.id,to:r.to,envelope:r.envelope}));
  const phone=await w.sibling(b,'bob/phone'); await ender.changeHuman('end',{pid:dp.pid});
  check(JSON.stringify((await ender.store.all('outbox')).filter(r=>r.pid===dp.pid&&r.sub==='event'&&wire.parseEvent(r.body).type==='dismiss').map(r=>({id:r.id,to:r.to,envelope:r.envelope})))===JSON.stringify(ends),ending+': repeated end reuses exact encrypted recipients, not newly linked '+phone.address);
}
// Accepted guests learn each other from originals before anyone speaks:
// pending invitations stay undisclosed, a lone shared invitation is inert,
// reordered records wait, end reaches every guest that may have learned the
// acceptance (delivery unconfirmed included), a queued acceptance never goes
// after a local end, and replayed acceptance cannot undo it.
{
  const w = await world(), a = w.alice, b = w.bob, c = w.carol, d = w.mallory, conv = w.conv;
  const settle = async () => { await a.discloseHumanAudience(); await b.discloseHumanAudience(); };
  const sharedBy = async (e, to, pid, type) => (await e.store.all('outbox')).filter(r => r.forwarded && r.to === to.address && r.pid === pid && (!type || wire.parseEvent(r.body).type === type));
  const cp = await a.changeHuman('invite', { conv, host: c.address }); await w.drain(c); await w.drain(b);
  await c.changeHuman('decide', { pid: cp.pid, accept: true }); await w.drain(a); await w.drain(b);
  const dp = await a.changeHuman('invite', { conv, host: d.address }); await w.drain(d); await w.drain(b); await settle(); await w.drain(c);
  const pending = await d.dm(conv);
  check(JSON.stringify(pending.members.map(p => p.person).sort()) === JSON.stringify([a.me.person, b.me.person].sort()), 'pending guest sees both verified original people before consenting');
  check(pending.messages.find(m => m.pid === dp.pid && m.event)?.event === 'alice invited you into this DM.', 'human invitation wording names the inviter and the guest');
  for (const e of [a, b, c, d]) { e.members = { current: true, list: w.users.map(u => ({ address: u.address, presence: 'online' })) }; e.typing.connected = true; }
  await refuses(() => d.typingTargets({ conv }), /typing requires/);
  check(!(await c.dm(conv)).guests.some(g => g.pid === dp.pid) && !(await sharedBy(a, c, dp.pid)).length && !(await sharedBy(b, c, dp.pid)).length, 'pending invitation is never shared with another guest');
  await d.changeHuman('decide', { pid: dp.pid, accept: true }); await w.drain(a); await w.drain(b); await settle();
  const shared = await sharedBy(a, c, dp.pid);
  check(JSON.stringify(shared.map(r => wire.parseEvent(r.body).type).sort()) === '["accept","scope"]' && shared.every(r => r.recipient_fp === c.fp && r.required_cap === wire.CapHumanParticipation && !r.body.includes('"note"') && !r.body.includes('"grant"')), 'original shares exactly the counted public scope+accept (never the invitation), to the exact accepted guest key');
  const inv = shared.find(r => wire.parseEvent(r.body).type === 'scope'), acc = shared.find(r => wire.parseEvent(r.body).type === 'accept');
  await w.receive(inv.envelope, c);
  check(!(await c.dm(conv)).guests.some(g => g.pid === dp.pid) && !(await c.dm(conv)).messages.some(m => m.pid === dp.pid), 'lone shared scope stays inert proof: no participant or timeline line');
  await w.receive(acc.envelope, c);
  check((await c.dm(conv)).guests.find(g => g.pid === dp.pid)?.state === 'active', 'shared acceptance makes the other guest known');
  const toD = await sharedBy(a, d, cp.pid), early = await w.receive(toD.find(r => wire.parseEvent(r.body).type === 'accept').envelope, d);
  check((await d.store.get('held', early))?.reason === 'proof_pending', 'reordered acceptance waits for its invitation');
  await w.receive(toD.find(r => wire.parseEvent(r.body).type === 'scope').envelope, d); await d.retryHeld();
  check((await d.dm(conv)).guests.find(g => g.pid === cp.pid)?.state === 'active', 'new guest learns the existing guest, order-independent');
  check(!(await c.store.all('inbox')).some(r => r.sub === 'event' && r.pid === dp.pid && wire.parseEvent(r.body).type === 'invite') && !(await d.store.all('inbox')).some(r => r.sub === 'event' && r.pid === cp.pid && wire.parseEvent(r.body).type === 'invite'), 'no guest holds another guest\'s private invitation');
  await w.drain(c); await w.drain(d);
  const targets = async (e) => (await e.typingTargets({ conv })).map(k => k.pub.address).sort();
  check(JSON.stringify(await targets(c)) === JSON.stringify([a.address, b.address, d.address].sort()), 'accepted guest types to both originals and the other accepted guest');
  check(JSON.stringify(await targets(a)) === JSON.stringify([b.address, c.address, d.address].sort()), 'member types to the other original and each accepted guest key');
  c.connected = true; c.typing.seen.set('probe', { ts: now, active: true, expires: Date.now() + 60000, fp: d.fp, scope: { conv }, entry: { address: d.address, label: 'mallory' } });
  check((await c.typingRows({ conv })).some(v => v.entry.address === d.address), 'accepted guest typing is shown to another accepted guest');
  check((await c.dm(conv)).messages.filter(m => m.event === 'mallory joined this DM.').length === 1 && (await c.store.all('inbox')).filter(r => r.sub === 'event' && r.pid === dp.pid && wire.parseEvent(r.body).type === 'accept').length >= 2, 'acceptance shown once although each original shared it');
  const before = (await a.store.all('outbox')).length + (await b.store.all('outbox')).length; await settle(); await settle();
  check((await a.store.all('outbox')).length + (await b.store.all('outbox')).length === before, 'sharing is idempotent across passes and restarts (stored copies)');
  const first = await d.sendDM({ conv, pid: dp.pid, body: 'GUEST FIRST', files: [{ name: 'first.txt', size: 5, bytes: new TextEncoder().encode('FIRST') }] });
  check((await d.store.all('outbox')).some(r => r.lid === first.lid && r.to === c.address), 'guest turn captures the other accepted guest before any original turn');
  await w.drain(c);
  const got = (await c.dm(conv)).messages.find(m => m.body === 'GUEST FIRST');
  check(got && new TextDecoder().decode((await c.openFile(got.id, 0, 'in')).bytes) === 'FIRST', 'other guest receives text and exact file bytes');
  await c.sendDM({ conv, pid: cp.pid, reply_to: got.id, body: 'GUEST REPLY' }); await w.drain(d);
  check((await d.dm(conv)).messages.find(m => m.body === 'GUEST REPLY')?.reply_to === (await d.dm(conv)).messages.find(m=>m.lid===first.lid).id, 'guest-to-guest reply names the parent LID');
  const bAcc = (await sharedBy(b, c, dp.pid, 'accept'))[0], aAcc = (await sharedBy(a, c, dp.pid, 'accept'))[0];
  await b.store.write([{ s: 'outbox', k: bAcc.id, v: { ...bAcc, state: 'queued' } }]); // a lost ACK: delivery unconfirmed
  await a.store.write([{ s: 'outbox', k: aAcc.id, v: { ...aAcc, state: 'queued' } }]);
  await d.changeHuman('end', { pid: dp.pid }); await w.drain(a); await w.drain(b); await settle();
  const posted = w.posts.length; await a.post({ ...aAcc, state: 'queued' });
  check(w.posts.length === posted, 'a queued acceptance never goes after a local end');
  check((await sharedBy(b, c, dp.pid, 'dismiss')).length === 1, 'end goes to a guest whose acceptance copy delivery is unconfirmed');
  await w.drain(c);
  check((await c.dm(conv)).guests.find(g => g.pid === dp.pid)?.state === 'dismissed', 'remaining guest learns the end');
  check(!(await c.typingRows({ conv })).some(v => v.entry.address === d.address) && !(await targets(c)).includes(d.address) && !(await targets(a)).includes(d.address), 'ended guest typing drops at view time and gets no targets');
  check((await c.dm(conv)).messages.some(m => m.event === 'mallory left this DM.'), 'self-leave wording');
  c.connected = false;
  await w.receive(await w.from(a, c, { pid: dp.pid, sub: 'event', body: acc.body }), c); await c.retryHeld();
  check((await c.dm(conv)).guests.find(g => g.pid === dp.pid)?.state === 'dismissed', 'replayed acceptance cannot undo the end');
  const after = await c.sendDM({ conv, pid: cp.pid, body: 'AFTER OTHER LEFT' });
  check((await c.store.all('outbox')).filter(r => r.lid === after.lid).every(r => r.to !== d.address), 'later guest turn excludes the departed guest');
}
// P3 review: a pushed receipt during the profile read is delivery progress,
// not a change to the captured human authority.
{
  const w = await world(), {alice:a,bob:b,carol:c,conv} = w;
  const invited = await a.changeHuman('invite',{conv,host:c.address}); await w.accept(invited.pid);
  await a.sendDM({conv,body:'FIRST RACE TURN'});
  const first = (await a.store.all('outbox')).find(r=>r.body==='FIRST RACE TURN'&&r.to===c.address);
  const profile = a.profile.bind(a); let fired = false;
  a.profile = async address => {
    if (!fired) { fired=true; await a.dispatch('receipt',JSON.stringify({id:first.id,state:'delivered',seq:1})); }
    return profile(address);
  };
  await a.sendDM({conv,body:'SECOND RACE TURN'});
  check(fired && (await a.store.get('outbox',first.id)).state==='delivered','receipt injected during profile read');
  check((await a.store.all('outbox')).filter(r=>r.body==='SECOND RACE TURN').length===2,'exactly one human turn commits and sends');
  // An actual dependency change still causes bounded pre-commit rechecking.
  let changed = false;
  a.profile = async address => {
    if (!changed) {
      changed=true;const row=await a.store.get('outbox',first.id);
      await a.store.write([{s:'outbox',k:first.id,v:{...row,authority_probe:true}}]);
    }
    return profile(address);
  };
  await a.sendDM({conv,body:'RECHECKED TURN'});
  check(changed && (await a.store.all('outbox')).filter(r=>r.body==='RECHECKED TURN').length===2,'authority conflict retries before storing, never duplicates');
}
// Plain-DM quotes are logical on every device, even when each copy id differs.
{
  const w=await world(),{alice:a,bob:b,conv}=w;
  const ap=await w.sibling(a,'alice/phone'),bp=await w.sibling(b,'bob/phone');
  const original=await a.sendDM({conv,body:'MULTIDEVICE PARENT'});
  for(const e of [b,ap,bp])await w.drain(e);
  const parent=(await b.dm(conv)).messages.find(m=>m.lid===original.lid);
  const quoted=await b.sendDM({conv,body:'MULTIDEVICE QUOTE',quote:parent.id});
  for(const e of [a,ap,bp]){
    await w.drain(e);const view=await e.dm(conv),q=view.messages.find(m=>m.lid===quoted.lid),p=view.messages.find(m=>m.lid===original.lid);
    check(q&&p&&q.quote===p.id,'quote resolves on '+e.address);
  }
}
// Only the exact invited host's connectivity matters. Member phones catch up.
{
  const w=await world(),{alice:a,bob:b,carol:c,conv}=w;
  const phone=await w.sibling(a,'alice/phone');
  const profile=a.profile.bind(a);
  a.profile=async address=>({...await profile(address),live:address!==phone.address});
  const v=await a.checkHuman({conv,host:c.address});
  check(v.ready&&v.offline.length===0&&!v.text.includes('reconnect'),'offline member device does not delay a live guest');
  await w.caps(phone,[wire.CapEnv2,wire.CapPerson]);
  const own=await a.checkHuman({conv,host:c.address});
  check(own.needs_update.some(p=>p.role==='me'&&p.me)&&own.text.includes('your other devices')&&!own.text.includes('before joining'),'own-device wording does not invite self');
  await w.caps(phone);await w.caps(b,[wire.CapEnv2,wire.CapPerson]);
  const member=await a.checkHuman({conv,host:c.address});
  check(member.needs_update.some(p=>p.role==='member')&&member.text.includes('keep this chat working with a guest')&&!member.text.includes('before joining'),'existing-member wording');
  await w.caps(b);await w.caps(c,[wire.CapEnv2,wire.CapPerson]);
  const guest=await a.checkHuman({conv,host:c.address});
  check(guest.needs_update.some(p=>p.role==='guest')&&guest.text.includes('before joining'),'guest wording');
}
// MEL-545: the browser's badge and read action use the same displayed rows,
// including guest turns, while hidden participation copies stay uncounted.
{
  const w = await world(), { alice: a, bob: b, carol: c, conv } = w;
  const invited = await a.changeHuman('invite', { conv, host: c.address });
  await w.drain(b); await w.drain(c);
  await c.changeHuman('decide', { pid: invited.pid, accept: true });
  await w.drain(a); await w.drain(b);
  await c.sendDM({ conv, pid: invited.pid, body: 'UNREAD GUEST TURN' });
  await w.drain(a); await w.drain(b);
  await b.sendDM({ conv, body: 'OWN LATEST REPLY' });
  await w.drain(a); await w.drain(c);
  for (const e of [a, b, c]) {
    const thread = await e.dm(conv);
    check(thread.messages.some(m => m.body === 'UNREAD GUEST TURN'), 'guest turn appears for each participant');
    check(thread.messages.filter(m => m.dir === 'out').every(m => !m.unread), 'own messages never unread');
    check((await e.overview()).dms.find(d => d.id === conv).unread > 0, 'unseen rows still count');
    await e.markRead(thread.messages.filter(m => m.unread).map(m => m.id));
    check((await e.overview()).dms.find(d => d.id === conv).unread === 0, 'read guest chat has no badge');
  }
  await c.sendDM({ conv, pid: invited.pid, body: 'NEXT UNSEEN GUEST TURN' });
  await w.drain(a); await w.drain(b);
  for (const e of [a, b]) check((await e.overview()).dms.find(d => d.id === conv).unread === 1, 'later unseen guest turn adds one unread');
  check((await c.overview()).dms.find(d => d.id === conv).unread === 0, 'own later guest turn stays read');
}
// MEL-521: an approved own phone, accepted as a human guest, sends the
// executable question/task to its own assistant host rather than a replica.
{
 const w=await world(),{alice:a,bob:b,carol:c,conv}=w;
 const phone=await w.sibling(c,'carol/phone');
 const guest=await a.changeHuman('invite',{conv,host:phone.address});await w.drain(phone);await w.drain(b);
 await phone.changeHuman('decide',{pid:guest.pid,accept:true});await w.drain(a);await w.drain(b);
 const assistant=await a.inviteAgent({conv,host:c.address,agent_id:w.agent.id});
 for(const e of [b,c,phone])await w.drain(e);
 const invitation=(await a.store.all('outbox')).find(r=>r.pid===assistant.pid&&r.sub==='event'&&wire.parseEvent(r.body).type==='invite');
 const scope=await wire.signEvent(a.keys,await wire.scopeOf(wire.parseEvent(invitation.body),Math.floor(now/1000)));
 await w.receive(await w.from(a,phone,{sub:'event',pid:assistant.pid,body:wire.eventJSON(scope)}),phone);
 const acceptance=await w.accept(assistant.pid);
 await w.receive(await w.from(a,phone,{sub:'event',pid:assistant.pid,body:wire.eventJSON(acceptance)}),phone);
 for(const kind of ['question','task']) {
  const sent=await phone.askAgent({pid:assistant.pid,kind,body:'Exact own guest '+kind});
  const task=(await phone.store.all('outbox')).find(r=>r.lid===sent.lid&&r.to===c.address);
  check(task&&task.replica===false&&task.target.fingerprint===c.fp,'own human guest '+kind+' has executable exact-host copy');
  const n=await wire.open(task.envelope,c.keys,c.address,phone.pub);
  check(!n.replica&&n.human.author_pid===guest.pid&&n.pid===assistant.pid,'sealed own guest '+kind+' retains separate human author and assistant target');
 }
}

// Public group guest APIs, real signed envelopes, chosen old context only.
{
 const w=await world(), {alice:a,bob:b,carol:g,mallory:other}=w;
 for(const e of w.users){e.realm='f'.repeat(32);await w.caps(e,[wire.CapEnv2,wire.CapPerson,wire.CapAgentIdentity,wire.CapExternalParticipation,wire.CapHumanParticipation,wire.CapGroupHumanParticipation,wire.CapGroupInvitationControl,wire.CapGroup,wire.CapRoom]);}
 const conv=await a.createGroup('Guest scope');
 const invitation=await a.inviteGroup({conv,person:b.me.person});
 await w.drain(b);await b.decideGroup({id:invitation.id,accept:true});await w.drain(a);
 if(a.recoveringGroups)await a.recoveringGroups;
 await a.recoverGroupIntents();await w.drain(b);await b.retryHeld();

 check(!(await b.groupThread(conv)).members.find(m=>m.person===b.me.person).admin,'ordinary member fixture');
 // Old rm1 target/member sessions must fail before any guest invite is saved.
 const names=[wire.CapEnv2,wire.CapPerson,wire.CapAgentIdentity,wire.CapHumanParticipation,wire.CapGroupInvitationControl,wire.CapGroup,wire.CapRoom];
 for(const legacy of [g,a]){await w.caps(legacy,names);check(!(await b.checkHuman({conv,host:g.address})).ready,'mixed v081/v082 requires update');await refuses(()=>b.changeHuman('invite',{conv,host:g.address}),/update/i);await w.caps(legacy,[...names,wire.CapGroupHumanParticipation]);}
 const old=await b.sendDM({conv,body:'chosen old group context'});
 await b.sendDM({conv,body:'unshared old group context'});await w.drain(a);
 check((await b.checkHuman({conv,host:g.address})).ready,'ordinary member checks group guest');
 const p=await b.changeHuman('invite',{conv,host:g.address,share:[old.id]});
 await w.drain(g);await w.drain(a);

 check((await g.groupThread(conv)).guests.some(x=>x.pid===p.pid&&x.can_decide),'browser guest offered exact consent');
 await g.changeHuman('decide',{pid:p.pid,accept:true});await w.drain(a);await w.drain(b);
 await b.recoverHumanExcerpts();await w.drain(g);
 const view=await g.groupThread(conv);
 check(view.role==='human_guest'&&!view.members.some(m=>m.person===g.me.person),'guest has no membership');

 check(view.messages.some(m=>m.body==='chosen old group context')&&!view.messages.some(m=>m.body==='unshared old group context'),'only selected history disclosed');
 // Mixed active member readers must hold only their sealed copy, never the composer.
 for(const missing of [wire.CapGroupHumanParticipation,wire.CapGroup]) {
  const current=[...names,wire.CapGroupHumanParticipation];
  await w.caps(a,current.filter(cap=>cap!==missing && !(missing===wire.CapGroup && cap===wire.CapRoom)));
  const body=`[@Alice](agentnet:person/${a.me.person}) mixed ${missing}`;
  const sent=await b.sendDM({conv,body});
  const copy=(await b.store.all('outbox')).find(r=>r.lid===sent.lid&&r.to===a.address);
  const sealed=copy.envelope,scope=wire.humanJSON(copy.human);
  await b.post(copy);const held=await b.store.get('outbox',copy.id);
  check(held.state==='waiting'&&held.detail.includes(a.address),'mixed member copy waits with exact device detail '+missing);
  check(!w.posts.some(raw=>wire.parseEnvelope(raw).id===copy.id),'unsupported member ciphertext not handed off '+missing);
  await w.drain(g);check((await g.groupThread(conv)).messages.filter(m=>m.lid===sent.lid&&m.body===body).length===1,'compatible guest receives mention once '+missing);
  await w.caps(a,current);await w.drain(a);await w.drain(a);
  const restored=await b.store.get('outbox',copy.id);
  check(restored.envelope===sealed&&wire.humanJSON(restored.human)===scope,'recovery retains exact sealed context '+missing);
  check((await a.groupThread(conv)).messages.filter(m=>m.lid===sent.lid&&m.body===body).length===1,'updated member receives same logical message once '+missing);
 }

 await b.sendDM({conv,body:'new group member turn'});await w.drain(g);

 check((await g.groupThread(conv)).messages.some(m=>m.body==='new group member turn'),'guest reads captured new turn');
 await g.sendDM({conv,pid:p.pid,body:'new group guest turn'});await w.drain(a);await w.drain(b);
 check((await b.groupThread(conv)).messages.some(m=>m.body==='new group guest turn'),'guest writes with exact scope');
 // A reader may start an older signed session during a file upload. The
 // actual postStored final gate must stop the message, retaining sealed bytes.
 const fileTurn=await b.sendDM({conv,body:'Capability handoff check',files:[{name:'handoff.txt',size:4,bytes:new TextEncoder().encode('TEST')}]});
 const fileCopy=(await b.store.all('outbox')).find(r=>r.lid===fileTurn.lid&&r.to===g.address);
 const sealedFileCopy=fileCopy.envelope,capturedFileScope=wire.humanJSON(fileCopy.human);
 const upload=b.uploadBlob.bind(b);let readerDowngraded=false;
 b.uploadBlob=async(...args)=>{await upload(...args);if(!readerDowngraded){readerDowngraded=true;await w.caps(g,names);}};
 await b.post(fileCopy);b.uploadBlob=upload;
 const blockedFileCopy=await b.store.get('outbox',fileCopy.id);
 check(readerDowngraded&&!w.posts.some(raw=>wire.parseEnvelope(raw).id===fileCopy.id),'old signed hgg1 reader appearing during upload blocks actual final message handoff');
 check(blockedFileCopy.envelope===sealedFileCopy&&wire.humanJSON(blockedFileCopy.human)===capturedFileScope,'final hgg1 refusal preserves sealed message and captured scope');
 await w.caps(g,[...names,wire.CapGroupHumanParticipation]);
 await b.post(blockedFileCopy);
 check(w.posts.some(raw=>wire.parseEnvelope(raw).id===fileCopy.id),'same sealed copy can retry after current hgg1 support is restored');
 await refuses(()=>g.sendDM({conv,body:'no scope'}),/member|admission|guest/i);
 const second=await b.changeHuman('invite',{conv,host:other.address});await w.drain(other);await w.drain(a);await other.changeHuman('decide',{pid:second.pid,accept:true});await w.drain(b);await w.drain(a);
 await b.discloseHumanPass();await a.discloseHumanPass();await w.drain(g);await w.drain(other);await g.retryHeld();await other.retryHeld();

 check((await other.groupThread(conv)).guests.some(x=>x.pid===p.pid&&x.state==='active'),'another guest receives public counted lifecycle only');
 // Offline copies retain exact scope; key changes block retry.
 const pin=await b.store.get('pins',g.address);await b.store.write([{s:'pins',k:g.address,v:{...pin,pending:'changed'}}]);
 await refuses(()=>b.sendDM({conv,body:'changed guest key denied'}),/key|pinned|exact host/i);await b.store.write([{s:'pins',k:g.address,v:pin}]);
 await b.sendDM({conv,body:'captured before offline end'});
 const queued=(await b.store.all('outbox')).find(r=>r.to===g.address&&r.body==='captured before offline end');
 await b.changeHuman('end',{pid:p.pid});await w.drain(g);await w.drain(a);await w.drain(other);
 check((await other.groupThread(conv)).guests.find(x=>x.pid===p.pid)?.state==='dismissed','other guest learns ended scope');
 check(!!(await b.gate(await b.groupRecord(conv),queued)).why,'offline captured pre-end copy is fenced after dismiss');
 await refuses(()=>g.sendDM({conv,pid:p.pid,body:'after end'}),/active|guest/i);
 const leave=await b.changeHuman('invite',{conv,host:g.address});await w.drain(g);await w.drain(a);await g.changeHuman('decide',{pid:leave.pid,accept:true});await w.drain(b);await w.drain(a);
 await g.changeHuman('leave',{pid:leave.pid});await w.drain(b);await w.drain(a);
 check((await b.groupThread(conv)).guests.find(x=>x.pid===leave.pid).state==='dismissed','guest may leave without member rights');
}
// Pre-upgrade forwarded lifecycle copies used the transport member as author.
// Repair a completed own-human job without resetting history or hiding holds.
{
 const w=await world(),{alice:a,bob:b,carol:c,mallory:d,conv}=w;
 const cp=await a.changeHuman('invite',{conv,host:c.address});await w.drain(c);await w.drain(b);
 await c.changeHuman('decide',{pid:cp.pid,accept:true});await w.drain(a);await w.drain(b);
 const dp=await b.changeHuman('invite',{conv,host:d.address});await w.drain(d);await w.drain(a);
 await d.changeHuman('decide',{pid:dp.pid,accept:true});await w.drain(a);await w.drain(b);await a.discloseHumanAudience();
 const source=(await a.store.all('outbox')).find(r=>r.forwarded&&r.to===c.address&&r.pid===dp.pid&&wire.parseEvent(r.body).type==='scope');
 check(!!source,'real forwarded member-signed lifecycle source exists');
 const phone=await w.sibling(a,'alice/lifecycle-phone',true),dev=a.me.devices.find(d=>d.address===phone.address),chat=await a.store.get('convs',conv),raw=a.itemOf(source,true);
 const correct=await a.historyCopy(dev,chat,raw),item=wire.parseHistory(correct.body);
 check(item.from===b.address&&item.from_key===b.fp&&item.body===source.body&&item.id===source.id&&item.lid===source.lid,'forwarded lifecycle history keeps verified original author and exact source');
 const legacy=await a.ownCopy(dev,chat,'history',wire.historyJSON(raw));
 const authorPin=await phone.store.get('pins',b.address);
 await phone.store.write([{s:'pins',k:b.address,v:undefined}]);
 await w.receive(legacy.envelope,phone);
 check((await phone.store.get('receipts',legacy.id))?.state==='delivered'&&!await phone.store.get('held',legacy.id),'fresh phone recovers old transport-attributed signed public scope');
 check(!await phone.store.get('pins',b.address),'verified current author roster suffices without inventing an absent transport pin');
 await phone.store.write([{s:'pins',k:b.address,v:authorPin}]);
 await w.receive(correct.envelope,phone);
 check((await phone.store.get('receipts',correct.id))?.state==='delivered'&&!await phone.store.get('held',correct.id),'sealed own-phone receiver accepts unchanged signed lifecycle');
 await w.receive(legacy.envelope,phone);
 check((await phone.store.all('inbox')).filter(r=>r.lid===raw.lid).length===1&&(await phone.store.get('inbox',item.id)).state==='','old and corrected repeated carriers keep one inert item, no execution state');
 const atomic=await phone.admitInner(legacy.envelope,wire.parseEnvelope(legacy.envelope));
 await phone.store.write([{s:'pins',k:b.address,v:{...authorPin,pending:'changed'}}]);
 await refuses(()=>phone.store.write(atomic,atomic.checks),/storage changed during verification/);
 await phone.store.write([{s:'pins',k:b.address,v:authorPin}]);
 const collision=id(),occupied={id:collision,conv,lid:id(),from:b.address,fp:b.fp,sub:'',kind:'message',body:'different accepted record',at:now};
 await phone.store.write([{s:'inbox',k:collision,v:occupied}]);
 const savedOccupied=await phone.store.get('inbox',collision);
 const conflicting=await a.ownCopy(dev,chat,'history',wire.historyJSON({...raw,id:collision}));await w.receive(conflicting.envelope,phone);
 check((await phone.store.get('held',conflicting.id))?.reason==='conflicting_duplicate','legacy history cannot acknowledge an unrelated accepted source ID: '+(await phone.store.get('held',conflicting.id))?.reason);
 assert.deepEqual(await phone.store.get('inbox',collision),savedOccupied);checks++;
 const privateInvite=(await b.store.all('outbox')).find(r=>r.sub==='event'&&r.pid===dp.pid&&wire.parseEvent(r.body).type==='invite');
 const foreignScope=await wire.signEvent(c.keys,{...wire.parseEvent(raw.body),author:c.author()});
 const decline=await w.decision(d,dp.pid,'decline');
 for(const [body,why] of [[privateInvite.body,'private invite'],[wire.eventJSON(decline),'private decline'],[wire.eventJSON(foreignScope),'valid signature of unrelated guest']]) {
  const bad=await a.ownCopy(dev,chat,'history',wire.historyJSON({...raw,id:id(),lid:id(),body}));await w.receive(bad.envelope,phone);
  check((await phone.store.get('receipts',bad.id))?.state==='quarantined'&&!!await phone.store.get('held',bad.id),'legacy repair refuses '+why);
 }
 // Refusals must still apply to a previously seen item: a duplicate cannot
 // acknowledge a sender whose current authority changed after acceptance.
 for(const mode of ['sender-agent','reader-agent','removed-sender','removed-reader','frozen','pending-author','pending-sender','changed-author','wrong-transport','wrong-conv','wrong-pid','bad-signature']) {
  const own=await phone.store.get('kv','person'),savedAuthor=await phone.store.get('pins',b.address),savedSender=await phone.store.get('pins',a.address);
  let bad={...raw},restore=[];
  if(['sender-agent','reader-agent','removed-sender','removed-reader','frozen'].includes(mode)) {
   const fp=mode.includes('sender')?a.fp:phone.fp;
   const changed=mode==='frozen'?{...own,state:'conflict'}:mode.startsWith('removed')?{...own,devices:own.devices.filter(d=>d.fingerprint!==fp)}:{...own,human_keys:own.human_keys.filter(k=>k!==fp)};
   await phone.store.write([{s:'kv',k:'person',v:changed}]);restore.push({s:'kv',k:'person',v:own});
  } else if(mode==='pending-author'||mode==='changed-author') {
   await phone.store.write([{s:'pins',k:b.address,v:mode==='pending-author'?{...savedAuthor,pending:'changed'}:{...savedAuthor,fingerprint:c.fp,json:wire.marshalPublic(c.pub)}}]);restore.push({s:'pins',k:b.address,v:savedAuthor});
  } else if(mode==='pending-sender') {
   await phone.store.write([{s:'pins',k:a.address,v:{...savedSender,pending:'changed'}}]);restore.push({s:'pins',k:a.address,v:savedSender});
  } else if(mode==='wrong-transport')bad={...bad,from:c.address,from_key:c.fp};
  else {
   const e=wire.parseEvent(raw.body);if(mode==='wrong-conv')e.conv='a'.repeat(64);else if(mode==='wrong-pid')e.pid=id();else e.sig[0]^=1;bad.body=wire.eventJSON(e);
  }
  const copy=await a.ownCopy(dev,chat,'history',wire.historyJSON(bad));await w.receive(copy.envelope,phone);
  check((await phone.store.get('receipts',copy.id))?.state==='quarantined'&&!!await phone.store.get('held',copy.id),'legacy duplicate preserves '+mode+' refusal');
  await phone.store.write(restore);
 }
 // The exact same signed bytes stored as invalid by an old browser recover
 // after restart. The old group migration marker cannot consume this repair.
 const upgrade=await w.sibling(a,'alice/lifecycle-upgrade',true),upgradeDev=a.me.devices.find(d=>d.address===upgrade.address);
 const kept=await a.ownCopy(upgradeDev,chat,'history',wire.historyJSON(raw));
 await upgrade.store.write([{s:'held',k:kept.id,v:{id:kept.id,from:a.address,reason:'invalid',envelope:kept.envelope,at:now}},{s:'kv',k:'held-group-history-recovery-v1',v:true}]);
 const restarted=new Engine({store:upgrade.store,base:upgrade.base,fetch:upgrade.fetch,now:()=>now});await restarted.load();await restarted.retryHeld();
 check(!await upgrade.store.get('held',kept.id)&&(await upgrade.store.get('receipts',kept.id))?.state==='delivered','pre-held legacy ciphertext recovers through normal admission after restart');
 check(!!await upgrade.store.get('kv','held-dm-lifecycle-recovery-v1')&&!!await upgrade.store.get('kv','held-group-history-recovery-v1'),'DM upgrade records its own one-time marker without resetting group recovery');
 const stored=await upgrade.store.get('inbox',item.id);
 check(stored?.history&&stored.fp===b.fp&&stored.from===b.address&&stored.body===raw.body&&stored.state==='','recovery keeps exact original signed author and inert stored record');
 await restarted.load();await restarted.retryHeld();
 check((await upgrade.store.all('inbox')).filter(r=>r.lid===raw.lid).length===1&&!(await upgrade.store.all('outbox')).some(r=>['question','task'].includes(r.kind)),'restart neither duplicates history nor queues agent work');
 const accepted=(await a.store.all('outbox')).find(r=>r.forwarded&&r.to===c.address&&r.pid===dp.pid&&wire.parseEvent(r.body).type==='accept');
 check(!!accepted,'real outside-host acceptance was disclosed by original member');
 const late=await w.sibling(a,'alice/lifecycle-late',true),lateDev=a.me.devices.find(d=>d.address===late.address),acceptRaw=a.itemOf(accepted,true);
 const acceptCopy=await a.ownCopy(lateDev,chat,'history',wire.historyJSON(acceptRaw));
 await late.store.write([{s:'held',k:acceptCopy.id,v:{id:acceptCopy.id,from:a.address,reason:'invalid',envelope:acceptCopy.envelope,at:now}},{s:'kv',k:'held-group-history-recovery-v1',v:true}]);
 await late.load();await late.retryHeld();
 check((await late.store.get('held',acceptCopy.id))?.reason==='proof_pending'&&!!(await late.store.get('held',acceptCopy.id))?.history_recovery&&!await late.store.get('inbox',acceptRaw.id),'old acceptance waits inertly for its exact public scope after upgrade');
 const scopeCopy=await a.ownCopy(lateDev,chat,'history',wire.historyJSON(raw));await w.receive(scopeCopy.envelope,late);await late.retryHeld();
 check(!await late.store.get('held',acceptCopy.id)&&(await late.store.get('inbox',acceptRaw.id))?.fp===d.fp,'later exact scope rechecks and recovers original host acceptance');
 await late.store.write([{s:'persons',k:d.me.person,v:undefined},{s:'pins',k:d.address,v:undefined}]);
 const missingAuthor=await a.ownCopy(lateDev,chat,'history',wire.historyJSON(acceptRaw));await w.receive(missingAuthor.envelope,late);
 check((await late.store.get('receipts',missingAuthor.id))?.state==='delivered'&&(await late.store.get('persons',d.me.person))?.state==='pinned','missing acceptance-author proof is fetched and verified through the normal person chain');
 await d.changeHuman('leave',{pid:dp.pid});await w.drain(a);await w.drain(b);await a.discloseHumanAudience();
 const ended=(await a.store.all('outbox')).find(r=>r.forwarded&&r.to===c.address&&r.pid===dp.pid&&wire.parseEvent(r.body).type==='dismiss');
 check(!!ended,'original member discloses exact outside human-host end');
 const endRaw=a.itemOf(ended,true),endCopy=await a.ownCopy(lateDev,chat,'history',wire.historyJSON(endRaw));await w.receive(endCopy.envelope,late);
 check((await late.store.get('receipts',endCopy.id))?.state==='delivered'&&(await late.store.get('inbox',endRaw.id))?.fp===d.fp,'forwarded outside-host end retains its verified signer and counted predecessor');
 for(const field of ['author','conv','pid','sig']) {
  const e=wire.parseEvent(source.body);
  if(field==='author')e.author=c.author();else if(field==='conv')e.conv='a'.repeat(64);else if(field==='pid')e.pid=id();else e.sig[0]^=1;
  await refuses(()=>a.dmLifecycleHistorySource(chat,{...raw,body:wire.eventJSON(e)}),/lifecycle|signature|record/i);
 }
 const old=await a.ownCopy(dev,chat,'history',wire.historyJSON(raw));old.recipient_fp=phone.fp;old.state='quarantined';
 const oldKey='history-copy/'+phone.fp+'/'+conv+'/'+a.fp+'/'+raw.lid,newKey='history-copy/'+phone.fp+'/'+conv+'/'+b.fp+'/'+raw.lid;
 const ceiling=await a.store.get('kv','history-arrival')||0;
 const job={device:phone.address,fingerprint:phone.fp,state:'done',pos:{conv,ms:now,id:source.id},done:1,total:1,own_human:a.fp,catchup:{v:2,source:a.fp,stage:'tail',recent:conv,older:null,tail:ceiling,ceiling,started:now}};
 const pin=await a.store.get('pins',b.address);
 // Simulate the old browser's progress ledger and pending author in one
 // transaction. Admission already started background history: separate setup
 // writes otherwise let it legitimately repair the mapping before the pin
 // becomes pending. Old browsers have no corrected-author progress entry;
 // reset that fixture metadata only, retaining every ciphertext copy.
 await a.store.write([{s:'pins',k:b.address,v:{...pin,pending:'changed'}},{s:'outbox',k:old.id,v:old},{s:'kv',k:oldKey,v:{hash:await wire.groupHistoryContentHash(conv,raw),copy:old.id}},{s:'kv',k:newKey},{s:'kv',k:'history',v:{[phone.address]:job}}]);
 check((await a.store.get('pins',b.address))?.pending==='changed'&&(await a.store.get('kv',oldKey))?.copy===old.id&&!await a.store.get('kv',newKey),'old-browser repair fixture starts with pending author, exact blocked copy and no corrected progress');
 if(a.historyRun)await a.historyRun;
 await a.historyCatchupStep(dev,(await a.historyBook())[phone.address]);
 const blockedOld=await a.store.get('kv',oldKey),blockedNew=await a.store.get('kv',newKey),deferred=await a.store.get('kv','history-deferred/'+phone.fp+'/'+conv+'/'+a.fp+'/'+raw.lid);
 check(!!blockedOld&&!blockedNew&&deferred?.why==='invalid','pending author key keeps old blocked mapping until verified replacement exists: '+JSON.stringify({old:!!blockedOld,new:!!blockedNew,deferred:deferred?.why,pending:(await a.store.get('pins',b.address))?.pending}));
 await a.store.write([{s:'pins',k:b.address,v:pin}]);a.historyWake=(a.historyWake||0)+1;
 await a.historyCatchupStep(dev,(await a.historyBook())[phone.address]);
 const repaired=await a.store.get('kv',newKey),row=repaired&&await a.store.get('outbox',repaired.copy);
 check(!!row&&!await a.store.get('kv',oldKey)&&(await a.store.get('outbox',old.id)).state==='quarantined','verified replacement retires only obsolete progress mapping, retaining old ciphertext');
 await w.receive(row.envelope,phone);await a.dispatch('receipt',JSON.stringify({id:row.id,state:'delivered',seq:1}));
 const before=(await a.store.all('outbox')).length,position=JSON.stringify((await a.historyBook())[phone.address].pos);
 a.historySweeps=new Map();a.historyWake++;
 await a.historyCatchupStep(dev,(await a.historyBook())[phone.address]);
 check((await a.store.all('outbox')).length===before&&JSON.stringify((await a.historyBook())[phone.address].pos)===position,'restart/reconnect preserves completed cursor and exact corrected copy');
 check((await a.overview()).history.find(j=>j.device===phone.address).blocked===0,'old quarantined transport tuple no longer falsely reports blocked history');
}
console.log('human engine isolated lifecycle checks passed: ' + checks);
