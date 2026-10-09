// Local reviewed history contribution. The wire is an ordinary quoted message;
// no content marker grants authority, mirrors source history or runs old work.
import * as wire from './wire.mjs';
const changed='The selected history or destination audience changed; review the contribution again.';
const quote=s=>'> '+String(s||'').replaceAll('\r\n','\n').replaceAll('\n','\n> ');
export function contributionText(items,importer,at,source) {
 let body=`Shared by ${importer} at ${at} · quoted context from another chat.\n\nOriginal speakers and times below are attributed by the person sharing this snapshot. Source tasks and permissions stay in the source chat.\n`;
 for(const [i,m] of items.entries()) {
  body+=`\n${i+1}. ${m.label||m.from} — ${m.sent_at}\n\n${quote(m.body)}\n\n[Open source message](agentnet:message/${m.id}?conv=${source})\n`;
  if(m.selected_parent)body+=`\nReply to selected item ${m.selected_parent}.\n`;else if(m.reply_to)body+='\nReply outside the selected history; parent not shared.\n';
  for(const f of m.files||[])if(f.selected)body+=`\nAttached selected file: ${wire.safeName(f.name)} (${f.size} bytes).\n`;
  const omitted=(m.files||[]).filter(f=>!f.selected).length;if(omitted)body+=`\n${omitted} source file(s) omitted.\n`;
 }
 return body;
}
export async function contributionOperation(review) {
 return wire.hex(await wire.sha256(new TextEncoder().encode(JSON.stringify({...review,operation:''})))).slice(0,32);
}
function orderMessages(messages) {
 const aliases=new Map();for(const m of messages){aliases.set(m.id,m.lid);aliases.set(m.lid,m.lid);for(const c of m.copies||[])aliases.set(c.id,m.lid);}
 const ordered=[...messages].sort((a,b)=>a.at-b.at||String(a.key||a.fp||a.claimed_key||'').localeCompare(String(b.key||b.fp||b.claimed_key||''))||a.lid.localeCompare(b.lid)),out=[],done=new Set();
 while(out.length<ordered.length){const next=ordered.find(m=>!done.has(m.lid)&&(!aliases.has(m.reply_to)||done.has(aliases.get(m.reply_to))));if(!next)throw Error('Selected reply relationships form a cycle; choose fewer messages.');out.push(next);done.add(next.lid);}return out;
}
export async function contributionPreview(e,input) {
 const r={source:input.source,ids:input.ids,destination:input.destination,...(input.topic?{topic:input.topic}:{}),...(input.new_topic?{new_topic:true}:{}),...(input.title?{title:wire.topicTitle(input.title)}:{}),...(input.files?.length?{files:input.files}: {})};
 if(!wire.validHash(r.source)||!wire.validHash(r.destination)||r.source===r.destination||!Array.isArray(r.ids)||!r.ids.length||r.ids.length>64||r.ids.some(id=>!wire.validID(id))||new Set(r.ids).size!==r.ids.length||r.topic&&!wire.validID(r.topic)||r.new_topic&&(!r.topic||!r.title)||[...(r.title||'')].length>120||(r.files||[]).length>8)throw Error('Choose at most 64 messages, 8 files and a different existing group in this workspace.');
 const checks=[],source=await e.groupRead(checks,'convs',r.source)||await e.groupRecord(r.source),destination=await e.groupRead(checks,'convs',r.destination)||await e.groupRecord(r.destination);
 if(!source||!destination||destination.kind!=='group')throw Error('Choose an existing group in this workspace.');
 const sourcePeople=await e.topicOrganizationAuthor(source,e.address,e.fp,checks),destinationPeople=await e.topicOrganizationAuthor(destination,e.address,e.fp,checks);
 for(const p of sourcePeople)for(const d of p.devices||[])if(d.address!==e.address)await e.groupRead(checks,'pins',d.address);
 const events=await e.convEvents(r.destination,checks),members=await e.dmMembers(destination,events,checks),audience=[];
 for(const p of destinationPeople)for(const d of p.devices||[])audience.push({person:p.person,address:d.address,fingerprint:d.fingerprint,label:p.label,role:'member'});
 let audience_pending=false;
 for(const pid of new Set(events.map(x=>x.e.pid))) {
  const p=e.resolveAgent(pid,events,members);if(p.state==='invited')audience_pending=true;
  if(p.state==='active'&&(['human'].includes(p.role)||p.audience==='room')&&p.held)throw Error('Destination audience proof is pending; review after recovery.');
  if(p.state!=='active'||p.held||!(p.role==='human'||p.audience==='room')||p.topic!=null&&p.topic!==(r.topic||''))continue;
  audience.push({person:p.host.person,address:p.host.address,fingerprint:p.host.fingerprint,label:p.role==='human'?p.host.label:`Agent ${(p.agent_id||p.pid).slice(0,12)} at ${p.host.label}`,role:p.role==='human'?'guest':'agent',pid:p.pid,...(p.agent_id?{agent_id:p.agent_id}:{}),...(p.topic!=null?{topic:p.topic}:{})});
 }
 audience.sort((a,b)=>(a.person+'/'+a.address+'/'+a.role+'/'+(a.pid||'')).localeCompare(b.person+'/'+b.address+'/'+b.role+'/'+(b.pid||'')));
 for(const p of audience){if(p.address===e.address)continue;const pin=await e.groupRead(checks,'pins',p.address);if(!pin||pin.pending||pin.fingerprint!==p.fingerprint)throw Error('Destination recipient key changed.');}
 const inbox=await e.store.all('inbox'),outbox=await e.store.all('outbox'),summary=await e.chatTopicSummaries(r.destination),topics=summary.topics,target=r.topic?topics.find(t=>t.id===r.topic):summary.main_topic;
 audience_pending ||= (await e.store.prefix('kv','group-invitation/')).some(i=>i?.proposal?.state?.conv===r.destination&&['pending','accepted'].includes(i.status));
 if(r.new_topic?!!target:r.topic?!target||target.state!=='active':target&&target.state!=='active')throw Error('Choose an active destination topic, or reopen it first.');
 await e.groupRead(checks,'kv','topic/'+r.destination+'/'+(r.topic||''));
 if(!r.topic){const scope=wire.hex(await wire.sha256(new TextEncoder().encode('agentnet/main-flow-preference/v1\0'+r.destination)));await e.groupRead(checks,'kv','topic/'+scope+'/'+'0'.repeat(32));}
 const sourceEvents=await e.convEvents(r.source,checks),sourceMembers=await e.dmMembers(source,sourceEvents,checks);
 const messages=await e.convMessages(r.source,inbox,outbox),selected=r.ids.map(id=>{const found=messages.filter(m=>m.id===id||m.lid===id);if(found.length!==1||found[0].sub||found[0].topic_event||found[0].excerpt_pid)throw Error('Selected message unavailable or ambiguous.');return found[0];});
 const items=[],used=new Set(),ids=[...r.ids];
 for(const m of orderMessages(selected)) {
  const author=m.key||m.fp||m.claimed_key||e.fp;if(!wire.validFingerprint(author)||await e.groupRead(checks,'erased',r.source+'|'+author+'|'+m.lid))throw Error('Selected message deleted.');
  const controls=[...inbox,...outbox].filter(x=>x.conv===r.source&&x.control&&x.ref?.id===m.lid&&x.ref.fingerprint===author),person=await e.personOfFp(author),state=e.controlsOn(controls,person,x=>x.person||'',()=>'',()=>false);
  if(state.deleted)throw Error('Selected message deleted.');const text=state.edited?state.text:m.body||'';
  const item={id:m.id,lid:m.lid,author,hash:await wire.groupHistoryContentHash(r.source,{...m,body:text}),from:m.from||e.address,sent_at:new Date((m.ts||Math.floor(m.at/1000))*1000).toISOString(),body:text,...(m.reply_to?{reply_to:m.reply_to}:{}),files:[]};
  const speaker=[e.me,...await e.store.all('persons')].find(p=>p?.devices?.some(d=>d.address===item.from&&d.fingerprint===author));
  item.label=speaker?.label||item.from;
  if(m.agent_id||String(m.origin||'').startsWith('agent:')){const agent=(m.agent_id||m.origin.slice(6)).slice(0,12),pid=m.human&&wire.agentAuthor(m.human)?m.human.author_pid:m.pid,info=pid?e.resolveAgent(pid,sourceEvents,sourceMembers):null,verified=!!m.pid&&!!info?.invite&&!!info.host&&info.state!=='conflict'&&info.role!=='human'&&item.from===info.host.address&&author===info.host.fingerprint;item.label=`${verified?'Agent':'Claimed agent'} ${agent}, ${m.kind}; host person: ${item.label} (${item.from})`;}
  else if(item.label!==item.from)item.label+=` (${item.from})`;
  for(const [index,f] of (m.attachments||[]).entries()) {
   const refs=(r.files||[]).map((v,j)=>({v,j})).filter(({v})=>(v.id===m.id||v.id===m.lid)&&v.index===index);if(refs.length>1)throw Error('Duplicate selected file.');if(refs.length)used.add(refs[0].j);
   const local=!m.fp&&!m.history&&!m.replica,key=local?'kept/'+f.sha256:f.blob?'ct/'+f.blob.id:'',kept=key?await e.groupRead(checks,'files',key):null,available=!!kept,chosen=refs.length>0;
   if(chosen&&(!available||f.size>wire.BrowserMaxFile))throw Error('Selected file bytes are unavailable or exceed this browser limit; open or request the exact file first.');
   item.files.push({index,name:wire.safeName(f.name),size:f.size,sha256:f.sha256,available,selected:chosen});
  }
  items.push(item);ids.push(m.lid);
 }
 if(used.size!==(r.files||[]).length)throw Error('Selected file is outside the reviewed messages.');
 if(items.flatMap(m=>m.files).filter(f=>f.selected).reduce((sum,f)=>sum+f.size,0)>wire.BrowserMaxMessage)throw Error('Selected files exceed this browser message limit; choose fewer files.');
 for(const m of items){const parent=items.findIndex(p=>p.lid===m.reply_to||p.id===m.reply_to);if(parent>=0)m.selected_parent=parent+1;}
 await e.authorityRows({conv:r.source,organization:true,ids:[...new Set(ids)].sort()},checks);
 await e.authorityRows({conv:r.destination},checks);
 const token=wire.hex(await wire.sha256(new TextEncoder().encode(JSON.stringify([r,items,audience,audience_pending,checks])))),imported_at=new Date(e.now()).toISOString();
 const review={...r,operation:'',token,body:contributionText(items,e.address,imported_at,r.source),items,audience,imported_at,audience_pending};
 if(new TextEncoder().encode(review.body).length>(128<<10))throw Error('Selected quoted history is larger than 128 KiB; choose fewer messages.');
 review.operation=await contributionOperation(review);
 if(new TextEncoder().encode(JSON.stringify(review)).length>(48<<10))throw Error('Selected history review is larger than 48 KiB; choose fewer messages or files.');
 return {review,checks,destination};
}
function storedContributionMatches(r,m) {
 const selected=r.items.flatMap(item=>(item.files||[]).filter(f=>f.selected)),files=m.attachments||[];
 return m.kind==='message'&&!m.sub&&!m.pid&&!m.target&&!m.reply_to&&!m.agent_id&&m.body===r.body&&(m.topic||'')===(r.topic||'')&&selected.length===files.length&&selected.every((f,i)=>f.sha256===files[i].sha256&&f.size===files[i].size&&f.name===wire.safeName(files[i].name));
}
async function keptContribution(e,r) {
 const rows=(await e.store.all('outbox')).filter(x=>x.conv===r.destination&&x.lid===r.operation&&!x.aside&&!x.forwarded);
 if(rows.length){if(rows.some(x=>!storedContributionMatches(r,x)))throw Error(changed);const least=rows.reduce((a,b)=>['queued','waiting','receiver_waiting'].includes(b.state)?b:a);return {id:rows[0].id,lid:r.operation,state:least.state,copies:rows.map(x=>({id:x.id,to:x.to,state:x.state,detail:x.detail}))};}
 const own=await e.store.get('kv','person'),m=(await e.store.all('inbox')).find(x=>x.conv===r.destination&&x.lid===r.operation&&own?.devices.some(d=>d.fingerprint===(x.fp||x.claimed_key)));
 if(m){if(!storedContributionMatches(r,m))throw Error(changed);return {id:m.id,lid:r.operation,state:'stored'};}return null;
}
export async function contributionRecheck(e,r) {
 let fresh;try{fresh=await contributionPreview(e,r);}catch{throw Object.assign(Error(changed),{code:'history_review_changed'});}
 const current={...fresh.review,imported_at:r.imported_at,body:contributionText(fresh.review.items,e.address,r.imported_at,r.source)};current.operation=await contributionOperation(current);
 if(current.operation!==r.operation)throw Error(changed);return fresh;
}
export async function contributionApply(e,r) {
 if(!wire.validHash(r.token)||!wire.validID(r.operation)||r.operation!==await contributionOperation(r))throw Error(changed);
 const saved=await keptContribution(e,r);if(saved)return saved;
 const fresh=await contributionRecheck(e,r),files=[];
 for(const p of r.audience)if(p.address!==e.address)await e.groupSupport(p.address,await e.pinned(p.address));
 for(const item of r.items)for(const f of item.files||[])if(f.selected){const rows=await e.convMessages(r.source,await e.store.all('inbox'),await e.store.all('outbox')),m=rows.find(m=>m.id===item.id),att=m?.attachments?.[f.index];if(!att||att.sha256!==f.sha256||att.size!==f.size)throw Error(changed);const local=!m.fp&&!m.history&&!m.replica,kept=await e.store.get('files',local?'kept/'+f.sha256:'ct/'+att.blob?.id);if(!kept)throw Error(changed);const bytes=await wire.decryptFile(kept.ct,local?kept.attachment:att,e.keys);if(bytes.length!==f.size||wire.hex(await wire.sha256(bytes))!==f.sha256)throw Error(changed);files.push({name:f.name,size:bytes.length,bytes});}
 const outgoing={id:r.operation,queued:true,kind:'message',origin:'ui',body:r.body,topic:r.topic||'',files,contribution_review:r};
 try {return await e.sendGroupTurn(fresh.destination,outgoing);}catch(error){const saved=await keptContribution(e,r);if(saved)return saved;throw error;}
}
