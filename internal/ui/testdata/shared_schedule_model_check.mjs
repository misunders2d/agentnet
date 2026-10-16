import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {stripTypeScriptTypes} from 'node:module';
import {scheduleSaves} from '../skins/shared/schedule-saves.mjs';
import {captureSchedule,submitSchedule,clearScheduledDraft} from '../skins/shared/schedule-model.mjs';
const read=p=>readFileSync(new URL(p,import.meta.url),'utf8');
assert.equal(read('../skins/shared/schedule-model.mjs'),stripTypeScriptTypes(read('../web/src/features/Composer.schedule-model.ts')),'shared model must mechanically derive from Comic');
const derived=stripTypeScriptTypes(read('../web/src/schedule-saves.ts'))
 .replace('import { skin as brand } from "./skin/brand";','')
 .replace('scheduleSaves(host     , changed         )','scheduleSaves(host     , changed         , owner)')
 .replace('  const owner=brand.storage;','');
assert.equal(read('../skins/shared/schedule-saves.mjs'),derived,'ledger differs only by injected draft owner, never by persistence or lifetime rules');
// Run the existing Comic model vectors against this actual shared implementation.
const vectors=read('./composer_schedule_check.mjs').replace("const source=readFileSync(new URL('../web/src/features/Composer.schedule-model.ts',import.meta.url),'utf8');",'const source='+JSON.stringify(read('../skins/shared/schedule-model.mjs'))+';');
await import('data:text/javascript,'+encodeURIComponent(vectors));
const saved=new Map();let denied=false;
globalThis.localStorage={getItem:k=>saved.get(k)||null,setItem:(k,v)=>{if(denied)throw Error('denied');saved.set(k,v);},removeItem:k=>{if(denied)throw Error('denied');saved.delete(k);}};
const conv='a'.repeat(64),file=new File(['private actual bytes'],'same.txt'),draft={text:'private body',topic:'topic',replyTo:'quote',agent:'agent',files:[{key:'one',name:file.name,size:file.size,file}]};
let serial=0;const attempt=()=>captureSchedule((++serial).toString(16).padStart(32,'0'),conv,draft.text,draft,new Date(Date.now()+3600000).toISOString(),'UTC',{persons:['approved'],pids:[]});
const fixture=(id,state={})=>({workspace:{id,handle:'h'},workspaces:{state:()=>state}});
{
 const host=fixture('portable'),a=attempt();let old=0,current=0;
 const one=scheduleSaves(host,()=>old++,'skin.a');one.begin(conv,a);one.settle(conv,a.payload.id,'uncertain');
 const two=scheduleSaves({...host},()=>current++,'skin.b');one.dispose();assert.equal(two.snapshot()[conv].attempt,a);
 assert.throws(()=>two.begin(conv,attempt()),/original/);two.begin(conv,a);assert.equal(two.snapshot()[conv].owner,'skin.a');two.settle(conv,a.payload.id,'confirmed');
 assert.equal(old,2);assert.equal(current,2);assert.equal(saved.has('agentnet.messenger.schedule-save.portable'),false);assert.deepEqual(two.takeConfirmed(),[]);assert.throws(()=>two.begin(conv,attempt()),/original/);
 assert.equal(one.takeConfirmed()[0].attempt,a);assert.deepEqual(two.takeConfirmed(),[]);one.dispose();two.dispose();
}
{
 const a=attempt(),host=fixture('token'),ledger=scheduleSaves(host,()=>{},'skin.a');ledger.begin(conv,a);
 const raw=saved.get('agentnet.messenger.schedule-save.token'),token=JSON.parse(raw);assert.deepEqual(Object.keys(token.conv[conv]).sort(),['conv','files','id']);assert.deepEqual(token.conv[conv].files,[{name:file.name,size:file.size}]);assert(!raw.includes('private'));assert(!raw.includes('owner'));ledger.dispose();
 const cold=scheduleSaves(fixture('token'),()=>{},'skin.b');assert.equal(cold.snapshot()[conv].attempt,undefined);assert.throws(()=>cold.begin(conv,attempt()),/original/);cold.settle(conv,a.payload.id,'uncertain','Not confirmed');assert.equal(saved.get('agentnet.messenger.schedule-save.token'),raw);cold.dispose();
}
{
 const host=fixture('stale'),a=attempt(),b=attempt(),one=scheduleSaves(host,()=>{},'a');one.begin(conv,a);one.settle(conv,a.payload.id,'refused');one.begin(conv,b);one.settle(conv,a.payload.id,'confirmed');assert.equal(one.snapshot()[conv].attempt,b);
 const nextHost={...host,workspace:{...host.workspace,handle:'new'}},two=scheduleSaves(nextHost,()=>{},'a');one.settle(conv,b.payload.id,'confirmed');assert(two.snapshot()[conv]);assert(saved.get('agentnet.messenger.schedule-save.stale').includes(b.payload.id));assert.throws(()=>one.begin(conv,a),/handle/);one.dispose();two.dispose();
}
{
 denied=true;const a=attempt(),ledger=scheduleSaves(fixture('denied'),()=>{},'a');ledger.begin(conv,a);assert.equal(ledger.snapshot()[conv].durable,false);ledger.settle(conv,a.payload.id,'uncertain');assert.equal(ledger.snapshot()[conv].attempt,a);ledger.forget(conv,a.payload.id);denied=false;ledger.dispose();
}
// Partial staging discards only native handles already obtained, never file descriptors.
{
 const second=new File(['two'],'two.txt'),a=captureSchedule('f'.repeat(32),conv,'files',{...draft,files:[...draft.files,{key:'two',file:second}]},new Date(Date.now()+3600000).toISOString(),'UTC',{persons:[],pids:[]});
 const discarded=[];let count=0,posts=0;await assert.rejects(submitSchedule({stage:async()=>{if(++count===2)throw Error('stage failure');return 'one-use';},discard:async ids=>discarded.push(ids),createSchedule:async()=>{posts++;}},a),/stage failure/);assert.deepEqual(discarded,[['one-use']]);assert.equal(posts,0);
 const changed={...draft,text:'new typing',replyTo:'new reply',files:[{...draft.files[0],file:new File(['different'],'same.txt')}]};const cleared=clearScheduledDraft(changed,a);assert.equal(cleared.text,'new typing');assert.equal(cleared.replyTo,'new reply');assert.equal(cleared.files[0].file,changed.files[0].file);
}
// Exact token validators, including purpose-key parity and no hidden private payload.
for(const bad of [{id:'bad'}, {body:'private'}, {files:[{name:'x',size:-1}]}, {files:[{name:'x',size:1,sha256:'not-a-token'}]}, {files:Array.from({length:9},()=>({name:'x',size:1}))}]){
 const id='e'.repeat(32),ws='invalid-'+serial++,key='agentnet.messenger.schedule-save.'+ws;
 saved.set(key,JSON.stringify({v:1,conv:{[conv]:{id,conv,files:[],...bad}}}));const ledger=scheduleSaves(fixture(ws),()=>{},'a');assert.deepEqual(ledger.snapshot(),{});ledger.dispose();
}
console.log('PASS shared mechanical source parity + reused Comic vectors + cross-skin owner/ID/handle fences, metadata-only cold recovery, denied persistence, partial-stage cleanup and guarded draft/File identity');
