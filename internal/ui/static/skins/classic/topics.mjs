import {personTopicEntries} from './person-topics.mjs';
// Shared skin source, copied into each independent package at build time.
// Host API v1 only: no provider, storage, transport or global document access.
export const TOPIC_UI = Object.freeze({ barMax: 4, pageSize: 50, undoDelay: 6000 });
export function topicControls(root, {api, choose, chooseRoot, fresh, changed, announce}) {
 const doc=root.ownerDocument, bar=doc.createElement('nav');bar.className='topics-bar';bar.setAttribute('aria-label','Topics');
 const zoom=root.querySelector('#zoom');if(zoom)zoom.before(bar);else root.querySelector('.conv-head').after(bar);
 const node=(tag,text,props={})=>{const n=doc.createElement(tag);if(text)n.textContent=text;Object.assign(n,props);return n;};
 const button=(text,fn)=>node('button',text,{type:'button',onclick:fn});
 let scope={},topics=[],current='',all=null,timer=null,seq=0,dead=false,person=null,views={},personStamp='',personSeq=0,personReady=false,refreshList=null;
 const request=async(what,ids,counts)=>{
  if(!person)return api('/api/topic/'+what,{...scope,id:'',ids,counts});
  const selected=personTopicEntries(person.roots,views,person.main).filter(t=>ids.includes(t.key)&&t.native),groups=new Map();
  for(const t of selected){const group=groups.get(t.conv)||{ids:[],counts:{}};group.ids.push(t.topic);group.counts[t.topic]=counts[t.key];groups.set(t.conv,group);}
  const results=await Promise.all([...groups].map(([conv,g])=>api('/api/topic/'+what,{conv,id:'',...g})));
  return {note:results.map(r=>r.note).filter(Boolean).join(' ')};
 };
 function close(){if(timer)clearTimeout(timer);timer=null;all?.remove();all=null;refreshList=null;seq++;}
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
  popup.append(filters,actions,status,rows,node('p',scope.conv?'Done and Reopen are shared. Names and Archive sync across your linked devices. Delete for me leaves others’ copies.':'Names, Done and Archive sync across your linked devices. Delete for me leaves others’ copies.',{className:'hint'}));
  root.append(popup);for(const n of siblings)n.inert=true;bar.inert=true;
  const flat=!!person;let filter='active',selected=new Set(),counts=new Map(),items=[],next='',busy=false,confirming=false;
  const render=()=>{
   filters.replaceChildren(...['active','done','archived'].map(f=>{const b=button(f[0].toUpperCase()+f.slice(1),()=>{filter=f;void load();});b.setAttribute('aria-pressed',String(f===filter));return b;}));
   rows.replaceChildren(...items.map(t=>{const row=node('div','',{className:'topic-row'}),check=node('input','',{type:'checkbox',checked:selected.has(t.id),disabled:busy||confirming||flat&&!t.native});check.setAttribute('aria-label','Select '+(t.title||'Untitled topic'));check.onchange=()=>{counts.set(t.id,t.count);check.checked?selected.add(t.id):selected.delete(t.id);renderActions();};const go=button((t.title||'Untitled topic')+' · '+t.state,()=>{finish();flat?chooseRoot(t.conv,t.topic):choose(t.id);});row.append(check,go,node('small',(t.count||0)+(t.count===1?' message':' messages')+(t.unread?' · '+t.unread+' unread':'')+(t.guests?' · '+t.guests+(t.guests===1?' guest':' guests'):'')),node('small',t.last||''));return row;}));
   if(next)rows.append(button('Show more',()=>void load(true)));if(!items.length)rows.append(node('p','No '+filter+' topics.',{className:'hint'}));renderActions();
  };
  const renderActions=()=>{if(confirming)return;actions.replaceChildren();if(!selected.size)return;actions.append(node('span',selected.size+' selected'));for(const [what,label]of [['delete','Delete for me'],['done','Mark done'],['archive','Archive']]){const b=button(label,()=>confirm(what,label));b.disabled=busy;actions.append(b);}};
  const confirm=(what,label)=>{
   confirming=true;const ids=[...selected],covered=Object.fromEntries(ids.map(id=>[id,counts.get(id)]));actions.replaceChildren(node('p',label+' '+ids.length+' topics? '+(what==='delete'?'Other people keep their copies. People chats: your devices; agent chats: this device.':what==='done'&&scope.conv?'Shared with everyone.':'On your linked devices.')));
   const cancel=()=>{if(timer)clearTimeout(timer);timer=null;confirming=false;status.textContent='Cancelled; nothing changed.';render();};
   actions.append(button('Cancel',cancel),button('Confirm',()=>{
    actions.replaceChildren(button('Undo',cancel));status.textContent='Will apply in six seconds.';
    timer=setTimeout(async()=>{timer=null;busy=true;status.textContent='Applying…';actions.replaceChildren();try{const r=await request(what,ids,covered);if(dead||all!==popup)return;selected.clear();announce(r.note);await changed();status.textContent=r.note;confirming=false;await load();}catch(e){if(all===popup){status.textContent=e.message;confirming=false;}}finally{busy=false;if(all===popup)render();}},TOPIC_UI.undoDelay);
   }));
  };
  let loading=0;
  const load=async(more=false)=>{const ask=++loading;status.textContent='Loading…';try{const q=new URLSearchParams({...scope,state:filter,q:search.value,limit:String(TOPIC_UI.pageSize),...(more?{before:next}:{})});const p=flat?await flatPage(filter,search.value):await api('/api/topics?'+q);if(dead||seq!==version||all!==popup||ask!==loading)return;items=more?[...items,...p.topics]:p.topics;next=p.next||'';status.textContent='';render();}catch(e){if(all===popup&&ask===loading)status.textContent=e.message;}};
  refreshList=()=>{if(!busy&&!confirming)void load();};
  search.oninput=()=>void load();popup.addEventListener('keydown',e=>{if(e.key==='Escape'){e.preventDefault();finish();}if(e.key==='Tab'){const focus=[...popup.querySelectorAll('button,input')].filter(n=>!n.disabled),i=focus.indexOf(root.getRootNode().activeElement);if(e.shiftKey&&i===0){e.preventDefault();focus.at(-1)?.focus();}else if(!e.shiftKey&&i===focus.length-1){e.preventDefault();focus[0]?.focus();}}});
  popup.cleanup=()=>{for(const n of siblings)n.inert=false;bar.inert=false;};
  popup.querySelector('button').focus();await load();
 }
 const oldClose=close;close=()=>{all?.cleanup?.();oldClose();};
 async function flatPage(state,q) {
  const captured=person,version=personSeq;
  if(!captured)return {topics:[],next:''};
  const loaded=await Promise.all(captured.roots.map(async r=>[r.id,r.id===scope.conv&&captured.view?captured.view:await api('/api/dm?id='+encodeURIComponent(r.id))]));
  if(!person||person.main!==captured.main||version!==personSeq)return {topics:[],next:''};
  views=Object.fromEntries(loaded);personReady=true;paint();
  const query=q.trim().toLowerCase();
  return {topics:personTopicEntries(captured.roots,views,captured.main).filter(t=>t.state===state&&(t.title+' '+t.last).toLowerCase().includes(query)).map(t=>({...t,id:t.key})),next:''};
 }
 function paint() {
  bar.replaceChildren();
  if(person) {
   const entries=personTopicEntries(person.roots,views,person.main),selected=entries.find(t=>t.conv===scope.conv&&t.topic===current);
   const main=button('Main',()=>chooseRoot(person.main,''));main.setAttribute('aria-pressed',String(scope.conv===person.main&&!current));bar.append(main);
   if(selected){const b=button(selected.title.slice(0,40),()=>void list());b.className='topic-chip';b.setAttribute('aria-current','true');b.setAttribute('aria-pressed','true');bar.append(b);}
   bar.append(button(root.clientWidth<700?'All'+(personReady?' '+entries.length:''):'All topics'+(personReady?' ('+entries.length+')':''),()=>void list()),Object.assign(button(root.clientWidth<700?'+ New':'New topic',fresh),{ariaLabel:'New topic'}));
  } else {
   if(scope.conv){const main=button(root.clientWidth<700?'Main':'Main flow',()=>choose(''));main.setAttribute('aria-pressed',String(!current));bar.append(main);}
   const picked=topics.filter(t=>t.state==='active'||t.id===current).slice(0,TOPIC_UI.barMax);
   const opened=topics.find(t=>t.id===current);if(opened&&!picked.includes(opened))picked[picked.length-1]=opened;
   for(const t of picked){const b=button((t.title||'Untitled').slice(0,40),()=>choose(t.id));b.title=t.title+' · '+t.state;b.setAttribute('aria-pressed',String(current===t.id));b.className='topic-chip';bar.append(b);}
   bar.append(button(root.clientWidth<700?'All '+topics.length:'All topics ('+topics.length+')',()=>void list()),Object.assign(button(root.clientWidth<700?'+ New':'New topic',fresh),{ariaLabel:'New topic'}));
  }
  const t=topics.find(t=>t.id===current);if(t){bar.append(button(t.state==='active'?(root.clientWidth<700?'Done':'Mark done'):'Reopen',async()=>{try{const r=await api('/api/topic/'+(t.state==='active'?'done':'reopen'),{...scope,id:t.id,count:t.count});announce(r.note);await changed();}catch(e){announce(e.message);}}));}
 }
 function update(nextScope,nextTopics,id,nextPerson=null) {
  if(JSON.stringify(scope)!==JSON.stringify(nextScope))close();scope=nextScope;topics=nextTopics||[];current=id||'';person=nextPerson;
  if(person){views[scope.conv]=person.view;const stamp=JSON.stringify([person.main,person.roots.map(r=>[r.id,r.count,r.last_at,r.unread])]);if(stamp!==personStamp){personStamp=stamp;personReady=false;personSeq++;refreshList?.();}}else{personStamp='';personSeq++;personReady=false;views={};}
  paint();
 }
 return {update,show(visible){bar.hidden=!visible;},stop(){dead=true;close();bar.remove();}};
}
