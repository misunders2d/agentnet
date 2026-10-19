// Source-only fake DOM/events, matching the shared visible/video check convention.
import assert from 'node:assert/strict';
import {topicControls,TOPIC_UI} from '../skins/shared/topics.mjs';
import {unreadReview} from '../skins/shared/unread-review.mjs';
import {holdChoices,pagedChoices} from '../skins/shared/choices.mjs';


class Events {
 listeners=new Map();
 addEventListener(k,f){const set=this.listeners.get(k)||new Set();set.add(f);this.listeners.set(k,set);}
 removeEventListener(k,f){this.listeners.get(k)?.delete(f);}
 fire(type,props={}){const event={type,target:this,relatedTarget:null,preventDefault(){this.prevented=true;},...props};for(let n=this;n;n=n.parentNode){event.currentTarget=n;for(const fn of [...(n.listeners.get(type)||[])])fn(event);}return event;}
}
class Element extends Events {
 constructor(tag,doc){super();this.tagName=tag.toUpperCase();this.ownerDocument=doc;this.children=[];this.attributes=new Map();this.style={};this.dataset={};this._text='';this.hidden=false;this.disabled=false;this.inert=false;this.value='';this.clientWidth=1280;}
 get classList(){return{contains:name=>(this.className||'').split(/\s+/).includes(name),toggle:(name,force)=>{const names=new Set((this.className||'').split(/\s+/).filter(Boolean)),on=force??!names.has(name);on?names.add(name):names.delete(name);this.className=[...names].join(' ');return on;}};}
 get textContent(){return this._text+this.children.map(n=>n.textContent).join('');}set textContent(v){this._text=String(v);for(const n of this.children)n.parentNode=null;this.children=[];}
 append(...nodes){for(const n of nodes)this.insertBefore(n,null);}
 insertBefore(node,before){if(node===before)return;node.remove();const i=before?this.children.indexOf(before):this.children.length;assert(i>=0);this.children.splice(i,0,node);node.parentNode=this;}
 replaceChildren(...nodes){for(const n of [...this.children])n.remove();this._text='';this.append(...nodes);}
 remove(){if(this.parentNode){this.parentNode.children=this.parentNode.children.filter(n=>n!==this);this.parentNode=null;}}
 after(node){this.parentNode.insertBefore(node,this.parentNode.children[this.parentNode.children.indexOf(this)+1]||null);}
 before(node){this.parentNode.insertBefore(node,this);}
 contains(node){return !!node&&(this===node||this.children.some(n=>n.contains(node)));}
 setAttribute(k,v){this.attributes.set(k,String(v));if(k.startsWith('data-'))this.dataset[k.slice(5).replace(/-([a-z])/g,(_,c)=>c.toUpperCase())]=String(v);}
 getAttribute(k){return this.attributes.get(k)??null;}
 get ariaLabel(){return this.getAttribute('aria-label');}set ariaLabel(v){this.setAttribute('aria-label',v);}
 getRootNode(){return this.ownerDocument;}
 get isConnected(){return this.connected??this.parentNode?.isConnected??false;}
 querySelectorAll(selector){return all(this).slice(1).filter(n=>selector.split(',').some(part=>matches(n,part.trim())));}
 querySelector(selector){return this.querySelectorAll(selector)[0]||null;}
 focus(){const old=this.ownerDocument.activeElement;if(old===this)return;this.ownerDocument.activeElement=this;old?.fire('focusout',{relatedTarget:this});this.fire('focusin',{relatedTarget:old});}
 click(){if(!this.disabled){const event=this.fire('click');return this.onclick?.(event);}}
}
const all=n=>[n,...n.children.flatMap(all)];
function matches(n,s){if(s[0]==='#')return n.id===s.slice(1);if(s[0]==='.')return n.className?.split(' ').includes(s.slice(1));const m=s.match(/^(\w+)?\[([^=\]]+)(?:="([^"]*)")?\]$/);if(m)return(!m[1]||n.tagName===m[1].toUpperCase())&&(m[3]===undefined?n.getAttribute(m[2])!==null:n.getAttribute(m[2])===m[3]);return n.tagName===s.toUpperCase();}
function dom(){const doc=new Events(),win=new Events(),timers=new Map();let serial=0;doc.defaultView=win;doc.activeElement=null;doc.createElement=tag=>new Element(tag,doc);win.setTimeout=(fn,ms)=>{const id=++serial;timers.set(id,{fn:()=>{timers.delete(id);return fn();},ms});return id;};win.clearTimeout=id=>timers.delete(id);const root=doc.createElement('div');root.connected=true;const head=doc.createElement('header');head.className='conv-head';root.append(head);return{root,doc,win,timers};}
const tick=async()=>{for(let i=0;i<8;i++)await new Promise(r=>setImmediate(r));};
const deferred=()=>{let resolve,reject;const promise=new Promise((a,b)=>{resolve=a;reject=b;});return{promise,resolve,reject};};
const button=(root,text)=>{const n=all(root).find(n=>n.tagName==='BUTTON'&&(typeof text==='string'?n.textContent===text:text.test(n.textContent)));assert(n,'missing button '+text);return n;};
const role=(root,name)=>{const n=all(root).find(n=>n.getAttribute('aria-label')===name);assert(n,'missing label '+name);return n;};
const topic=(i,extra={})=>({id:String(i),title:'needle '+i,last:'preview '+i,last_at:'2026-10-16T00:00:00Z',state:'active',count:2,unread:1,...extra});
assert.deepEqual(TOPIC_UI,{barMax:4,pageSize:50,undoDelay:6000},'existing public topic defaults retained');
function topicFixture(){const f=dom(),calls=[],chosen=[],announcements=[];let data=[topic(1),topic(2,{state:'done'}),topic(3,{state:'archived'})],revision=1;
 const api=async(path,body)=>{calls.push({path,body});if(body)return{note:'Updated'};const q=new URL('http://owned'+path).searchParams;if(path.startsWith('/api/topics?')){const match=data.filter(t=>(!q.has('state')||t.state===q.get('state'))&&(!q.get('q')||(t.title+' '+t.last).includes(q.get('q'))));const from=q.get('before')?Number(q.get('before').split(':')[1]):0,limit=Number(q.get('limit'));return{topics:match.slice(from,from+limit),matched:match.length,next:from+limit<match.length?'opaque:'+String(from+limit):''};}throw Error('unexpected '+path);};
 const controls=topicControls(f.root,{api,choose:id=>chosen.push(id),chooseRoot:(conv,topic)=>chosen.push({conv,topic}),fresh:()=>chosen.push('new'),changed:async()=>{},announce:n=>announcements.push(n)});
 const update=(meta={})=>{controls.update({conv:'chat'},data,'',null,{revision,mainUnread:2,unreadTotal:5,...meta});for(const b of all(f.root).filter(n=>n.tagName==='BUTTON'))assert(b.classList.contains('an-hit'));};
 return{...f,calls,controls,chosen,announcements,update,set data(v){data=v;},get data(){return data;},push(){revision++;update();},async open(){button(f.root,/^All topics/).click();await tick();return role(f.root,'All topics');}};
}

// This actual old-module path fails before the shared topics upgrade.
{
 const f=topicFixture();f.update();assert.equal(f.calls.length,0);const popup=await f.open();button(popup,/^Archived/).click();await tick();const search=role(popup,'Search topics');search.value='  needle  ';search.oninput();await tick();const q=new URL('http://owned'+f.calls.at(-1).path).searchParams;
 assert.equal(q.has('state'),false,'nonempty trimmed search spans ALL statuses');assert.equal(q.get('q'),'needle');button(popup,'Clear search').click();await tick();assert.equal(new URL('http://owned'+f.calls.at(-1).path).searchParams.get('state'),'archived');f.controls.stop();
}
if(process.argv.includes('--before-topics'))process.exit(0);

// The actual event helper: first bubbling move holds synchronously;100 more do no work.
{
 const f=dom();let changes=0;const hold=holdChoices(f.root,{key:r=>r.id,onChange:()=>changes++});let rows=[topic('old'),topic('stay')];hold.update(rows);f.root.fire('pointermove');for(let i=0;i<100;i++){f.root.fire('pointermove');f.root.fire('pointerover');}assert.equal(changes,1);
 rows=[topic('stay',{count:9}),topic('new')];let view=hold.update(rows);assert.deepEqual(view.map(c=>[c.row.id,c.present]),[['old',false],['stay',true],['new',true]]);assert.equal(view[1].row.count,9);f.root.fire('pointerleave');assert.deepEqual(hold.update(rows).map(c=>c.row.id),['stay','new']);f.root.fire('focusin');rows.reverse();assert.equal(hold.update(rows)[0].row.id,'stay');f.root.fire('focusout',{relatedTarget:null});assert.equal(hold.update(rows)[0].row.id,'new');hold.select(true);rows.reverse();assert.equal(hold.update(rows)[0].row.id,'new');hold.release();assert.equal(hold.update(rows)[0].row.id,'stay');hold.stop();const before=changes;f.root.fire('pointermove');assert.equal(changes,before);assert.equal([...f.root.listeners.values()].reduce((n,s)=>n+s.size,0),0);
}

// Actual Review projection: closed fetch0, uncapped total,>200 prefix, stable nodes.
{
 const f=dom(),calls=[],chosen=[];let data=Array.from({length:263},(_,i)=>({key:'k'+i,kind:'chat',conv:'chat',topic:'named',topic_title:'Never opened literal title',id:'id'+i,lid:'lid'+i,author:'verified-author',dir:'in',label:'Casey',preview:'Preview '+i,at:'2026-10-16T00:00:00Z'})),revision=1,gate=null;
 const api=async(path,body)=>{calls.push({path,body});assert.equal(body,undefined);assert(path.startsWith('/api/unread?'));if(gate){const g=gate;gate=null;await g.promise;}const q=new URL('http://owned'+path).searchParams,from=q.get('after')?Number(q.get('after').split(':')[1]):0,limit=Number(q.get('limit'));assert(limit<=200);return{rows:data.slice(from,from+limit),total:data.length,next:from+limit<data.length?'opaque:'+String(from+limit):'',content:revision,seq:revision};};
   const review=unreadReview(f.root,{api,onChoose:row=>chosen.push(row)}),overview=()=>({unread_total:data.length,seq:revision,dms:[{id:'chat',peer:{label:'Fictional chat'}}]});
 const update=()=>review.update({overview:overview(),revision,scope:'workspace-one'});update();await tick();assert.equal(calls.length,0);review.show(true);await tick();assert.equal(new URL('http://owned'+calls[0].path).searchParams.get('limit'),'50');assert.equal(role(f.root,'Unread messages').children.length,50);assert(f.root.textContent.includes('Never opened literal title'));assert(f.root.textContent.includes('Unread · 263'));
 for(let i=0;i<5;i++){button(f.root,'Show more unread messages').click();await tick();}const list=role(f.root,'Unread messages');assert.equal(list.children.length,263);const first=list.children[0];list.fire('pointermove');const start=calls.length;data=[{...data[1],preview:'Updated in place'},...data.slice(2),{...data[0],key:'new',id:'new-id'}];const loading=deferred();gate=loading;revision++;for(let i=0;i<100;i++)update();await tick();const loadingStatus=all(f.root).find(n=>n.getAttribute('role')==='status');assert(loadingStatus.classList.contains('sr-only'),'loading with retained rows uses the existing accessibility utility');loading.resolve();await tick();assert.equal(loadingStatus.classList.contains('sr-only'),false,'finished loading removes only the accessibility utility');assert.deepEqual(calls.slice(start).map(c=>Number(new URL('http://owned'+c.path).searchParams.get('limit'))),[200,63]);assert.equal(list.children[0],first);assert.equal(first.children[0].getAttribute('aria-disabled'),'true');assert.equal(list.children.length,264);first.children[0].click();assert.equal(chosen.length,0);assert(list.textContent.includes('Updated in place'));const exact=data[0];list.children[1].children[0].click();assert.equal(chosen[0],exact);assert.equal(list.children.length,263);assert.equal(list.children[0].dataset.unreadKey,'k1');
 const count=calls.length;update();await tick();assert.equal(calls.length,count,'same content revision/presence update does not refetch');review.show(false);revision++;update();await tick();assert.equal(calls.length,count,'closed Review never fetches');
 const old=deferred();gate=old;review.show(true);await tick();review.stop();old.resolve();await tick();assert.equal(f.root.querySelector('.unread-review'),null);assert.equal(chosen.length,1);
}

// Bounded request sequencing, dedupe, cursor/stamp errors and stale/stop fences.
{
 let data=Array.from({length:263},(_,i)=>({id:String(i)})),stamp=1,calls=[],active=0,max=0,gate=null,mode='',changes=0;
 const page=pagedChoices({key:r=>r.id,onChange:()=>changes++,load:async(after,limit)=>{active++;max=Math.max(max,active);calls.push({after,limit});try{if(gate){const g=gate;gate=null;await g.promise;}const from=after?Number(after.split(':')[1]):0;return{rows:data.slice(from,from+limit),total:data.length,next:mode==='cycle'?'opaque:0':from+limit<data.length?'opaque:'+String(from+limit):'',content:mode==='stamp'&&after?stamp+1:stamp};}finally{active--;}}});
 page.update('one',1,true);await tick();for(let i=0;i<5;i++){page.more();await tick();}assert.equal(page.state.rows.length,263);assert.equal(max,1);const before=calls.length;for(let i=0;i<100;i++)page.update('one',2,true);await tick();assert.equal(calls.length-before,2);assert.equal(page.state.rows.length,263);
 mode='stamp';page.update('one',3,true);await tick();assert.match(page.state.error,/list changed/);assert.equal(page.state.rows.length,263);mode='cycle';page.retry();await tick();assert.match(page.state.error,/list changed/);mode='';
 const held=deferred();gate=held;page.update('one',4,true);await tick();page.update('two',5,true);held.resolve();await tick();assert.equal(page.state.rows.length,50,'old scope result cannot supply new scope prefix');const last=deferred();gate=last;page.retry();await tick();page.stop();const done=changes;last.resolve();await tick();assert.equal(changes,done,'stop discards in-flight state');
}

// Topics actual paging/selection/undo remain local and preserve captured counts.
{
 const f=topicFixture();f.data=Array.from({length:263},(_,i)=>topic(i));f.update();const popup=await f.open();for(let i=0;i<5;i++){button(popup,'Show more').click();await tick();}const rows=role(popup,'Active topics');assert.equal(rows.children.length,263);const first=rows.children[0],check=first.children[0];check.checked=true;check.onchange();f.data=[...f.data.slice(1).map(t=>({...t,count:9})),topic('new')];f.push();await tick();assert.equal(rows.children.length,264);assert.equal(rows.children[0],first);assert.equal(first.children[1].getAttribute('aria-disabled'),'true');button(popup,'Mark done').click();button(popup,'Confirm').click();assert.equal(f.timers.size,1);const timer=[...f.timers.values()][0];assert.equal(timer.ms,6000);button(popup,'Undo').click();assert.equal(f.timers.size,0);assert.equal(f.calls.filter(c=>c.body).length,0);
 check.checked=true;check.onchange();button(popup,'Mark done').click();button(popup,'Confirm').click();const pending=[...f.timers.values()][0];await pending.fn();await tick();const action=f.calls.find(c=>c.body);assert.equal(action.path,'/api/topic/done');assert.deepEqual(action.body.ids,['0']);assert.deepEqual(action.body.counts,{'0':2});assert(f.root.textContent.includes('2 unread'));f.controls.stop();assert.equal(f.timers.size,0);
}
console.log('PASS shared Review/topics:263 prefix/opaque bounded refill, stable pointer/focus/selection DOM, exact navigation/no actions, search all statuses/clear, preserved bulk counts/undo, stale scope/stop/cursor/stamp fences');

// Page boundaries may repeat/move rows; opaque cursors are never looked up locally.
{
 const calls=[],a=Array.from({length:149},(_,i)=>({id:String(i)}));
 const page=pagedChoices({key:r=>r.id,onChange(){},load:async(after,limit)=>{
  calls.push({after,limit});
  if(!after)return{rows:a.slice(0,50),total:149,next:'u1|old-position',seq:1};
  if(after==='u1|old-position')return{rows:a.slice(49,99),total:149,next:'u1|cursor-row-moved',seq:1};
  assert.equal(after,'u1|cursor-row-moved');return{rows:a.slice(99),total:149,next:'',seq:1};
 }});
 page.update('opaque',1,true);await tick();page.more();await tick();assert.equal(page.state.rows.length,149);assert.equal(new Set(page.state.rows.map(r=>r.id)).size,149);assert.deepEqual(page.state.rows,a);assert.deepEqual(calls.map(c=>c.after),['','u1|old-position','u1|cursor-row-moved']);page.stop();
}

// Useful fixed errors, older-than-overview seq refusal, retry and retained-node stop.
{
 const f=dom(),chosen=[],row={key:'only',kind:'device',thread:'thread',peer:'agent/host',topic_title:'Device literal title',id:'physical',lid:'logical',author:'fingerprint',dir:'out',label:'You',preview:'Preview',at:'2026-10-16T00:00:00Z'};let fail=true,seq=4,calls=0;
 const review=unreadReview(f.root,{api:async(path,body)=>{calls++;assert(path.startsWith('/api/unread?'));assert.equal(body,undefined);if(fail)throw Error('/private/secret');return{rows:[row],total:1,content:1,seq};},onChoose:r=>chosen.push(r)});
 review.update({overview:{unread_total:1,seq:5},revision:1,scope:'host'});review.show(true);await tick();assert(f.root.textContent.includes('couldn’t load'));assert(!f.root.textContent.includes('private'));assert.equal(calls,1);fail=false;button(f.root,'Try again').click();await tick();assert(f.root.textContent.includes('list changed'));seq=5;button(f.root,'Try again').click();await tick();const chosenButton=role(f.root,'Unread messages').children[0].children[0];assert(chosenButton.textContent.includes('Device literal title'));for(const b of all(f.root).filter(n=>n.tagName==='BUTTON'))assert(b.classList.contains('an-hit'));assert(chosenButton.classList.contains('an-row-button'));chosenButton.focus();chosenButton.click();assert.equal(chosen[0],row);review.show(false);review.show(true);await tick();chosenButton.click();assert.equal(chosen.length,1,'detached old node cannot navigate after reopening');const stoppedList=role(f.root,'Unread messages');review.stop();chosenButton.click();assert.equal(chosen.length,1);assert.equal([...stoppedList.listeners.values()].reduce((n,s)=>n+s.size,0),0);
}

// Person-flat baseline loads only open lists and uses DTO Main/native counts.
{
 const f=dom(),calls=[],chosen=[],roots=[{id:'r1',created:'2026-01-01',count:2,unread:5,peer:{person:'person'},last_at:'2026-10-16T00:00:00Z'},{id:'r2',created:'2026-01-02',count:1,unread:4,peer:{person:'person'},last_at:'2026-10-15T00:00:00Z'}];
 const views={r1:{main_topic:{unread:2,state:'active'},messages:[{id:'m1',body:'Main body',unread:false},{id:'m2',topic:'native',body:'Native body',unread:false}],topics:[topic('native',{state:'archived',unread:3})]},r2:{main_topic:{unread:4,state:'archived',last_at:'2026-10-17T00:00:00Z'},messages:[{id:'m3',body:'needle other Main',unread:false}],topics:[]}};
 const person={main:'r1',roots,view:views.r1},controls=topicControls(f.root,{api:async(path,body)=>{calls.push({path,body});assert.equal(body,undefined);assert.equal(path,'/api/dm?id=r2');return views.r2;},choose(){},chooseRoot:(conv,topic)=>chosen.push({conv,topic}),fresh(){},changed:async()=>{},announce(){}});
 controls.update({conv:'r1'},views.r1.topics,'',person,{revision:1,mainUnread:2,unreadTotal:9});await tick();assert.equal(calls.length,0);assert(f.root.textContent.includes('2 unread'));assert(f.root.textContent.includes('7 unread in other topics'));button(f.root,/^All topics/).click();await tick();const popup=role(f.root,'All topics');button(popup,'Archived').click();await tick();const list=role(popup,'Archived topics');assert.equal(list.children.length,2);assert(list.children[0].textContent.includes('4 unread'),'Main DTO count wins over unread:false message history');assert(list.children[1].textContent.includes('3 unread'));assert.equal(list.children[0].children[0].disabled,true,'other-root Main cannot become a native bulk action');list.children[1].children[1].click();assert.deepEqual(chosen,[{conv:'r1',topic:'native'}]);const before=calls.length;controls.update({conv:'r1'},views.r1.topics,'',person,{revision:2,mainUnread:2,unreadTotal:9});await tick();assert.equal(calls.length,before,'closed flat list never expands roots');controls.stop();
}

// Old modal controls/listeners cannot close a reopened list or act after stop.
{
 const f=topicFixture();assert.deepEqual(Object.keys(f.controls).sort(),['repaint','show','stop','update']);f.update();const first=await f.open(),oldGo=role(first,'Active topics').children[0].children[1],oldClose=button(first,'Close');oldClose.click();const second=await f.open();oldGo.click();oldClose.click();first.fire('keydown',{key:'Escape'});assert.equal(role(f.root,'All topics'),second);assert.equal(f.chosen.length,0);assert.equal([...first.listeners.values()].reduce((n,s)=>n+s.size,0),0);f.controls.show(false);const before=f.calls.length;f.push();await tick();assert.equal(f.calls.length,before,'hidden topic controls close the list before a content update');f.controls.stop();oldGo.click();assert.equal(f.chosen.length,0);
}
console.log('PASS shared Review/topics additional gates: duplicates/moving opaque cursors, fixed retry/seq,44px and exact device row, authoritative person Main/hidden counts, closed flat cost and detached modal/row fences');

// Host replacement destroys the old controller before binding a new host API.
{
 const f=dom(),pending=deferred(),chosen=[],row=host=>({key:host,kind:'chat',conv:host,topic:'',id:host,lid:host,author:host,dir:'in',at:'2026-10-16T00:00:00Z',label:host,preview:host+' preview'});
 const old=unreadReview(f.root,{api:()=>pending.promise,onChoose:r=>chosen.push(r)});old.update({overview:{unread_total:1,seq:1},revision:1,scope:'old-host'});old.show(true);await tick();old.stop();
 let requests=0;const current=unreadReview(f.root,{api:async()=>{requests++;return{rows:[row('current-host')],total:1,content:1,seq:1};},onChoose:r=>chosen.push(r)});current.update({overview:{unread_total:1,seq:1},revision:1,scope:'current-host'});current.show(true);await tick();const currentNode=role(f.root,'Unread messages').children[0];pending.resolve({rows:[row('old-host')],total:1,content:1,seq:1});await tick();assert.equal(role(f.root,'Unread messages').children[0],currentNode);assert(f.root.textContent.includes('current-host preview'));assert(!f.root.textContent.includes('old-host preview'));assert.equal(requests,1);assert.equal(chosen.length,0);current.stop();
}

// Keep the pre-existing Archive/Delete/Reopen routes and exact count payloads.
for(const [label,route] of [['Archive','archive'],['Delete for me','delete']]){
 const f=topicFixture();f.update();const popup=await f.open(),check=role(popup,'Active topics').children[0].children[0];check.checked=true;check.onchange();button(popup,label).click();button(popup,'Confirm').click();const timer=[...f.timers.values()][0];assert.equal(timer.ms,6000);await timer.fn();await tick();const action=f.calls.find(c=>c.body);assert.equal(action.path,'/api/topic/'+route);assert.deepEqual(action.body,{conv:'chat',id:'',ids:['1'],counts:{'1':2}});f.controls.stop();
}
{
 const f=topicFixture();f.controls.update({conv:'chat'},f.data,'2',null,{revision:1,mainUnread:2,unreadTotal:5});button(f.root,'Reopen').click();await tick();assert.deepEqual(f.calls.find(c=>c.body),{path:'/api/topic/reopen',body:{conv:'chat',id:'2',count:2}});button(f.root,'New topic').click();assert.deepEqual(f.chosen,['new']);f.controls.stop();
}
console.log('PASS shared Review/topics remaining gates: stale host response cannot repaint current host; Archive/Delete/Reopen/New retain routes and captured counts');

// T1: reuse the actual existing popup and bound API for shared and personal names.
function titleFixture(scope={conv:'chat'},rows=[topic('named')]){
 const f=dom(),calls=[],notes=[];let changes=0,refusal=null,gate=null;
 const controls=topicControls(f.root,{api:async(path,body)=>{calls.push({path,body});if(body){if(gate)await gate.promise;if(refusal)throw Error(refusal);return{note:'Name saved <literal>'};}return{topics:rows,matched:rows.length,next:''};},choose(){},chooseRoot(){},fresh(){},changed:async()=>{changes++;},announce:note=>notes.push(note)});
 controls.update(scope,rows,'',null,{revision:1});
 return{...f,calls,notes,controls,get changes(){return changes;},set refusal(v){refusal=v;},set gate(v){gate=v;},async open(){button(f.root,/^All topics/).click();await tick();return role(f.root,'All topics');}};
}
for(const reset of [false,true]){
 const f=titleFixture({conv:'chat',role:'visitor'}),popup=await f.open();button(popup,'Rename topic needle named…').click();assert(popup.textContent.includes('changes for everyone with access'));assert(popup.textContent.includes('automatic title for everyone'));
 const input=role(popup,'Topic name'),automatic=role(popup,'Use automatic title for everyone');assert.equal(input.maxLength,64);input.value=' Shared <literal> ';automatic.checked=reset;automatic.onchange();button(popup,'Save name').click();await tick();assert.deepEqual(f.calls.filter(c=>c.body),[{path:'/api/topic/rename',body:{conv:'chat',peer:'',id:'named',title:reset?'':'Shared <literal>'}}]);assert.equal(f.changes,1);assert.deepEqual(f.notes,['Name saved <literal>']);assert(popup.textContent.includes('Name saved <literal>'));f.controls.stop();
}
for(const main of [false,true]){
 const f=titleFixture(main?{conv:'chat'}:{peer:'device/host'},[topic('device',{peer:'device/host'})]),popup=await f.open();button(popup,main?'Rename Main…':'Rename topic needle device…').click();assert(popup.textContent.includes('syncs across your linked devices'));assert(!role(popup,'Topic name').parentNode.textContent.includes('changes for everyone'));
 role(popup,'Topic name').value='Personal name';button(popup,'Save name').click();await tick();assert.deepEqual(f.calls.find(c=>c.body),{path:'/api/topic/rename',body:main?{conv:'chat',root:true,peer:'',id:'',title:'Personal name'}:{peer:'device/host',id:'device',title:'Personal name'}});f.controls.stop();
}
for(const stale of ['cancel','close','scope','stop']){
 const f=titleFixture(),popup=await f.open();button(popup,'Rename topic needle named…').click();const save=button(popup,'Save name');role(popup,'Topic name').value='Never sent';
 if(stale==='cancel')button(popup,'Cancel name change').click();if(stale==='close')button(popup,'Close').click();if(stale==='scope')f.controls.update({conv:'other'},[topic('named')],'',null,{revision:2});if(stale==='stop')f.controls.stop();save.click();await tick();assert.equal(f.calls.filter(c=>c.body).length,0,stale);f.controls.stop();
}
{
 const f=titleFixture(),popup=await f.open();f.refusal='Host refused <literal> participation';button(popup,'Rename topic needle named…').click();role(popup,'Topic name').value='Denied';button(popup,'Save name').click();await tick();assert(popup.textContent.includes('Host refused <literal> participation'));assert.equal(f.changes,0);assert.equal(f.calls.filter(c=>c.body).length,1);f.controls.stop();
}
{
 const f=titleFixture(),popup=await f.open(),gate=deferred();f.gate=gate;button(popup,'Rename topic needle named…').click();role(popup,'Topic name').value='Pending';button(popup,'Save name').click();f.controls.stop();gate.resolve();await tick();assert.equal(f.changes,0,'late host result cannot reload a replacement binding');assert.deepEqual(f.notes,[]);
}
console.log('PASS T1 actual shared popup: exact named/reset/Main/device bodies,64limit,plain refusal/note,viewer host authority,cancel/closed/scope/dead/late-result fences');
// Invalid empty/overlong edits never post; explicit reset remains the only empty-title path.
{
 const f=titleFixture(),popup=await f.open();button(popup,'Rename topic needle named…').click();const input=role(popup,'Topic name');for(const value of ['', 'x'.repeat(65)]){input.value=value;button(popup,'Save name').click();await tick();assert.equal(f.calls.filter(c=>c.body).length,0);}f.controls.stop();
}
// Person-flat routing keeps the exact native root/topic and personal other-root Main.
{
 const f=dom(),calls=[],roots=[{id:'r1',created:'2026-01-01',count:1,peer:{person:'p'}},{id:'r2',created:'2026-01-02',count:1,peer:{person:'p'}}],views={r1:{main_topic:{title:'Personal Main'},messages:[{body:'Main'}],topics:[topic('native')]},r2:{main_topic:{title:'Other Main'},messages:[{body:'Other Main'}],topics:[]}};
 const controls=topicControls(f.root,{api:async(path,body)=>{calls.push({path,body});return body?{note:'Saved'}:views.r2;},choose(){},chooseRoot(){},fresh(){},changed:async()=>{},announce(){}});controls.update({conv:'r1'},views.r1.topics,'',{main:'r1',roots,view:views.r1},{revision:1});button(f.root,/^All topics/).click();await tick();const popup=role(f.root,'All topics');button(popup,'Rename topic needle native…').click();role(popup,'Topic name').value='Shared native';button(popup,'Save name').click();await tick();assert.deepEqual(calls.find(c=>c.body).body,{conv:'r1',peer:'',id:'native',title:'Shared native'});button(popup,'Rename topic Other Main…').click();role(popup,'Topic name').value='Personal other';button(popup,'Save name').click();await tick();assert.deepEqual(calls.filter(c=>c.body).at(-1).body,{conv:'r2',peer:'',id:'',root:true,title:'Personal other'});controls.stop();
}
console.log('PASS T1 validation/person-flat gates: empty/65characters noPOST; exact named native and personal other-root Main routing');

// Restore the v32 New-topic callback API; repaint stays inert after stop.
{
 const f=dom(),calls=[],chosen=[];let controls;controls=topicControls(f.root,{api:async(path,body)=>{calls.push({path,body});return{topics:[],matched:0};},choose(){},chooseRoot(){},fresh(){chosen.push('new');controls.repaint();},changed:async()=>{},announce(){}});
 controls.update({conv:'chat'},[topic('named')],'',null,{revision:1});const fresh=button(f.root,'New topic'),bar=f.root.querySelector('.topics-bar');fresh.click();assert.deepEqual(chosen,['new']);assert.equal(calls.length,0);assert.equal(f.root.querySelector('.topics-bar'),bar);controls.stop();controls.repaint();fresh.click();assert.deepEqual(chosen,['new']);assert.equal(f.root.querySelector('.topics-bar'),null);assert.equal(calls.length,0);
}
console.log('PASS retained topicControls API: New callback invokes repaint; stop prevents repaint/fresh/request work');

// Fresh is the entry's existing draft state; only its pressed projection belongs here.
for(const flat of [false,true]){
 const f=dom(),calls=[],chosen=[],rows=[topic('named'),topic('other')],roots=[{id:'r1',created:'2026-01-01',count:1,peer:{person:'p'}},{id:'r2',created:'2026-01-02',count:1,title:'Other Main',peer:{person:'p'}}],views={r1:{messages:[{body:'Main'}],topics:rows},r2:{messages:[{body:'Other Main'}],main_topic:{title:'Other Main'},topics:[]}};
 let freshState=false,conv='r1',current='named',controls;
 const update=()=>controls.update({conv},views[conv].topics,current,flat?{main:'r1',roots,view:views[conv]}:null,{revision:1});
 controls=topicControls(f.root,{api:async(path,body)=>{calls.push({path,body});throw Error('Fresh selection adds no request');},isFresh:()=>freshState,choose:id=>{chosen.push(id);freshState=false;current=id;update();},chooseRoot:(id,topic)=>{chosen.push({conv:id,topic});freshState=false;conv=id;current=topic;update();},fresh:()=>{chosen.push('new');freshState=true;controls.repaint();},changed:async()=>{},announce(){}});
 update();const fresh=button(f.root,'New topic'),main=button(f.root,'Main flow'),bar=f.root.querySelector('.topics-bar');
 const pressedOnly=expected=>{for(const b of all(bar).filter(n=>n.tagName==='BUTTON'))assert.equal(b.getAttribute('aria-pressed')==='true',b===expected,(flat?'person':'native')+' pressed '+b.textContent);};
 fresh.click();assert.equal(fresh.getAttribute('aria-pressed'),'true','New topic projects existing fresh state');pressedOnly(fresh);assert.equal(f.root.querySelector('.topics-bar'),bar);assert.equal(calls.length,0);
 const named=button(bar,/^needle named/);named.click();assert.equal(fresh.getAttribute('aria-pressed'),'false');pressedOnly(named);assert.deepEqual(chosen.at(-1),flat?{conv:'r1',topic:'named'}:'named');
 main.click();assert.equal(fresh.getAttribute('aria-pressed'),'false');pressedOnly(main);fresh.click();pressedOnly(fresh);controls.repaint();pressedOnly(fresh);assert.equal(button(bar,'New topic'),fresh);
 if(flat){conv='r2';current='';update();pressedOnly(fresh);const other=button(bar,/^Other Main/);other.click();assert.deepEqual(chosen.at(-1),{conv:'r2',topic:''});pressedOnly(other);assert.equal(fresh.getAttribute('aria-pressed'),'false');}
 const before=chosen.length;controls.stop();controls.repaint();fresh.click();assert.equal(chosen.length,before);assert.equal(f.root.querySelector('.topics-bar'),null);assert.equal(calls.length,0);
}
console.log('PASS fresh topic projection: native/person Main and chips exclude New, selection resets fresh, stable repaint and stopped zero-request behavior');
