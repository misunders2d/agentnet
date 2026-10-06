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
  const users = [], pubs = new Map(), profiles = new Map(), rosters = new Map(), catalogs = new Map(), blobs = new Map(), posts = [];
  let offline = false;
  const fetch = async (url, o = {}) => {
    if (offline) throw new TypeError('Failed to fetch');
    const p = new URL(url).pathname;
    if (p === '/v1/version') return json({ features: ['env2', 'person2', 'caps', 'notify1'], realm_id: 'test-realm' });
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
console.log('human engine isolated lifecycle checks passed: ' + checks);
