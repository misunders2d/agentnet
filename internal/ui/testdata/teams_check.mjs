// Synthetic real-key parity; no network, browser, peers or grants.
import assert from 'node:assert/strict';
import { webcrypto } from 'node:crypto';
globalThis.crypto ||= webcrypto;
const wire = await import('../static/wire.mjs');
const { browserTeams, teamCanonical, teamHash, signTeam, applyTeam, validateTeam } = await import('../static/teams.mjs');
const { memoryStore } = await import('../static/engine.mjs');
const realm = '00112233445566778899aabbccddeeff', id = 'fedcba9876543210fedcba9876543210';
async function fixedKeys(seed) {
  const der = Buffer.concat([Buffer.from('302e020100300506032b657004220420', 'hex'), Buffer.alloc(32, seed)]);
  const sign = await crypto.subtle.importKey('pkcs8', der, 'Ed25519', false, ['sign']);
  // Fixed test-only public key for seed zero; other fixture signers use JWK derivation.
  const { createPrivateKey, createPublicKey } = await import('node:crypto');
  const raw = createPublicKey(createPrivateKey({ key: der, format: 'der', type: 'pkcs8' })).export({ format: 'jwk' });
  const signPublic = await crypto.subtle.importKey('jwk', raw, 'Ed25519', true, ['verify']);
  const box = await crypto.subtle.importKey('pkcs8', Buffer.concat([Buffer.from('302e020100300506032b656e04220420', 'hex'), Buffer.alloc(32, seed + 10)]), 'X25519', false, ['deriveBits']);
  return { sign, signPublic, box };
}
// Deterministic IDs too; fixed imported signing/box keys remain real WebCrypto keys.
let nonce = 0;
crypto.getRandomValues = bytes => { bytes.fill(++nonce); return bytes; };
const keys = await fixedKeys(0);
const golden = { v: 1, realm_id: realm, team: id, seq: 0, prev: '', author: {
  person: '0123456789abcdef0123456789abcdef', roster: 'f78b94d0e4bf9f75df8a53076cb188f257acf1b660df845f0da670fdeef9f755',
  address: 'vitalii/desk', fingerprint: '19c77bce-aca933c7-80e1c0e9-e46fc988' }, op: 'create', name: 'Ops <&> Ю', ts: 1790000000 };
const expected = 'agentnet-team-v1\n{"v":1,"realm_id":"00112233445566778899aabbccddeeff","team":"fedcba9876543210fedcba9876543210","seq":0,"prev":"","author":{"person":"0123456789abcdef0123456789abcdef","roster":"f78b94d0e4bf9f75df8a53076cb188f257acf1b660df845f0da670fdeef9f755","address":"vitalii/desk","fingerprint":"19c77bce-aca933c7-80e1c0e9-e46fc988"},"op":"create","name":"Ops \\u003c\\u0026\\u003e Ю","ts":1790000000}';
assert.equal(new TextDecoder().decode(teamCanonical(golden)), expected);
assert.equal(await teamHash(golden), '3e062daa30eb7a21dfae4b1b9a705fd791ef46f5086732ba6fbba7ddb4f2d89b');
assert.equal(wire.hex(wire.unb64((await signTeam(keys, golden)).sig)), 'a2ae5e29bc0f093af6a4ad0db44b90e6454934ea380371e367d567a872c60b8560508274b14af80c8bd5cf51e4f2a727517eed5e5734821c50b3503afc1ec808');
assert.throws(() => validateTeam({ ...golden, members: [] }), /unknown/);
const tagVector={...golden,v:2,seq:1,prev:await teamHash(golden),op:'agent-add',name:undefined,agent:{id,host:golden.author.address,host_key:golden.author.fingerprint}};
const tagExpected=expected.replace('"v":1','"v":2').replace('"seq":0,"prev":""','"seq":1,"prev":"3e062daa30eb7a21dfae4b1b9a705fd791ef46f5086732ba6fbba7ddb4f2d89b"').replace(/"op":"create","name":.*,"ts":/,'"op":"agent-add","agent":{"id":"fedcba9876543210fedcba9876543210","host":"vitalii/desk","host_key":"19c77bce-aca933c7-80e1c0e9-e46fc988"},"ts":');
assert.equal(new TextDecoder().decode(teamCanonical(tagVector)),tagExpected,'v2 agent target has the same signed bytes in Go and browser');

const rosterA = await wire.newRoster(keys, 'alice/desk', 'Alice');
const keysB = await fixedKeys(1), rosterB = await wire.newRoster(keysB, 'bob/desk', 'Bob');
const proofMap = new Map([[rosterA.person, rosterA], [rosterB.person, rosterB]]);
async function step(roster, key, prev, op, extras = {}, team = id) {
  return signTeam(key, { v: 1, realm_id: realm, team, seq: prev ? prev.seq + 1 : 0, prev: prev?.hash || '',
    author: { person: roster.person, roster: await wire.rosterHash(roster), address: roster.devices[0].address, fingerprint: await wire.fingerprint(roster.devices[0]) },
    op, ts: 1790000000, ...extras });
}
const create = await step(rosterA, keys, null, 'create', { name: 'Ops' });
let state = await applyTeam(create, null, rosterA);
assert.deepEqual(state.members, [rosterA.person]);
await assert.rejects(applyTeam(await step(rosterA, keys, state, 'leave'), state, rosterA), /last manager/);
const join = await step(rosterB, keysB, state, 'join');
state = await applyTeam(join, state, rosterB);
await assert.rejects(applyTeam(await step(rosterB, keysB, state, 'rename', { name: 'Hijack' }), state, rosterB), /manager/);
const promote = await step(rosterA, keys, state, 'manager-add', { target: rosterB.person });
state = await applyTeam(promote, state, rosterA);
state = await applyTeam(await step(rosterA, keys, state, 'manager-remove', { target: rosterA.person }), state, rosterA);
state = await applyTeam(await step(rosterA, keys, state, 'leave'), state, rosterA);
assert.deepEqual(state.managers, [rosterB.person]);
state = await applyTeam(await step(rosterB, keysB, state, 'archive'), state, rosterB);
await assert.rejects(applyTeam(await step(rosterA, keys, state, 'join'), state, rosterA), /archived/);

// V2 tags have explicit people/agent targets, independent of management.
const tagged = await step(rosterA,keys,null,'create',{v:2,name:'Reviewers'});
let tagState=await applyTeam(tagged,null,rosterA);
assert.equal(tagState.version,2);assert.deepEqual(tagState.members,[]);
const ref={id:'3'.repeat(32),host:rosterB.devices[0].address,host_key:await wire.fingerprint(rosterB.devices[0])};
for(const op of ['agent-add','agent-add']) tagState=await applyTeam(await step(rosterA,keys,tagState,op,{v:2,agent:ref}),tagState,rosterA);
assert.deepEqual(tagState.agents,[ref]);
await assert.rejects(applyTeam(await step(rosterB,keysB,tagState,'agent-remove',{v:2,agent:ref}),tagState,rosterB),/manager/);
tagState=await applyTeam(await step(rosterA,keys,tagState,'add',{v:2,target:rosterB.person}),tagState,rosterA);
assert.deepEqual(tagState.members,[rosterB.person]);
await assert.rejects(step(rosterB,keysB,tagState,'join',{v:2}),/version/);
await assert.rejects(step(rosterA,keys,tagState,'agent-add',{agent:ref}),/version/);
await assert.rejects(step(rosterA,keys,null,'create',{v:2,name:'EVERYONE'}),/reserved/);
const immutableTag=structuredClone(tagState);
const changedRef={...ref,host_key:await wire.fingerprint(rosterA.devices[0])};
tagState=await applyTeam(await step(rosterA,keys,tagState,'agent-remove',{v:2,agent:changedRef}),tagState,rosterA);
assert.deepEqual(tagState.agents,[ref]);
await assert.rejects(applyTeam(await step(rosterA,keys,tagState,'rename',{name:'Wrong version'}),tagState,rosterA),/version/);
tagState=await applyTeam(await step(rosterA,keys,tagState,'agent-remove',{v:2,agent:ref}),tagState,rosterA);
assert.equal(tagState.agents,undefined);assert.deepEqual(immutableTag.agents,[ref]);

const chains = new Map([[id, [create, join]]]);
let calls = [], offline = false, conflictOnce = false, failPut = false, duringGet = null, tagsSupported=false;
const store = memoryStore();
const before = { s: 'kv', k: 'unrelated-grant', v: { allow: true } }; await store.write([before]);
const engine = { store, keys, address: 'alice/desk', fp: await wire.fingerprint(rosterA.devices[0]),
  me: { person: rosterA.person, hash: await wire.rosterHash(rosterA) },
  async pinChain(person) {
    const r = proofMap.get(person); if (!r) throw Error('unknown person');
    const hash = await wire.rosterHash(r); return { person, hash, hashes: [hash], state: 'pinned' };
  },
  async chain(person) { return [proofMap.get(person)]; },
  async call(method, path, body) {
    calls.push([method, path]); if (offline) throw Error('offline');
    if (path === '/v1/teams' || path === '/v1/teams?version=2') {
      const teams = [];
      for (const [id, records] of chains) teams.push({ id, seq: records.length - 1, hash: await teamHash(records.at(-1)) });
      if (duringGet) { const f = duringGet; duringGet = null; f(); }
      return { realm_id: realm, teams, truncated: false,...(tagsSupported?{version:2}:{}) };
    }
    if (method === 'DELETE') { chains.delete(path.split('/')[3]); return null; }
    if (method === 'GET') {
      const id = path.split('/')[3]; return { realm_id: realm, team: id, records: chains.get(id), more: false };
    }
    const s = JSON.parse(body);
    if (failPut) throw Error('lost acceptance response');
    if (conflictOnce) { conflictOnce = false; const e = Error('concurrent'); e.code = 'team_stale'; throw e; }
    if (!chains.has(s.team)) chains.set(s.team, []);
    chains.get(s.team).push(s); return null;
  },
};
const teams = browserTeams(engine, realm);
assert.equal((await teams.teams()).current, true);
assert.equal((await teams.view()).teams[0].members.length, 2);
conflictOnce = true;
const renamed = await teams.team({ team: id, op: 'rename', name: 'Renamed' });
assert.equal(renamed.id, id);
assert.equal(renamed.name, 'Renamed');
assert.equal(renamed.seq, 2);
assert.deepEqual(renamed.members, [rosterA.person, rosterB.person].sort());
assert.equal('teams' in renamed, false); // /api/team returns one accepted TeamState, as the daemon does.
assert.equal(calls.filter(([m]) => m === 'PUT').length, 2);
const snap = await teams.teamsSnapshot([id, id]);
assert.equal(snap.sources.length, 1); assert.equal(snap.persons.length, 2);
assert.deepEqual(await store.get('kv', 'unrelated-grant'), before.v);
const oldSnapshot = structuredClone(snap);
await teams.team({ team: id, op: 'rename', name: 'Later' });
assert.deepEqual(snap, oldSnapshot);
failPut = true;
await assert.rejects(teams.team({ team: id, op: 'rename', name: 'Not accepted' }), /lost acceptance/);
assert.equal((await teams.view()).teams[0].name, 'Later'); failPut = false;
duringGet = () => teams.disconnected();
assert.equal((await teams.teams()).current, false);
offline = true;
assert.equal((await teams.teams()).current, false);
assert.equal((await teams.view()).teams[0].name, 'Later');
await assert.rejects(teams.teamsSnapshot([id]), /current/); offline = false;
// Captured transport does not switch if host changes its current Engine reference.
let activeEngine = engine;
const other = { ...engine, call: async () => { throw Error('wrong workspace'); } };
activeEngine = other;
assert.equal((await teams.teams()).current, true);
assert.equal(activeEngine, other);
// Push only queues refs; sync verifies and a newer push invalidates a running query.
const pushed = await engine.call('GET', '/v1/teams');
teams.onTeams(pushed); assert.equal((await teams.view()).current, false);
assert.equal((await teams.sync()).current, true);
duringGet = () => teams.onTeams(pushed);
assert.equal((await teams.teams()).current, false);
assert.equal((await teams.sync()).current, true);
teams.onTeams({ realm_id: realm, teams: [{ id: 'bad' }], truncated: false });
assert.equal((await teams.view()).current, false);
await teams.teams();
// A second team can include the same person; dedup snapshot unions persons.
const id2 = 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa';
const second = await step(rosterA, keys, null, 'create', { name: 'Second' }, id2);
chains.set(id2, [second]);
const union = await teams.teamsSnapshot([id, id2]);
assert.equal(union.sources.length, 2); assert.equal(union.persons.length, 2);
// Unsupported route preserves pinned state and refuses selection.
const originalCall = engine.call;
const missing = browserTeams({ ...engine, call: async () => { const e = Error('unsupported'); e.status = 404; throw e; } }, realm);
assert.equal((await missing.teams()).status, 'unsupported');
assert.equal((await missing.view()).teams.length, 2);
assert.equal(engine.call, originalCall);
// Spoof alternative must not freeze; valid authorized alternative must freeze.
const validAlternative = await step(rosterA, keys, null, 'create', { name: 'Fork' });
chains.set(id, [{ ...validAlternative, sig: wire.b64(new Uint8Array(64)) }]);
await teams.teams(); assert.equal((await teams.view()).teams.find(t => t.id === id).conflict, false);
chains.set(id, [validAlternative]);
await teams.teams(); assert.equal((await teams.view()).teams.find(t => t.id === id).conflict, true);
assert.deepEqual(await store.get('kv', 'unrelated-grant'), before.v);
// A partial snapshot does not prove omission; accepted deletion does and
// remains hidden after reload/offline and replay of a previously valid push.
const beforeDeletion = await engine.call('GET', '/v1/teams');
teams.onTeams({ realm_id: realm, teams: [], truncated: true });
await teams.sync();
assert.ok((await teams.view()).teams.some(t => t.id === id2));
// Avoid the intentionally conflicting first list for this independent proof.
chains.delete(id);
await teams.teams();
await teams.team({ op: 'delete', team: id2 });
assert.ok(!(await teams.view()).teams.some(t => t.id === id2));
offline = true;
const restarted = browserTeams(engine, realm);
assert.ok(!(await restarted.teams()).teams.some(t => t.id === id2));
restarted.onTeams(beforeDeletion); await restarted.sync();
assert.ok(!(await restarted.view()).teams.some(t => t.id === id2));
offline=false;
const tagEngine={...engine,store:memoryStore()},tagBrowser=browserTeams(tagEngine,realm);
await assert.rejects(tagBrowser.team({op:'create',v:2,name:'Reviewers'}),/Update the server/);
tagsSupported=true;
const shared=await tagBrowser.team({op:'create',v:2,name:'Reviewers'});
assert.equal(shared.version,2);assert.deepEqual(shared.members,[]);
await tagBrowser.team({team:shared.id,op:'agent-add',agent:ref});
await tagBrowser.team({team:shared.id,op:'add',target:rosterB.person});
const refreshed=await browserTeams(tagEngine,realm).teams();
assert.equal(refreshed.tags,true);assert.deepEqual(refreshed.teams.find(t=>t.id===shared.id).agents,[ref]);
assert.deepEqual((await tagBrowser.teamsSnapshot([shared.id])).persons.map(p=>p.id),[rosterB.person]);
console.log('PASS teams: Go canonical/hash/signature, real-key authority, transfer/archive, CAS, snapshot isolation, offline/generation and authenticated fork');
