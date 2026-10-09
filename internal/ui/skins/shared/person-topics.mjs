// Presentation only. Every entry retains its signed root and native topic route.
// Names never combine identities; guest-only rooms stay outside a person's chat.
export function personRoots(overview, dm) {
  const person = dm?.peer?.person;
  if (!person || dm.kind === 'group' || dm.role && dm.role !== 'member') return [];
  return (overview?.dms || []).filter(d => d.peer?.person === person && d.kind !== 'group' && (!d.role || d.role === 'member'));
}
export function orderedPersonRoots(roots) {
  return [...roots].sort((a,b) => (a.created || '').localeCompare(b.created || '') || a.id.localeCompare(b.id));
}
// Initial Main uses immutable creation order and the populated preference. Pin its exact
// root in the persisted workspace preference; sync/activity never reroutes it.
export function personMainRoot(roots) {
  const ranked=orderedPersonRoots(roots);
  return (ranked.find(r=>r.count>0) || ranked[0])?.id || '';
}
export function mainPreferences(workspace, storage) {
  const key='agentnet.messenger.mains.'+workspace;
  let saved={};
  try { saved=JSON.parse(storage()?.getItem(key) || '{}'); } catch { /* UI preference only */ }
  return (roots,person)=>{
    const eligible=roots.filter(r=>r.peer?.person===person&&r.kind!=='group'&&(!r.role||r.role==='member'));
    if(!person||!eligible.length)return '';
    let id=saved?.[person];
    if(typeof id!=='string'||!eligible.some(r=>r.id===id)) {
      id=personMainRoot(eligible);
      saved={...saved,[person]:id};
      try { storage()?.setItem(key,JSON.stringify(saved)); } catch { /* UI preference only */ }
    }
    return id;
  };
}
export function personTopicEntries(roots, views, main = '') {
  const entries=[];
  for(const root of orderedPersonRoots(roots)) {
    const view=views[root.id], topics=view?.topics || [], messages=view?.messages?.filter(m=>!m.topic && !m.topic_event && !m.event);
    const count=messages ? messages.length : root.count;
    const text=m=>m?.deleted ? 'Message deleted' : m?.edited ? m.text || '' : m?.body || '';
    const body=messages ? text(messages[0]) : undefined;
    const title=view?.main_topic?.title || (count ? body || root.title || 'Untitled topic' : 'Empty topic');
    if(root.id !== main && (!view || count > 0 || !topics.length)) entries.push({key:root.id+':',conv:root.id,topic:'',title,last:messages ? text(messages.at(-1)) : root.last || '',count,
      unread:messages ? messages.filter(m=>m.unread).length : Math.max(0,(root.unread || 0)-topics.reduce((n,t)=>n+(t.unread || 0),0)),state:view?.main_topic?.state || 'active',guests:root.guests || 0,root,main:view?.main_topic});
    for(const native of topics) entries.push({key:root.id+':'+native.id,conv:root.id,topic:native.id,title:native.title || 'Untitled topic',last:native.last || '',count:native.count,
      unread:native.unread || 0,state:native.state || 'active',guests:root.guests || 0,root,native});
  }
  return entries;
}
