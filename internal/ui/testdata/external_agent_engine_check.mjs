// Isolated real-key browser Engine world. No real peer/model/browser/cloud.
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
  for (const name of ['alice', 'bob', 'charlie', 'mallory']) {
    const e = new Engine({ store: memoryStore(), now: () => now, base: 'https://synthetic.invalid', fetch });
    e.keys = await wire.newKeys(); e.address = name + '/desk'; e.pub = await wire.publicEntry(e.keys, e.address); e.fp = await wire.fingerprint(e.pub);
    e.roster = await wire.newRoster(e.keys, e.address, name); e.me = await e.personRecord([e.roster], 'self', null);
    await e.store.write([{ s: 'kv', k: 'identity', v: { keys: e.keys, address: e.address, fingerprint: e.fp } }, { s: 'kv', k: 'person', v: e.me }]);
    e.connected = false; pubs.set(e.address, e.pub); rosters.set(e.me.person, [JSON.parse(wire.rosterJSON(e.roster))]); users.push(e);
  }
  const caps = async (e, names = [wire.CapEnv2, wire.CapPerson, wire.CapAgentIdentity, wire.CapExternalParticipation]) => {
    const session = id(); profiles.set(e.address, { person: JSON.parse(wire.rosterJSON(e.roster)), live: true, sessions: [session], caps: [JSON.parse(wire.capsJSON(await wire.newCaps(e.keys, e.address, session, names)))] });
  };
  for (const e of users) { await caps(e); for (const other of users) {
    const p = await e.personRecord([other.roster], other === e ? 'self' : 'pinned', null);
    await e.store.write([{ s: 'persons', k: p.person, v: p }, { s: 'pins', k: other.address, v: { address: other.address, json: wire.marshalPublic(other.pub), fingerprint: other.fp, pending: null } }]);
  } }
  const [alice, bob, charlie, mallory] = users;
  const agent = await wire.signAgent(charlie.keys, { id: id(), host: charlie.address, host_key: charlie.fp, label: 'Charlie agent', ts: Math.floor(now / 1000) });
  catalogs.set(charlie.address, [JSON.parse(wire.agentJSON(agent))]);
  const conv = await alice.newDM(bob.address), c = await alice.store.get('convs', conv);
  const receive = async (raw, e) => { const env = wire.parseEnvelope(raw); await e.admit(raw, env); return env.id; };
  const drain = async (e, predicate = () => true) => { for (const raw of posts) { const env = wire.parseEnvelope(raw); if (env.to === e.address && predicate(env) && !await e.store.get('inbox', env.id) && !await e.store.get('held', env.id)) await receive(raw, e); } };
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
  return { alice, bob, charlie, mallory, users, conv, c, agent, posts, blobs, caps, catalogs, receive, drain, from, decision, accept, sibling, offline: (v) => { offline = v; } };
}

// Member DM remains normal; outside host gets only exact grant/file bytes.
{
  const w = await world(), { alice: a, bob: b, charlie: c } = w;
  const hidden = await a.sendDM({ conv: w.conv, body: 'UNSELECTED SECRET', files: [{ name: 'hidden.txt', size: 6, bytes: new TextEncoder().encode('HIDDEN') }] });
  const selected = await a.sendDM({ conv: w.conv, body: 'SELECTED TEXT', files: [{ name: 'chosen.txt', size: 6, bytes: new TextEncoder().encode('CHOSEN') }] });
  await w.drain(b);
  const invite = await a.inviteAgent({ conv: w.conv, host: c.address, agent_id: w.agent.id, share: [selected.id] });
  await w.drain(b); await w.drain(c);
  check((await a.dm(w.conv)).role === 'member' && (await c.dm(w.conv)).role === 'visitor', 'host proof does not make membership');
  const hostMessages = (await c.dm(w.conv)).messages;
  const excerpt = hostMessages.find((m) => m.excerpt_pid === invite.pid);
  check(excerpt.body === 'SELECTED TEXT' && excerpt.claimed_key === a.fp && excerpt.replica && excerpt.dir === 'in' && !excerpt.agent_id && excerpt.can.length === 0, 'selected excerpt claimed, non-executable, immutable original author claim');
  check(!hostMessages.some((m) => m.body.includes('UNSELECTED')) && !hostMessages.some((m) => m.id === hidden.id), 'no ambient room text');
  const rawExcerpt = (await c.store.all('inbox')).find((m) => m.sub === 'excerpt');
  check(rawExcerpt.lid === await wire.excerptLID(invite.pid, { lid: selected.lid, fingerprint: a.fp }), 'stable carrier identity');
  check(new TextDecoder().decode((await c.openFile(excerpt.id, 0, 'in')).bytes) === 'CHOSEN', 'selected bytes decrypt through existing file opener');
  check((await a.dm(w.conv)).messages.filter((m) => m.body === 'SELECTED TEXT').length === 1, 'sender excerpt carrier never extra room turn');
  await refuses(() => c.sendDM({ conv: w.conv, body: 'ordinary visitor send' }), /member|record|conversation/i);
  await refuses(() => c.inviteAgent({ conv: w.conv, host: b.address, agent_id: w.agent.id }), /member/);
  const badOrdinary = await w.receive(await w.from(c, a, { body: 'visitor discussion' }), a);
  check((await a.store.get('held', badOrdinary)).reason === 'invalid', 'ordinary visitor receive refused');
  const wrongPrev = await w.decision(c, invite.pid, 'accept', 'f'.repeat(64));
  const invalidDecision = await w.receive(await w.from(c, a, { sub: 'event', pid: invite.pid, body: wire.eventJSON(wrongPrev) }), a);
  check((await a.store.get('held', invalidDecision)).reason === 'invalid', 'wrong Prev held without changing participation');
  await w.accept(invite.pid);
  check((await a.agentConv(invite.pid)).info.state === 'active', 'exact host decision activates');
  const request = await a.askAgent({ pid: invite.pid, kind: 'task', body: '', files: [{ name: 'input.txt', size: 5, bytes: new TextEncoder().encode('INPUT') }] });
  const ownRequest = await a.store.get('outbox', request.id);
  check(request.id === request.lid && ownRequest.to === c.address && ownRequest.required_cap === 'apx1', 'host executable ID equals shared LID with durable apx1');
  await w.drain(b); await w.drain(c);
  check(new TextDecoder().decode((await c.openFile(request.id, 0, 'in')).bytes) === 'INPUT', 'file-only explicit task bytes');
  const task = await c.store.get('inbox', request.id);
  check(task.state === 'conv_held', 'browser host never executes');
  const output = { kind: 'result', body: 'completed report', pid: invite.pid, reply_to: request.id, agent_id: w.agent.id, origin: 'agent:stub', emotion: 'plain' };
  // Nonterminal progress: the exact host's output authority, never an answer.
  const progress = { kind: 'message', status: wire.StatusProgress, body: 'checking now', pid: invite.pid, reply_to: request.id, agent_id: w.agent.id, origin: 'agent:stub', emotion: 'neutral' };
  for (const e of [a, b]) {
    const row = await e.store.get('inbox', await w.receive(await w.from(c, e, progress), e));
    check(row?.status === wire.StatusProgress && row.kind === 'message' && row.agent_id === w.agent.id, 'participation progress admitted under the exact host output authority');
    check(!(await e.dm(w.conv)).messages.some(m => m.pid === invite.pid && ['answer', 'result'].includes(m.kind)), 'progress is never an answer or result');
  }
  for (const extra of [{ agent_id: id() }, { reply_to: selected.id }]) {
    const bad = await w.receive(await w.from(c, a, { ...progress, ...extra }), a);
    check(!!await a.store.get('held', bad) && !await a.store.get('inbox', bad), 'progress for another agent or a non-request parent refused');
  }
  const foreignProgress = await w.receive(await w.from(w.users[3], a, progress), a);
  check((await a.store.get('held', foreignProgress))?.reason === 'invalid', 'only the exact host sends participation progress');
  for (const e of [a, b]) {
    const result = await w.receive(await w.from(c, e, output), e);
    check((await e.store.get('inbox', result)).agent_id === w.agent.id, 'output exact binding on both human copies');
    const thread = await e.dm(w.conv), report = thread.messages.find((m) => m.id === result);
    // The output names the logical request; the view links it to the request as shown here.
    const parents = thread.messages.filter((m) => m.id === report.reply_to && m.pid === invite.pid && m.kind === 'task');
    check(parents.length === 1 && parents[0].lid === request.lid && parents[0].target.agent_id === w.agent.id, 'DM projection links the output to the verified logical request on both human copies');
    if (e === b) check(parents[0].id !== parents[0].lid, 'audience physical copy differs from projected logical request ID');
  }
  const sibling = w.users[3]; // Separate enrolled person cannot impersonate host, label irrelevant.
  const foreign = await w.receive(await w.from(sibling, a, output), a);
  check((await a.store.get('held', foreign)).reason === 'invalid', 'wrong host refuses agent authorship');
  for (const extra of [{ pid: id() }, { agent_id: id() }, { reply_to: selected.id }, { kind: 'answer' }]) {
    const bad = await w.receive(await w.from(c, a, { ...output, ...extra }), a);
    check(!!await a.store.get('held', bad) && !await a.store.get('inbox', bad), 'wrong PID, ID or request refuses');
  }
  const second = await a.inviteAgent({ conv: w.conv, host: c.address, agent_id: w.agent.id }); await w.drain(b); await w.drain(c); await w.accept(second.pid);
  const crossPID = await w.receive(await w.from(c, a, { ...output, pid: second.pid }), a);
  check((await a.store.get('held', crossPID)).reason === 'invalid', 'known sibling PID cannot reuse another participation request');
  const badTarget = { ...ownRequest, target: { ...ownRequest.target, fingerprint: b.fp } };
  await a.store.write([{ s: 'outbox', k: request.id, v: badTarget }]);
  const wrongTarget = await w.receive(await w.from(c, a, output), a);
  check((await a.store.get('held', wrongTarget)).reason === 'invalid', 'matching request ID must still bind exact target host key');
  await a.store.write([{ s: 'outbox', k: request.id, v: ownRequest }]);
  await refuses(() => a.askAgent({ pid: invite.pid, body: 'too many', files: Array(9).fill({ name: 'x', size: 0, bytes: new Uint8Array() }) }), /at most 8/);
  await refuses(() => a.askAgent({ pid: invite.pid, body: 'false size', files: [{ name: 'oversize', size: 0, bytes: new Uint8Array(wire.BrowserMaxFile + 1) }] }), /larger than/);
  const follow = await b.askAgent({ pid: invite.pid, body: 'follow up' });
  check(follow.id === follow.lid, 'other human follow-up same participation');
  await a.dismissAgent(invite.pid); await w.drain(b); await w.drain(c);
  check((await b.agentConv(invite.pid)).info.state === 'dismissed', 'explicit dismissal converges');
  await refuses(() => b.askAgent({ pid: invite.pid, body: 'after dismissal' }), /not active/);
  const late = await w.receive(await w.from(c, a, output), a);
  check((await a.store.get('held', late)).reason === 'invalid', 'late output cannot revive dismissed participation');
  for (const e of w.users) e.stop();
}

// Delayed request proof, replay, capability/key changes, selected original PID.
{
  const w = await world(), { alice: a, bob: b, charlie: c } = w;
  const shared = await a.sendDM({ conv: w.conv, body: 'original' }); await w.drain(b);
  await refuses(() => a.inviteAgent({ conv: w.conv, host: c.address }), /exact named/);
  await w.caps(c, [wire.CapEnv2, wire.CapPerson, wire.CapAgentIdentity]);
  const before = (await a.store.all('outbox')).length;
  await refuses(() => a.inviteAgent({ conv: w.conv, host: c.address, agent_id: w.agent.id }), /external participation/);
  check((await a.store.all('outbox')).length === before, 'stale capability has no fallback/persistence');
  await w.caps(c);
  const inv = await a.inviteAgent({ conv: w.conv, host: c.address, agent_id: w.agent.id, share: [shared.id] }); await w.drain(b); await w.drain(c); await w.accept(inv.pid);
  const request = await a.askAgent({ pid: inv.pid, body: 'delayed' });
  const raw = await w.from(c, b, { kind: 'answer', body: 'early', pid: inv.pid, reply_to: request.id, agent_id: w.agent.id });
  const result = await w.receive(raw, b);
  check((await b.store.get('held', result)).reason === 'proof_pending', 'output before exact request remains proof pending');
  await w.drain(b); await b.retryHeld();
  check(!!await b.store.get('inbox', result) && !await b.store.get('held', result), 'request proof releases delayed output');
  const count = (await b.store.all('inbox')).length; await w.receive(raw, b);
  check((await b.store.all('inbox')).length === count, 'replayed output idempotent');
  const originalPID = id();
  // Frozen manifest without bytes stays honestly unavailable; other signed
  // bytes not covered by that manifest cannot ride the selected carrier.
  const missingManifest = { name: 'missing.txt', size: 1, sha256: 'a'.repeat(64) };
  const missingItem = { from: a.address, from_key: a.fp, id: id(), lid: shared.lid, ts: 1790000000, kind: 'message', body: 'manifest only', at: now, attachments: [missingManifest] };
  const missingID = await w.receive(await w.from(a, c, { sub: 'excerpt', pid: inv.pid, replica: true, body: wire.historyJSON(missingItem) }), c);
  const missingView = (await c.dm(w.conv)).messages.find((m) => m.id === missingID);
  check(missingView.attachments[0].availability === 'unavailable' && !missingView.attachments[0].openable, 'selected missing bytes shown unavailable');
  const extraFile = await wire.encryptFile(new TextEncoder().encode('X'), 'different.txt', c.pub);
  const smuggled = await w.receive(await w.from(a, c, { sub: 'excerpt', pid: inv.pid, replica: true, body: wire.historyJSON(missingItem), attachments: [extraFile.attachment] }), c);
  check((await c.store.get('held', smuggled)).reason === 'invalid', 'extra selected file bytes outside exact manifest refused');
  const historical = { from: a.address, from_key: a.fp, id: id(), lid: shared.lid, ts: 1790000000, kind: 'question', body: 'claimed old request', pid: originalPID, at: now, target: { address: b.address, fingerprint: b.fp } };
  // Use another carrier logical ID for admission of synthetic selected same-reference excerpt.
  const otherInv = await a.inviteAgent({ conv: w.conv, host: c.address, agent_id: w.agent.id, share: [shared.id] }); await w.drain(c);
  const carrier2 = await w.from(a, c, { kind: 'message', sub: 'excerpt', replica: true, pid: otherInv.pid, body: wire.historyJSON(historical) });
  const excerpt = await w.receive(carrier2, c), shown = (await c.dm(w.conv)).messages.find((m) => m.id === excerpt);
  check(shown.pid === originalPID && shown.excerpt_pid === otherInv.pid && shown.kind === 'question' && shown.state === '' && shown.replica, 'original PID retained separately; claimed old request never held/executed');
  const unselected = { ...historical, lid: id() };
  const denied = await w.receive(await w.from(a, c, { sub: 'excerpt', pid: otherInv.pid, replica: true, body: wire.historyJSON(unselected) }), c);
  check((await c.store.get('held', denied)).reason === 'invalid', 'unsigned additional selection refused');
  w.offline(true);
  const queued = await a.askAgent({ pid: inv.pid, body: 'retry', files: [{ name: 'offline.txt', size: 7, bytes: new TextEncoder().encode('OFFLINE') }] });
  const q = await a.store.get('outbox', queued.id);
  check(q.state === 'waiting' && q.files[0].ct.length > 7, 'offline files persist encrypted before any send');
  w.offline(false);
  await w.caps(c, [wire.CapEnv2, wire.CapPerson, wire.CapAgentIdentity]); const sent = w.posts.length;
  await a.load(); a.connected = true; await a.flushOutbox(); a.connected = false;
  const retained = await a.store.get('outbox', q.id);
  check(retained.envelope === q.envelope && retained.required_cap === 'apx1' && retained.state === 'waiting' && !w.posts.slice(sent).some((raw) => wire.parseEnvelope(raw).to === c.address), 'reload rechecks latest capabilities; exact ciphertext no fallback');
  const pin = await a.store.get('pins', c.address); await a.store.write([{ s: 'pins', k: c.address, v: { ...pin, pending: { fingerprint: 'f'.repeat(64) } } }]);
  await refuses(() => a.askAgent({ pid: inv.pid, body: 'changed key' }), /pinned|key/);
  for (const e of w.users) e.stop();
}
// Sharing freezes visible revision; retry never replaces it with later edits.
{
  const w = await world(), a = w.alice, c = w.charlie;
  const original = await a.sendDM({ conv: w.conv, body: 'old' });
  const revision = { id: id(), conv: w.conv, control: true, sub: wire.SubRevision, ref: { id: original.lid, fingerprint: a.fp }, person: a.me.person, body: JSON.stringify({ rev: 1, text: 'selected revision' }), at: now };
  await a.store.write([{ s: 'outbox', k: revision.id, v: revision }]);
  const inv = await a.inviteAgent({ conv: w.conv, host: c.address, agent_id: w.agent.id, share: [original.id] }); await w.drain(c);
  const excerpt = (await c.dm(w.conv)).messages.find((m) => m.excerpt_pid === inv.pid);
  check(excerpt.body === 'selected revision', 'selected visible revision frozen rather than superseded text');
  const carrier = (await a.store.all('outbox')).find((r) => r.pid === inv.pid && r.sub === 'excerpt');
  await a.store.write([{ s: 'outbox', k: revision.id, v: { ...revision, body: JSON.stringify({ rev: 2, text: 'later unselected edit' }) } }]);
  const invitation = wire.parseEvent((await a.store.all('outbox')).find((r) => r.pid === inv.pid && r.sub === 'event').body);
  await a.sendGrantedExcerpts(w.c, invitation);
  const retained = (await a.store.all('outbox')).filter((r) => r.pid === inv.pid && r.sub === 'excerpt');
  check(retained.length === 1 && retained[0].envelope === carrier.envelope, 'retry retains frozen selected excerpt ciphertext');
  for (const e of w.users) e.stop();
}
// Linked human receives host decisions/outputs only after exact invitation and
// request proof; external-host siblings receive no visitor-room history.
{
  const w = await world(), { alice: a, bob: b, charlie: c } = w;
  const inv = await a.inviteAgent({ conv: w.conv, host: c.address, agent_id: w.agent.id }); await w.drain(b); await w.drain(c); await w.accept(inv.pid);
  const request = await a.askAgent({ pid: inv.pid, body: 'history request' }); await w.drain(b);
  const answer = await w.receive(await w.from(c, a, { kind: 'answer', body: 'history report', pid: inv.pid, reply_to: request.id, agent_id: w.agent.id }), a);
  const later = await w.sibling(a, 'alice/later'), dev = a.me.devices.find((d) => d.address === later.address);
  const outputCopy = await a.historyCopy(dev, w.c, a.itemOf(await a.store.get('inbox', answer), false));
  check(outputCopy.required_cap === 'apx1', 'external output history retains durable apx1');
  await w.receive(outputCopy.envelope, later);
  check((await later.store.get('held', outputCopy.id)).reason === 'proof_pending', 'linked output waits for signed external invitation');
  const inviteRow = (await a.store.all('outbox')).find((r) => r.pid === inv.pid && r.sub === 'event');
  const inviteCopy = await a.historyCopy(dev, w.c, a.itemOf(inviteRow, true)); await w.receive(inviteCopy.envelope, later);
  const acceptRow = (await a.store.all('inbox')).find((r) => r.pid === inv.pid && r.sub === 'event');
  const acceptCopy = await a.historyCopy(dev, w.c, a.itemOf(acceptRow, false)); await w.receive(acceptCopy.envelope, later);
  await a.dismissAgent(inv.pid);
  const dismissedRow = (await a.store.all('outbox')).find((r) => r.pid === inv.pid && r.sub === 'event' && wire.parseEvent(r.body).type === 'dismiss');
  const dismissedCopy = await a.historyCopy(dev, w.c, a.itemOf(dismissedRow, true)); await w.receive(dismissedCopy.envelope, later);
  await later.retryHeld();
  check((await later.store.get('held', outputCopy.id)).reason === 'proof_pending', 'linked output still needs exact original request');
  const reqCopy = await a.historyCopy(dev, w.c, a.itemOf(await a.store.get('outbox', request.id), true)); await w.receive(reqCopy.envelope, later); await later.retryHeld();
  check((await later.store.get('inbox', answer)).agent_id === w.agent.id && (await later.dm(w.conv)).role === 'member', 'linked human exact host output admitted without room membership change');
  const linkedThread = await later.dm(w.conv), linkedOutput = linkedThread.messages.find((m) => m.id === answer);
  check(linkedThread.messages.some((m) => m.lid === linkedOutput.reply_to && m.pid === inv.pid && m.kind === 'question'), 'linked human DM projection keeps logical request ID for output parent');
  const hostSibling = await w.sibling(c, 'charlie/later');
  const job = { device: hostSibling.address, fingerprint: hostSibling.fp, state: 'running', done: 0, total: 1 };
  await c.store.write([{ s: 'kv', k: 'history', v: { [hostSibling.address]: job } }]);
  await c.historyStep(c.me.devices.find((d) => d.address === hostSibling.address), job);
  check(!(await c.store.all('outbox')).some((r) => r.conv === w.conv && r.sub === 'history'), 'external-host sibling receives no visitor-room history');
  for (const e of w.users) e.stop();
}
console.log('external-agent engine checks passed: ' + checks);
