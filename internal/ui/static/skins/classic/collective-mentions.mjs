// Tags are shortcuts to the existing exact-recipient mentions. Resolve only
// against the current conversation's eligible people/agents. The composer
// inserts these recipients visibly and stores the ordinary encoded mentions,
// so later list edits or retries cannot silently change whom a draft addresses.
export function collectiveOptions(people, view) {
  const unique = list => { const seen=new Set(); return list.filter(p=>{ const key=p.kind==='agent'?'agent/'+p.id:'person/'+(p.person||p.id); if(seen.has(key))return false; seen.add(key); return true; }); };
  const all = unique(people), out = [];
  const option = (id, name, targets, skipped=0) => ({
    key: 'collective/'+id, kind: 'collective', id, name, seed: id, targets,
    sub: targets.length+' here'+(skipped?' · '+skipped+' outside this chat':'')+' · expands to names',
  });
  if (all.length) out.push(option('everyone', 'everyone', all));
  if (!view?.current) return out;
  for (const team of view.teams || []) {
    if (!team.listed || team.conflict || team.archived) continue;
    const people = new Set(team.members || []), agents = team.agents || [];
    const targets = unique(all.filter(p => p.kind === 'agent'
      ? agents.some(a => a.id === p.agentID && a.host === p.host && a.host_key === p.hostKey)
      : people.has(p.person || p.id)));
    if (targets.length) out.push(option(team.id, team.name, targets, Math.max(0,people.size+agents.length-targets.length)));
  }
  return out;
}
