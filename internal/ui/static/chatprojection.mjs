// Saved-row queries and presentation only. Admission and actions retain their
// own current reads and atomic write guards in the engine.
import { deviceHistoryDirection, deviceHistoryPeer } from './devicehistory.mjs';

export function threadPeer(row, store) {
  if (!row || row.conv || !(row.v === 1 || row.control)) return '';
  return deviceHistoryPeer(row, store === 'inbox' ? row.from : row.to) || '';
}

export function threadIndexed(row, store) {
  const value = { ...row }, peer = threadPeer(row, store);
  if (peer) value.thread_peer = peer;
  else delete value.thread_peer;
  return value;
}

// Original execution identity only. Personal display topics never select an
// executor, and an empty legacy agent ID is distinct from every concrete ID.
export function directDestination(row) {
  if(row.destination)return row.destination;
  if(row.target?.address&&row.target?.fingerprint)return {host:row.target.address,fingerprint:row.target.fingerprint,agent_id:row.target.agent_id||""};
  if(["question","task"].includes(row.kind))return row.original_recipient?{host:row.original_recipient,fingerprint:row.original_recipient_key||"",agent_id:""}:null;
  if(["answer","result"].includes(row.kind)||row.status==="progress")return row.from&&row.author_fp?{host:row.from,fingerprint:row.author_fp,agent_id:row.agent_id||""}:null;
  if(!row.reply_to&&row.original_recipient&&row.original_recipient_key)return {host:row.original_recipient,fingerprint:row.original_recipient_key,agent_id:""};
  return null;
}

export async function readThreadRows(store, id) {
  const [incoming, outgoing] = await Promise.all([store.get('inbox', id), store.get('outbox', id)]);
  const peer = threadPeer(incoming, 'inbox') || threadPeer(outgoing, 'outbox');
  if (!peer) return { inbox: [], outbox: [] };
  const [inbox, outbox] = await Promise.all([store.byThreadPeer('inbox', peer), store.byThreadPeer('outbox', peer)]);
  return { inbox, outbox };
}

export function projectDeviceThreads({ inbox, outbox }, { address, fingerprint, erased, placements=[] }) {
  const msgs = [
    ...inbox.filter(m => m.v === 1 && !m.aside && !erased(m)).map(m => ({ ...m, dir: deviceHistoryDirection(m, 'in'), peer: threadPeer(m, 'inbox'), author_fp: m.fp, stored_in: true })),
    ...outbox.filter(m => m.v === 1 && !m.aside && !erased(m)).map(m => ({ ...m, dir: deviceHistoryDirection(m, 'out', m.from || address), peer: threadPeer(m, 'outbox'), author_fp: fingerprint })),
  ];
  const byID = new Map(msgs.map(m => [m.id, m])), parent = new Map(msgs.map(m => [m.id, m.id]));
  for(const m of msgs){const meta=m.device_history;m.from ||= m.stored_in?m.peer:address;m.original_recipient=meta?.recipient||(m.stored_in?address:m.to);m.original_recipient_key=meta?.recipient_key||(m.stored_in?fingerprint:m.recipient_fp||m.fp);m.peer_key=m.stored_in?m.author_fp:m.recipient_fp||m.fp;m.destination=directDestination(m);}
  for(const m of msgs){if(!m.destination?.host||m.destination.fingerprint)continue;const keys=new Set(msgs.filter(child=>child.reply_to===m.id&&(["answer","result"].includes(child.kind)||child.status==="progress")&&child.destination?.host===m.destination.host&&child.destination.agent_id===m.destination.agent_id&&child.destination.fingerprint).map(child=>child.destination.fingerprint));if(keys.size===1)m.destination={...m.destination,fingerprint:[...keys][0]};}
  const resolve=(m,seen=new Set())=>{if(!m||seen.has(m.id))return null;seen.add(m.id);return m.destination||=(resolve(byID.get(m.reply_to),seen));};
  for(const m of msgs)resolve(m);
  const find = x => {
    while (parent.get(x) !== x) { parent.set(x, parent.get(parent.get(x))); x = parent.get(x); }
    return x;
  };
  for (const m of msgs) {
    const reply = m.reply_to && byID.get(m.reply_to);
    if (reply && reply.peer === m.peer && JSON.stringify(m.destination)===JSON.stringify(reply.destination)) parent.set(find(m.id), find(reply.id));
  }
  const groups = new Map();
  // Self-delivery retains both transport copies. Present the sent row once,
  // as the native client does; its ID also reconciles optimistic send previews.
  const selfDelivered = m => m.stored_in && outbox.some(o => o.id === m.id && (m.author_fp === o.author_fp || m.from === (o.from || address) || m.fp === fingerprint));
  // Self-delivery retains both transport copies. Present the sent row once,
  // as the native client does; its ID also reconciles optimistic send previews.
  for (const m of msgs) {
    if (selfDelivered(m)) continue;
    const root = find(m.id);
    let group = groups.get(root);
    if (!group) groups.set(root, group = []);
    group.push(m);
  }
  const chronological=(a,b)=>a.at-b.at||(a.id<b.id?-1:1);
  const original=[...groups.values()].map(g=>g.sort(chronological)),held=new Map(),byOriginal=new Map(),dirty=new Set();
  for(const g of original)for(const m of g){m.base_topic=m.display_topic=g[0].id;held.set(m.peer+"/"+m.id,m);byOriginal.set(m.author_fp+"/"+m.id,m);}
  const same=(a,b)=>!!a&&!!b&&a.host===b.host&&a.fingerprint===b.fingerprint&&(a.agent_id||'')===(b.agent_id||''),synthetic=new Map(),conflicts=new Set();
  for(const p of placements){const m=byOriginal.get(p.author+"/"+p.id);if(!m||!same(m.destination,p.destination)||!p.synthetic)continue;const k=m.peer+"/"+p.topic;if(synthetic.has(k)&&!same(synthetic.get(k),p.destination))conflicts.add(k);synthetic.set(k,p.destination);}
  for(const p of placements){const m=byOriginal.get(p.author+"/"+p.id);if(!m||!same(m.destination,p.destination))continue;const k=m.peer+"/"+p.topic,destination=held.get(k);if(conflicts.has(k)||(p.synthetic?!!destination:!destination||!same(destination.destination,p.destination)))continue;dirty.add(m.peer+"/"+m.display_topic);dirty.add(k);m.display_topic=p.topic;}
  const assigned=new Map();for(const g of original)for(const m of g){const k=m.peer+"/"+m.display_topic;if(!assigned.has(k))assigned.set(k,[]);assigned.get(k).push(m);}
  return [...assigned.values()].map(g=>{g.sort(chronological);const k=g[0].peer+"/"+g[0].display_topic,root=held.get(k),topic=synthetic.has(k)||root?.display_topic===g[0].display_topic?g[0].display_topic:g[0].id;for(const m of g){m.organized=dirty.has(m.peer+"/"+m.display_topic);m.display_topic=topic;}return g;});
}

export async function readChatView(store, { me, fingerprint }, conv = null) {
  const [incoming, outgoing, persons, convs, groups, reports, decisions] = await Promise.all([
    conv === null ? store.all('inbox') : store.byConv('inbox', conv),
    conv === null ? store.all('outbox') : store.byConv('outbox', conv),
    store.all('persons'), store.all('convs'), store.prefix('kv', 'group/'),
    conv === null ? [] : store.inboxStatus('review_notice'),
    conv === null ? [] : store.outboxSub('decision'),
  ]);
  const unique = rows => [...new Map(rows.map(r => [r.id, r])).values()];
  const inbox = reports.length ? unique([...incoming, ...reports]) : incoming;
  const outbox = decisions.length ? unique([...outgoing, ...decisions]) : outgoing;
  const byConv = { inbox: new Map(), outbox: new Map() }, fpPersons = new Map();
  for (const [name, rows] of [['inbox', inbox], ['outbox', outbox]]) for (const row of rows) {
    let selected = byConv[name].get(row.conv);
    if (!selected) byConv[name].set(row.conv, selected = []);
    selected.push(row);
  }
  for (const person of [me, ...persons].filter(Boolean)) for (const device of person.known || person.devices || [])
    if (!fpPersons.has(device.fingerprint)) fpPersons.set(device.fingerprint, person.person);
  if (me) fpPersons.set(fingerprint, me.person);
  return { inbox, outbox, persons, convs, groups, byConv, fpPersons, parts: new Map() };
}
