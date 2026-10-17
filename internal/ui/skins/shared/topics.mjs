import {personTopicEntries} from './person-topics.mjs';
import {holdChoices,pagedChoices,placeChoices} from './choices.mjs';
// Shared skin source, copied into each independent package at build time.
// Host API v1 only: no provider, storage, transport or global document access.
export const TOPIC_UI = Object.freeze({barMax:4,pageSize:50,undoDelay:6000});
const unreadFirst=(a,b)=>Number(b.unread>0)-Number(a.unread>0)||Date.parse(b.last_at||'')-Date.parse(a.last_at||'')||String(b.topic??b.id).localeCompare(String(a.topic??a.id))||String(a.conv||a.peer||'').localeCompare(String(b.conv||b.peer||''));
export function topicControls(root,{api,choose,chooseRoot,fresh,changed,announce,isFresh=()=>false}) {
 const doc=root.ownerDocument,win=doc.defaultView,bar=doc.createElement('nav');bar.className='topics-bar';bar.setAttribute('aria-label','Topics');
 const zoom=root.querySelector('#zoom');if(zoom)zoom.before(bar);else root.querySelector('.conv-head').after(bar);
 const node=(tag,text='',props={})=>{const n=doc.createElement(tag);if(text)n.textContent=text;Object.assign(n,props);return n;};
 const button=(text,fn)=>node('button',text,{type:'button',className:'an-hit',onclick:e=>{if(!dead)return fn(e);}});
 let scope={},topics=[],current='',all=null,timer=null,seq=0,dead=false,person=null,views={},personStamp='',personSeq=0,personReady=false,refreshList=null,meta={},contentStamp='';
 const barNodes=new Map(),barStable=holdChoices(bar,{key:t=>t.id,onChange:()=>paint()});
 const flatEntries=()=>personTopicEntries(person.roots,views,person.main).map(t=>({...t,last_at:t.native?.last_at||views[t.conv]?.main_topic?.last_at||t.root.last_at||'',unread:t.topic?t.native?.unread||0:views[t.conv]?.main_topic?.unread??t.root.scopes?.find(s=>!s.topic)?.unread??0}));
 const request=async(what,ids,counts)=>{
  if(!person)return api('/api/topic/'+what,{...scope,id:'',ids,counts});
  const selected=flatEntries().filter(t=>ids.includes(t.key)&&t.native),groups=new Map();
  for(const t of selected){const group=groups.get(t.conv)||{ids:[],counts:{}};group.ids.push(t.topic);group.counts[t.topic]=counts[t.key];groups.set(t.conv,group);}
  const results=await Promise.all([...groups].map(([conv,g])=>api('/api/topic/'+what,{conv,id:'',...g})));
  return {note:results.map(r=>r.note).filter(Boolean).join(' ')};
 };
 function close(){if(timer)win.clearTimeout(timer);timer=null;all?.cleanup?.();all?.remove();all=null;refreshList=null;seq++;}
 async function list(){
  if(dead)return;close();const version=seq;
  all=node('section','',{className:'topics-list'});all.setAttribute('role','dialog');all.setAttribute('aria-modal','true');all.setAttribute('aria-label','All topics');
  const restore=root.getRootNode().activeElement,siblings=[...root.children].filter(n=>!n.inert),popup=all;
  const finish=()=>{if(dead||all!==popup)return;close();restore?.focus?.();};
  const control=(text,fn)=>button(text,e=>{if(all===popup)return fn(e);});
  const search=node('input','',{type:'search',placeholder:'Search topics'});search.setAttribute('aria-label','Search topics');
  const clear=control('Clear search',()=>{search.value='';reset();});
  const filters=node('div','',{className:'topics-actions'}),names=node('div','',{className:'topics-actions'}),renamePanel=node('form'),rows=node('ul','',{className:'topics-rows'}),actions=node('div','',{className:'topics-actions'}),status=node('p','',{className:'hint'}),retry=control('Try again',()=>page.retry()),more=control('Show more',()=>page.more());
  renamePanel.hidden=true;
  status.setAttribute('role','status');rows.setAttribute('aria-label','Active topics');
  popup.append(node('h2','All topics'),control('Close',finish),search,clear,filters,names,renamePanel,actions,status,retry,rows,more,node('p',scope.conv?'Done and Reopen are shared. Conversation topic names change for everyone with access. Main and device names, Archive and Delete for me stay personal.':'Names, Done and Archive sync across your linked devices. Delete for me leaves others’ copies.',{className:'hint'}));
  root.append(popup);for(const n of siblings)n.inert=true;bar.inert=true;
  const flat=!!person;let filter='active',selected=new Set(),counts=new Map(),busy=false,confirming=false,note='',cards=new Map(),queryScope='',editing=null;
  const stable=holdChoices(rows,{key:t=>t.id,onChange:()=>render()});
  const page=pagedChoices({key:t=>t.id,onChange:()=>render(),pageSize:TOPIC_UI.pageSize,load:async(before,limit,query)=>{
   const captured=JSON.parse(query);
   if(flat){const p=await flatPage(captured.state,captured.q);return {rows:p.topics,total:p.topics.length,next:''};}
   const q=new URLSearchParams({...scope,...(captured.q?{q:captured.q}:{state:captured.state}),limit:String(limit),...(before?{before}:{})});
   const p=await api('/api/topics?'+q);return{rows:p.topics||[],total:p.matched,next:p.next,content:p.content,seq:p.seq};
  }});
  const filterButtons=['active','done','archived'].map(f=>{const b=control(f[0].toUpperCase()+f.slice(1),()=>{filter=f;reset();});filters.append(b);return b;});
  if(scope.conv||person)names.append(control('Rename Main…',e=>renameTopic({conv:person?.main||scope.conv,root:true,title:views[person?.main||scope.conv]?.main_topic?.title||'Main flow'},e.currentTarget)));
  function renameTopic(t,restore,present=()=>true){
   if(busy||confirming||dead||all!==popup)return;
   const route=flat?{conv:t.conv,peer:'',id:t.topic||'',...(!t.topic?{root:true}:{})}:scope.conv?{conv:scope.conv,peer:'',id:t.root?'':t.id,...(t.root?{root:true}:{})}:{peer:t.peer||scope.peer||'',id:t.id};
   const shared=!!route.conv&&!route.root,captured=JSON.stringify(scope),edit={};editing=edit;stable.select(true);
   const valid=()=>!dead&&all===popup&&seq===version&&JSON.stringify(scope)===captured&&editing===edit&&present();
   const input=node('input','',{value:t.title||'',maxLength:64,autocomplete:'off'}),automatic=node('input','',{type:'checkbox',className:'an-hit'}),error=node('p','',{className:'error'}),buttons=node('div','',{className:'topics-actions'});
   input.setAttribute('aria-label','Topic name');automatic.setAttribute('aria-label',shared?'Use automatic title for everyone':'Use automatic title');error.setAttribute('role','alert');
   const cancel=()=>{if(editing!==edit)return;editing=null;renamePanel.replaceChildren();renamePanel.hidden=true;stable.select(selected.size>0);restore?.focus?.();};
   const save=control('Save name',async()=>{
    if(!valid()){error.textContent='The workspace changed. Reopen this topic.';return;}
    const title=automatic.checked?'':input.value.trim();if(!automatic.checked&&(!title||title.length>64)){error.textContent='Give the topic a name of up to 64 characters, or choose its automatic title.';return;}
    busy=true;save.disabled=true;input.disabled=true;automatic.disabled=true;
    try{const result=await api('/api/topic/rename',{...route,title});if(!valid())return;note=result.note||'Topic name saved.';announce(note);cancel();await changed();if(!dead&&all===popup){page.retry();render();}}
    catch(reason){if(valid())error.textContent=reason?.message||'The topic name could not be saved.';}
    finally{busy=false;if(valid()){save.disabled=false;automatic.disabled=false;input.disabled=automatic.checked;}}
   });
   automatic.onchange=()=>{input.disabled=automatic.checked||busy;};
   renamePanel.onsubmit=e=>{e.preventDefault();if(!busy)void save.onclick(e);};
   renamePanel.replaceChildren(node('h3','Rename topic'),node('p',shared?'This name changes for everyone with access to this topic. Resetting restores the automatic title for everyone.':'The name syncs across your linked devices. Other people keep their own names.'),node('label','Name'),input,node('label',shared?'Use automatic title for everyone':'Use automatic title'),automatic,error,buttons);
   buttons.append(control('Cancel name change',cancel),save);renamePanel.hidden=false;input.focus();
  }
  function renderActions(){
   if(confirming)return;actions.replaceChildren();if(!selected.size)return;
   actions.append(node('span',selected.size+' selected'),control('Cancel selection',()=>{selected.clear();counts.clear();stable.release();render();}));
   for(const [what,label]of [['delete','Delete for me'],['done','Mark done'],['archive','Archive']]){const b=control(label,()=>confirm(what,label));b.disabled=busy;actions.append(b);}
  }
  function render(){
   if(dead||all!==popup)return;
   const p=page.state;filterButtons.forEach((b,i)=>b.setAttribute('aria-pressed',String(['active','done','archived'][i]===filter)));
   rows.setAttribute('aria-label',filter[0].toUpperCase()+filter.slice(1)+' topics');rows.setAttribute('aria-busy',String(p.loading));
   status.textContent=note||p.error||(p.loading?'Loading…':!p.rows.length?'No '+(search.value.trim()?'matching':filter)+' topics.':'');status.setAttribute('role',p.error?'alert':'status');
   status.classList.toggle('sr-only',!!(p.loading&&p.rows.length));
   retry.hidden=!p.error;more.hidden=!p.next;more.disabled=p.loading;clear.hidden=!search.value;
   const choices=stable.update(p.rows),keep=new Set(),nodes=[];
   for(const {row:t,present} of choices){
    let card=cards.get(t.id);
    if(!card){const row=node('li','',{className:'topic-row'}),check=node('input','',{type:'checkbox',className:'an-hit'}),go=control('',()=>{}),summary=node('small'),preview=node('small'),rename=control('',e=>{if(card.present)renameTopic(card.topic,e.currentTarget,()=>card.present);});row.setAttribute('data-topic-id',t.id);row.append(check,go,summary,preview,rename);card={row,check,go,summary,preview,rename};cards.set(t.id,card);
     check.onchange=()=>{if(dead||all!==popup||!card.present)return;const t=card.topic;if(check.checked){if(!selected.has(t.id))counts.set(t.id,t.count);selected.add(t.id);}else{selected.delete(t.id);counts.delete(t.id);}stable.select(selected.size>0);renderActions();};
     go.onclick=()=>{if(dead||all!==popup||!card.present)return;const t=card.topic;stable.release();finish();flat?chooseRoot(t.conv,t.topic):choose(t.id);};
    }
    card.topic=t;card.present=present;card.check.checked=selected.has(t.id);card.check.disabled=!present||busy||confirming||flat&&!t.native;card.check.setAttribute('aria-label','Select '+(t.title||'Untitled topic'));
    card.go.textContent=(t.title||'Untitled topic')+' · '+t.state;card.go.setAttribute('aria-disabled',String(!present));
    card.rename.textContent='Rename topic '+(t.title||'Untitled topic')+'…';card.rename.disabled=!present||busy||confirming;
    card.summary.textContent=present?(t.count||0)+(t.count===1?' message':' messages')+(t.unread?' · '+t.unread+' unread':'')+(t.guests?' · '+t.guests+(t.guests===1?' guest':' guests'):''):'No longer available';card.preview.textContent=t.last||'';
    keep.add(t.id);nodes.push(card.row);
   }
   for(const id of cards.keys())if(!keep.has(id))cards.delete(id);placeChoices(rows,nodes);renderActions();
  }
  function confirm(what,label){
   confirming=true;const ids=[...selected],covered=Object.fromEntries(ids.map(id=>[id,counts.get(id)]));actions.replaceChildren(node('p',label+' '+ids.length+' topics? '+(what==='delete'?'Other people keep their copies. People chats: your devices; agent chats: this device.':what==='done'&&scope.conv?'Shared with everyone.':'On your linked devices.')));
   const cancel=()=>{if(timer)win.clearTimeout(timer);timer=null;confirming=false;note='Cancelled; nothing changed.';render();};
   actions.append(control('Cancel',cancel),control('Confirm',()=>{
    actions.replaceChildren(control('Undo',cancel));note='Will apply in six seconds.';status.textContent=note;
    timer=win.setTimeout(async()=>{timer=null;if(dead||all!==popup||seq!==version)return;busy=true;note='Applying…';status.textContent=note;actions.replaceChildren();try{const r=await request(what,ids,covered);if(dead||all!==popup)return;selected.clear();counts.clear();stable.release();announce(r.note);await changed();if(dead||all!==popup)return;note=r.note;confirming=false;page.retry();}catch{if(all===popup){note='These topics couldn’t change. Try again.';confirming=false;}}finally{busy=false;if(all===popup)render();}},TOPIC_UI.undoDelay);
   }));
  }
  function sync(){const q=search.value.trim(),key=JSON.stringify({state:q?'':filter,q});if(key!==queryScope){queryScope=key;stable.reset();selected.clear();counts.clear();}page.update(key,contentStamp,true);}
  function reset(){if(dead||all!==popup)return;note='';if(timer)win.clearTimeout(timer);timer=null;confirming=false;sync();render();}
  refreshList=()=>{if(!busy)sync();};search.oninput=reset;
  const keydown=e=>{if(dead||all!==popup)return;if(e.key==='Escape'){e.preventDefault();finish();}if(e.key==='Tab'){const focus=[...popup.querySelectorAll('button,input')].filter(n=>!n.disabled&&!n.hidden),i=focus.indexOf(root.getRootNode().activeElement);if(e.shiftKey&&i===0){e.preventDefault();focus.at(-1)?.focus?.();}else if(!e.shiftKey&&i===focus.length-1){e.preventDefault();focus[0]?.focus?.();}}};popup.addEventListener('keydown',keydown);
  popup.cleanup=()=>{popup.removeEventListener('keydown',keydown);page.stop();stable.stop();cards.clear();for(const n of siblings)n.inert=false;bar.inert=false;};
  popup.querySelector('button').focus();sync();
 }
 async function flatPage(state,q){
  const captured=person,version=personSeq,visit=seq;if(!captured||dead)return{topics:[],next:''};
  const loaded=await Promise.all(captured.roots.map(async r=>[r.id,r.id===scope.conv&&captured.view?captured.view:await api('/api/dm?id='+encodeURIComponent(r.id))]));
  if(dead||seq!==visit||!person||person.main!==captured.main||version!==personSeq)return{topics:[],next:''};
  views=Object.fromEntries(loaded);personReady=true;paint();const words=q.toLowerCase().split(/\s+/).filter(Boolean);
  return{topics:flatEntries().filter(t=>(q||t.state===state)&&words.every(w=>(t.title+' '+(t.native?.auto_title||'')+' '+t.last).toLowerCase().includes(w))).sort(unreadFirst).map(t=>({...t,id:t.key})),next:''};
 }
 function paint(){
  if(dead)return;const nodes=[],keep=new Set(),put=(id,text,click)=>{let b=barNodes.get(id);if(!b){b=button(text,()=>{if(barNodes.get(id)===b)return b.run?.();});barNodes.set(id,b);}b.textContent=text;b.run=click;keep.add(id);nodes.push(b);return b;};
  let entries=person?flatEntries():topics,picked=[],mainUnread=meta.mainUnread??(person?views[person.main]?.main_topic?.unread??person.roots.find(r=>r.id===person.main)?.scopes?.find(s=>!s.topic)?.unread:undefined);
  const main=scope.conv||person?put('main',(root.clientWidth<700?'Main':'Main flow')+(mainUnread?' · '+mainUnread+' unread':''),()=>{barStable.release();person?chooseRoot(person.main,''):choose('');}):null;
  if(main)main.setAttribute('aria-pressed',String(person?scope.conv===person.main&&!current&&!isFresh():!current&&!isFresh()));
  if(person){const selected=entries.find(t=>t.conv===scope.conv&&t.topic===current);picked=selected?[{...selected,id:selected.key}]:[];}
  else{picked=[...topics].filter(t=>t.state==='active'||t.id===current).sort(unreadFirst).slice(0,TOPIC_UI.barMax);const opened=topics.find(t=>t.id===current);if(opened&&!picked.includes(opened)){if(picked.length)picked[picked.length-1]=opened;else picked.push(opened);}}
  for(const {row:t,present}of barStable.update(picked)){const b=put('topic:'+t.id,(t.title||'Untitled').slice(0,40)+(t.unread?' · '+t.unread+' unread':''),()=>{if(!present)return;barStable.release();person?chooseRoot(t.conv,t.topic):choose(t.id);});b.className='topic-chip an-hit';b.title=t.title+' · '+t.state;b.setAttribute('aria-pressed',String((person?t.conv===scope.conv&&t.topic===current:current===t.id)&&!isFresh()));b.setAttribute('aria-disabled',String(!present));}
  const total=meta.unreadTotal??(person?person.roots.reduce((n,r)=>n+(r.unread||0),0):undefined),other=typeof total==='number'?Math.max(0,total-(mainUnread||0)-picked.reduce((n,t)=>n+(t.unread||0),0)):0;
  put('all',(root.clientWidth<700?'All':'All topics')+(person?(personReady?' ('+entries.length+')':''):' ('+topics.length+')')+(other?' · '+other+' unread in other topics':''),()=>void list());
  const freshButton=put('new',root.clientWidth<700?'+ New':'New topic',fresh);freshButton.setAttribute('aria-label','New topic');freshButton.setAttribute('aria-pressed',String(isFresh()));
  const t=topics.find(t=>t.id===current);if(t)put('state',t.state==='active'?(root.clientWidth<700?'Done':'Mark done'):'Reopen',async()=>{try{const r=await api('/api/topic/'+(t.state==='active'?'done':'reopen'),{...scope,id:t.id,count:t.count});if(dead)return;announce(r.note);await changed();}catch{if(!dead)announce('This topic couldn’t change. Try again.');}});
  for(const id of barNodes.keys())if(!keep.has(id))barNodes.delete(id);placeChoices(bar,nodes);
 }
 function update(nextScope,nextTopics,id,nextPerson=null,nextMeta={}){
  if(dead)return;const scopeChanged=JSON.stringify(scope)!==JSON.stringify(nextScope)||person?.main!==nextPerson?.main;if(scopeChanged){close();barStable.reset();}
  scope={...nextScope};topics=nextTopics||[];current=id||'';person=nextPerson;meta=nextMeta;
  const stamp=person?JSON.stringify([person.main,person.roots.map(r=>[r.id,r.count,r.last_at,r.unread]),person.view?.topics,person.view?.main_topic]):'';
  if(person){views[scope.conv]=person.view;if(stamp!==personStamp){personStamp=stamp;personReady=false;personSeq++;}}
  else{personStamp='';personSeq++;personReady=false;views={};}
  const revision=meta.revision??JSON.stringify([topics,stamp]);if(contentStamp!==revision){contentStamp=revision;refreshList?.();}paint();
 }
 return{update,repaint(){paint();},show(visible){bar.hidden=!visible;if(!visible)close();},stop(){if(dead)return;dead=true;close();barStable.stop();barNodes.clear();bar.remove();}};
}
