// Signed person teams. One captured Engine; existing workspace kv storage only.
import * as wire from './wire.mjs';
const enc = new TextEncoder();
const ops = new Set(['create', 'join', 'leave', 'rename', 'archive', 'restore', 'remove', 'manager-add', 'manager-remove']);
const fields = new Set(['v', 'realm_id', 'team', 'seq', 'prev', 'author', 'op', 'name', 'target', 'ts', 'sig']);
const fail = message => { throw new Error(message); };

export function validateTeam(s) {
  if (!s || Object.keys(s).some(k => !fields.has(k))) fail('team: unknown field');
  const a = s.author;
  if ((s.name !== undefined && typeof s.name !== 'string') || (s.target !== undefined && typeof s.target !== 'string') ||
      (s.sig !== undefined && typeof s.sig !== 'string' && !(s.sig instanceof Uint8Array))) fail('team: invalid optional field');
  if (s.v !== 1 || !wire.validID(s.realm_id) || !wire.validID(s.team) ||
      !Number.isSafeInteger(s.seq) || s.seq < 0 || !Number.isSafeInteger(s.ts) || s.ts <= 0 ||
      !a || Object.keys(a).some(k => !['person', 'roster', 'address', 'fingerprint'].includes(k)) ||
      !wire.validID(a.person) || !wire.validHash(a.roster) || !wire.validAddress(a.address) ||
      !wire.validFingerprint(a.fingerprint) || !ops.has(s.op)) fail('team: invalid fields');
  if (s.seq === 0 ? s.op !== 'create' || s.prev !== '' : s.op === 'create' || !wire.validHash(s.prev)) fail('team: invalid predecessor');
  if (['create', 'rename'].includes(s.op)) {
    wire.validLabel(s.name);
    if (s.target) fail('team: unexpected target');
  } else if (['remove', 'manager-add', 'manager-remove'].includes(s.op)) {
    if (!wire.validID(s.target) || s.name) fail('team: invalid target');
  } else if (s.name || s.target) fail('team: unexpected name/target');
  if (enc.encode(teamJSON(s, true)).length > 2048) fail('team: too large');
  return s;
}

export function teamJSON(s, signature = false) {
  const q = wire.goString, a = s.author;
  return '{"v":' + s.v + ',"realm_id":' + q(s.realm_id) + ',"team":' + q(s.team) +
    ',"seq":' + s.seq + ',"prev":' + q(s.prev) + ',"author":{"person":' + q(a.person) +
    ',"roster":' + q(a.roster) + ',"address":' + q(a.address) + ',"fingerprint":' + q(a.fingerprint) +
    '},"op":' + q(s.op) + (s.name ? ',"name":' + q(s.name) : '') +
    (s.target ? ',"target":' + q(s.target) : '') + ',"ts":' + s.ts +
    (signature && s.sig ? ',"sig":' + q(typeof s.sig === 'string' ? s.sig : wire.b64(s.sig)) : '') + '}';
}
export function teamCanonical(s) { return enc.encode('agentnet-team-v1\n' + teamJSON(s)); }
export async function teamHash(s) { return wire.hex(await wire.sha256(teamCanonical(s))); }
export async function signTeam(keys, s) {
  validateTeam(s);
  return { ...s, sig: wire.b64(new Uint8Array(await crypto.subtle.sign('Ed25519', keys.sign, teamCanonical(s)))) };
}
export async function applyTeam(s, prev, roster) {
  validateTeam(s);
  if (roster.person !== s.author.person || await wire.rosterHash(roster) !== s.author.roster) fail('team: author roster mismatch');
  const d = await wire.rosterDevice(roster, s.author.fingerprint);
  if (!d || d.address !== s.author.address) fail('team: author not in roster');
  const sig = typeof s.sig === 'string' ? wire.unb64(s.sig) : s.sig;
  if (!sig || sig.length !== 64) fail('team: invalid signature');
  const key = await crypto.subtle.importKey('raw', d.sign_key, 'Ed25519', false, ['verify']);
  if (!await crypto.subtle.verify('Ed25519', key, sig, teamCanonical(s))) fail('team: invalid signature');
  const person = s.author.person;
  let st;
  if (!prev) {
    if (s.seq !== 0) fail('team: missing predecessor');
    st = { realm_id: s.realm_id, id: s.team, name: s.name, managers: [person], members: [person], archived: false };
  } else {
    if (prev.realm_id !== s.realm_id || prev.id !== s.team || s.seq !== prev.seq + 1 || s.prev !== prev.hash) fail('team: wrong predecessor');
    st = structuredClone(prev);
    if (['join', 'leave'].includes(s.op)) {
      if (st.archived) fail('team is archived; a manager must explicitly restore it');
    } else if (!st.managers.includes(person)) fail('this team operation requires a manager');
    const add = (list, p) => { if (!list.includes(p)) list.push(p); };
    const remove = (list, p) => list.filter(x => x !== p);
    switch (s.op) {
      case 'join': add(st.members, person); break;
      case 'leave':
        if (st.managers.includes(person)) fail(st.managers.length === 1 ? 'last manager must explicitly transfer first' : 'remove your manager role explicitly before leaving the team');
        st.members = remove(st.members, person); break;
      case 'rename': st.name = s.name; break;
      case 'archive': st.archived = true; break;
      case 'restore': st.archived = false; break;
      case 'remove':
        if (st.managers.includes(s.target)) fail('remove the manager role explicitly before removing membership');
        st.members = remove(st.members, s.target); break;
      case 'manager-add':
        if (!st.members.includes(s.target)) fail('team manager must be a member');
        add(st.managers, s.target); break;
      case 'manager-remove':
        if (st.managers.includes(s.target) && st.managers.length === 1) fail('last manager must explicitly transfer first');
        st.managers = remove(st.managers, s.target); break;
    }
  }
  if (st.members.length > 1000) fail('team: too many members');
  st.members.sort(); st.managers.sort(); st.seq = s.seq; st.hash = await teamHash(s);
  return st;
}

function directory(d, realm) {
  if (!d || d.realm_id !== realm || !Array.isArray(d.teams) || d.teams.length > 1000 || typeof d.truncated !== 'boolean') fail('team: invalid directory');
  const seen = new Set();
  for (const r of d.teams) {
    if (!wire.validID(r.id) || !wire.validHash(r.hash) || !Number.isSafeInteger(r.seq) || r.seq < 0 || seen.has(r.id)) fail('team: invalid directory ref');
    seen.add(r.id);
  }
  return d;
}

// Existing Engine supplies signed call, pinned person authority, keys and kv.
// realmID is the host's verified workspace realm; never read active workspace later.
export function browserTeams(engine, realmID) {
  if (!wire.validID(realmID)) fail('team: verified realm required');
  const call = engine.call.bind(engine), store = engine.store;
  const storageKey = 'teams/' + realmID;
  let data, generation = 0, current = false, status = 'unknown', reason = '', pending = null;
  let serial = Promise.resolve();
  const exclusive = fn => {
    const result = serial.then(fn);
    serial = result.catch(() => {});
    return result;
  };
  async function load() {
    if (!data) data = await store.get('kv', storageKey) || { chains: {}, conflicts: {}, directory: { realm_id: realmID, teams: [], truncated: false }, at: null };
  }
  async function save(next) {
    await store.write([{ s: 'kv', k: storageKey, v: next }]);
    data = next;
  }
  async function personChain(person) {
    const p = await engine.pinChain(person);
    if (!p || p.state === 'conflict') fail('team: person conflict');
    const records = await engine.chain(person, -1);
    if (!records.length) fail('team: person proof pending');
    await wire.verifyFirst(records[0]);
    for (let i = 1; i < records.length; i++) await wire.verifyNext(records[i], records[i - 1]);
    // Compare every already-pinned historical hash, including the pinned head.
    const hashes = await Promise.all(records.map(wire.rosterHash));
    if (!hashes.includes(p.hash) || (p.hashes || []).some(h => !hashes.includes(h))) fail('team: pinned person fork');
    return { p, records, hashes };
  }
  async function rosterFor(s, cache) {
    let proof = cache.get(s.author.person);
    if (!proof) { proof = await personChain(s.author.person); cache.set(s.author.person, proof); }
    const i = proof.hashes.indexOf(s.author.roster);
    if (i < 0) fail('team: historical author proof pending');
    return proof.records[i];
  }
  async function pin(records, id) {
    await load();
    const next = structuredClone(data), kept = next.chains[id] || [], cache = new Map();
    if (next.conflicts[id]) fail('team: verified fork frozen');
    for (const s of records) {
      if (s.team !== id || s.realm_id !== realmID || s.seq > kept.length) fail('team: chain gap');
      const roster = await rosterFor(s, cache);
      const st = await applyTeam(s, s.seq ? kept[s.seq - 1]?.state : null, roster);
      if (kept[s.seq]) {
        if (kept[s.seq].state.hash !== st.hash) {
          // Only a signed, authorized alternative against pinned predecessor freezes.
          const frozen = structuredClone(data); frozen.conflicts[id] = true;
          await save(frozen); fail('team: verified fork frozen');
        }
      } else kept.push({ step: s, state: st });
    }
    next.chains[id] = kept;
    await save(next);
  }
  async function fetchTeam(id) {
    let after = -1, records = [];
    for (;;) {
      const page = await call('GET', '/v1/teams/' + id + '/chain?after=' + after);
      if (page.realm_id !== realmID || page.team !== id || !Array.isArray(page.records) || page.records.length > 100 || typeof page.more !== 'boolean') fail('team: invalid chain page');
      for (const raw of page.records) {
        const s = typeof raw === 'string' ? JSON.parse(raw) : raw;
        if (s.seq !== records.length) fail('team: noncontiguous chain');
        validateTeam(s); records.push(s);
      }
      if (!page.more) break;
      if (!page.records.length) fail('team: empty continuation');
      after = records.length - 1;
    }
    if (!records.length) fail('team: empty chain');
    await pin(records, id);
  }
  async function accept(d, fresh = false) {
    directory(d, realmID); await load();
    for (const ref of d.teams) {
      const row = data.chains[ref.id]?.[ref.seq];
      if (!row || row.state.hash !== ref.hash) await fetchTeam(ref.id);
      if (data.conflicts[ref.id] || data.chains[ref.id]?.[ref.seq]?.state.hash !== ref.hash) fail('team: directory proof mismatch');
    }
    const next = structuredClone(data);
    // Only a complete authenticated snapshot proves a list was removed.
    if (fresh && !d.truncated) {
      const listed = new Set(d.teams.map(r => r.id));
      next.deleted ||= {};
      for (const id of Object.keys(next.chains)) if (!listed.has(id)) next.deleted[id] = true;
    }
    next.directory = d; next.at = new Date().toISOString(); await save(next);
  }
  async function view() {
    await load();
    const listed = new Set(data.directory.teams.map(r => r.id)), self = engine.me?.person;
    return { realm_id: realmID, status, current, reason, at: data.at, truncated: data.directory.truncated,
      teams: Object.keys(data.chains).filter(id => !data.deleted?.[id]).sort().map(id => {
        const st = data.chains[id].at(-1).state;
        return { ...structuredClone(st), member: st.members.includes(self), manager: st.managers.includes(self), conflict: !!data.conflicts[id], listed: listed.has(id) };
      }) };
  }
  async function refresh() {
    const gen = generation; current = false;
    try {
      await accept(await call('GET', '/v1/teams'), true);
      if (gen === generation && !pending) { current = true; status = 'available'; reason = ''; }
    } catch (e) {
      current = false; status = (Object.values(data?.conflicts || {}).some(Boolean) || /person conflict|pinned person fork|verified fork/.test(e.message)) ? 'conflict' : e.status === 404 ? 'unsupported' : 'unavailable'; reason = e.message;
    }
    return view();
  }
  async function change(c) {
    if (c.op === 'delete') {
      if (!wire.validID(c.team) || c.name || c.target) fail('Invalid people list deletion.');
      const v = await refresh(), st = v.teams.find(t => t.id === c.team && t.listed && !t.conflict);
      if (!v.current || !st) fail('Refresh this people list before deleting it.');
      await call('DELETE', '/v1/teams/' + c.team);
      const next = structuredClone(data); next.deleted ||= {}; next.deleted[c.team] = true;
      await save(next); current = false; await refresh(); return st;
    }
    const id = c.op === 'create' ? wire.newID() : c.team;
    for (let attempt = 0; attempt < 2; attempt++) {
      const v = await refresh();
      if (!v.current) fail('team: current directory required');
      const proof = await personChain(engine.me.person), roster = proof.records.at(-1);
      if (await wire.rosterHash(roster) !== engine.me.hash) fail('team: refresh local self roster first');
      const d = await wire.rosterDevice(roster, engine.fp);
      if (!d || d.address !== engine.address) fail('team: current self device required');
      const prev = c.op === 'create' ? null : data.chains[id]?.at(-1)?.state;
      if (c.op !== 'create' && (!prev || data.conflicts[id])) fail('team: missing or frozen team');
      const s = await signTeam(engine.keys, { v: 1, realm_id: realmID, team: id, seq: prev ? prev.seq + 1 : 0, prev: prev?.hash || '',
        author: { person: roster.person, roster: await wire.rosterHash(roster), address: engine.address, fingerprint: engine.fp },
        op: c.op, ...(c.name ? { name: c.name } : {}), ...(c.target ? { target: c.target } : {}), ts: Math.floor(Date.now() / 1000) });
      const accepted = await applyTeam(s, prev, roster);
      try { await call('PUT', '/v1/team', teamJSON(s, true)); }
      catch (e) {
        current = false;
        if (!attempt && c.op !== 'create' && ['team_stale', 'roster_stale'].includes(e.code)) continue;
        throw e;
      }
      await pin([s], id);
      current = false; await refresh(); return accepted;
    }
  }
  async function snapshot(ids) {
    const v = await refresh();
    if (!v.current || !Array.isArray(ids) || !ids.length || ids.length > 1000) fail('team: current selection required');
    const sources = [], persons = new Set();
    for (const id of [...new Set(ids)].sort()) {
      const st = v.teams.find(t => t.id === id && t.listed && !t.conflict && !t.archived);
      if (!st) fail('team: unavailable selection');
      sources.push({ id, seq: st.seq, hash: st.hash }); st.members.forEach(p => persons.add(p));
    }
    const refs = [];
    for (const id of [...persons].sort()) {
      const proof = await personChain(id), r = proof.records.at(-1);
      refs.push({ id, seq: r.seq, hash: await wire.rosterHash(r) });
    }
    if (!current) fail('team: directory became stale');
    return { realm_id: realmID, sources, persons: refs, at: Math.floor(Date.now() / 1000) };
  }
  function invalidate(message) { generation++; current = false; reason = message; }
  return {
    teams: () => exclusive(refresh), team: c => exclusive(() => change(c)), teamsSnapshot: ids => exclusive(() => snapshot(ids)),
    view: () => exclusive(view),
    connected(supported) { invalidate('connection requires fresh directory'); status = supported ? 'unknown' : 'unsupported'; },
    disconnected() { pending = null; invalidate('offline'); status = 'unavailable'; },
    onTeams(d) {
      invalidate('directory changed');
      try { directory(d, realmID); pending = structuredClone(d); }
      catch (e) { pending = null; status = 'unavailable'; reason = e.message; }
    },
    sync: () => exclusive(async () => {
      if (!pending) return view();
      const d = pending, gen = generation;
      try { await accept(d); if (gen === generation) { pending = null; current = true; status = 'available'; reason = ''; } }
      catch (e) {
        if (gen === generation) {
          current = false; status = /person conflict|pinned person fork|verified fork/.test(e.message) ? 'conflict' : 'unavailable'; reason = e.message;
        }
      }
      return view();
    }),
  };
}
