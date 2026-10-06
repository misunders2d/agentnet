// Shared skin source, copied into each independent package at build time.
// Host API v1 only: no provider, storage, transport or global document access.
export const TOPIC_UI = Object.freeze({ barMax: 4, pageSize: 50, undoDelay: 6000 });
export function topicControls(root, {api, choose, fresh, changed, announce}) {
 const doc=root.ownerDocument, bar=doc.createElement('nav');bar.className='topics-bar';bar.setAttribute('aria-label','Topics');
 const zoom=root.querySelector('#zoom');if(zoom)zoom.before(bar);else root.querySelector('.conv-head').after(bar);
 const node=(tag,text,props={})=>{const n=doc.createElement(tag);if(text)n.textContent=text;Object.assign(n,props);return n;};
 const button=(text,fn)=>node('button',text,{type:'button',onclick:fn});
 let scope={},topics=[],current='',all=null,timer=null,seq=0,dead=false;
 const request=(what,ids,counts)=>api('/api/topic/'+what,{...scope,id:'',ids,counts});
 function close(){if(timer)clearTimeout(timer);timer=null;all?.remove();all=null;seq++;}
 async function list() {
  close();const version=seq;
  all=node('section','',{className:'topics-list'});all.setAttribute('role','dialog');all.setAttribute('aria-modal','true');all.setAttribute('aria-label','All topics');
  const restore=root.getRootNode().activeElement, siblings=[...root.children].filter(n=>!n.inert);
  // Modal ownership stays within the skin root and restores focus on close.
  const popup=all;
  const finish=()=>{close();for(const n of siblings)n.inert=false;bar.inert=false;restore?.focus?.();};
  popup.append(node('h2','All topics'),button('Close',finish));
  const search=node('input','',{type:'search',placeholder:'Search topics'});search.setAttribute('aria-label','Search topics');popup.append(search);
  const filters=node('div','',{className:'topics-actions'}),rows=node('div','',{className:'topics-rows'}),actions=node('div','',{className:'topics-actions'}),status=node('p','',{className:'hint'});status.setAttribute('role','status');
  popup.append(filters,actions,status,rows,node('p',scope.conv?'Done and Reopen are shared. Delete for me leaves others’ copies.':'Done and Archive stay on this device. Delete for me leaves others’ copies.',{className:'hint'}));
  root.append(popup);for(const n of siblings)n.inert=true;bar.inert=true;
  let filter='active',selected=new Set(),counts=new Map(),items=[],next='',busy=false,confirming=false;
  const render=()=>{
   filters.replaceChildren(...['active','done','archived'].map(f=>{const b=button(f[0].toUpperCase()+f.slice(1),()=>{filter=f;void load();});b.setAttribute('aria-pressed',String(f===filter));return b;}));
   rows.replaceChildren(...items.map(t=>{const row=node('div','',{className:'topic-row'}),check=node('input','',{type:'checkbox',checked:selected.has(t.id),disabled:busy||confirming});check.setAttribute('aria-label','Select '+(t.title||'Untitled topic'));check.onchange=()=>{counts.set(t.id,t.count);check.checked?selected.add(t.id):selected.delete(t.id);renderActions();};const go=button((t.title||'Untitled topic')+' · '+t.state,()=>{finish();choose(t.id);});row.append(check,go,node('small',t.last||''));return row;}));
   if(next)rows.append(button('Show more',()=>void load(true)));if(!items.length)rows.append(node('p','No '+filter+' topics.',{className:'hint'}));renderActions();
  };
  const renderActions=()=>{if(confirming)return;actions.replaceChildren();if(!selected.size)return;actions.append(node('span',selected.size+' selected'));for(const [what,label]of [['delete','Delete for me'],['done','Mark done'],['archive','Archive']]){const b=button(label,()=>confirm(what,label));b.disabled=busy;actions.append(b);}};
  const confirm=(what,label)=>{
   confirming=true;const ids=[...selected],covered=Object.fromEntries(ids.map(id=>[id,counts.get(id)]));actions.replaceChildren(node('p',label+' '+ids.length+' topics? '+(what==='delete'?'Other people keep their copies. People chats: your devices; agent chats: this device.':what==='done'&&scope.conv?'Shared with everyone.':'On this device.')));
   const cancel=()=>{if(timer)clearTimeout(timer);timer=null;confirming=false;status.textContent='Cancelled; nothing changed.';render();};
   actions.append(button('Cancel',cancel),button('Confirm',()=>{
    actions.replaceChildren(button('Undo',cancel));status.textContent='Will apply in six seconds.';
    timer=setTimeout(async()=>{timer=null;busy=true;status.textContent='Applying…';actions.replaceChildren();try{const r=await request(what,ids,covered);if(dead||all!==popup)return;selected.clear();announce(r.note);await changed();status.textContent=r.note;confirming=false;await load();}catch(e){if(all===popup){status.textContent=e.message;confirming=false;}}finally{busy=false;if(all===popup)render();}},TOPIC_UI.undoDelay);
   }));
  };
  const load=async(more=false)=>{status.textContent='Loading…';try{const q=new URLSearchParams({...scope,state:filter,q:search.value,limit:String(TOPIC_UI.pageSize),...(more?{before:next}:{})});const p=await api('/api/topics?'+q);if(dead||seq!==version||all!==popup)return;items=more?[...items,...p.topics]:p.topics;next=p.next||'';status.textContent='';render();}catch(e){if(all===popup)status.textContent=e.message;}};
  search.oninput=()=>void load();popup.addEventListener('keydown',e=>{if(e.key==='Escape'){e.preventDefault();finish();}if(e.key==='Tab'){const focus=[...popup.querySelectorAll('button,input')].filter(n=>!n.disabled),i=focus.indexOf(root.getRootNode().activeElement);if(e.shiftKey&&i===0){e.preventDefault();focus.at(-1)?.focus();}else if(!e.shiftKey&&i===focus.length-1){e.preventDefault();focus[0]?.focus();}}});
  popup.cleanup=()=>{for(const n of siblings)n.inert=false;bar.inert=false;};
  popup.querySelector('button').focus();await load();
 }
 const oldClose=close;close=()=>{all?.cleanup?.();oldClose();};
 function update(nextScope,nextTopics,id) {
  if(JSON.stringify(scope)!==JSON.stringify(nextScope))close();scope=nextScope;topics=nextTopics||[];current=id||'';
  bar.replaceChildren();if(scope.conv){const main=button(root.clientWidth<700?'Main':'Main flow',()=>choose(''));main.setAttribute('aria-pressed',String(!current));bar.append(main);}
  const picked=topics.filter(t=>t.state==='active'||t.id===current).slice(0,TOPIC_UI.barMax);
  const opened=topics.find(t=>t.id===current);if(opened&&!picked.includes(opened))picked[picked.length-1]=opened;
  for(const t of picked){const b=button((t.title||'Untitled').slice(0,40),()=>choose(t.id));b.title=t.title+' · '+t.state;b.setAttribute('aria-pressed',String(current===t.id));b.className='topic-chip';bar.append(b);}
  bar.append(button(root.clientWidth<700?'All '+topics.length:'All topics ('+topics.length+')',()=>void list()),Object.assign(button(root.clientWidth<700?'+ New':'New topic',fresh),{ariaLabel:'New topic'}));
  const t=topics.find(t=>t.id===current);if(t){bar.append(button(t.state==='active'?(root.clientWidth<700?'Done':'Mark done'):'Reopen',async()=>{try{const r=await api('/api/topic/'+(t.state==='active'?'done':'reopen'),{...scope,id:t.id,count:t.count});announce(r.note);await changed();}catch(e){announce(e.message);}}));}
 }
 return {update,show(visible){bar.hidden=!visible;},stop(){dead=true;close();bar.remove();}};
}
