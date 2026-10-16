import assert from 'node:assert/strict';
import {registerHooks,stripTypeScriptTypes} from 'node:module';
import {readFileSync} from 'node:fs';
import {pathToFileURL} from 'node:url';
const root=new URL('../',import.meta.url);
registerHooks({load(url,context,next){if(url.includes('/web/src/')&&url.endsWith('.ts'))return{format:'module',shortCircuit:true,source:stripTypeScriptTypes(readFileSync(new URL(url),'utf8').replace('constructor(readonly host: Host) {','constructor(host: Host) { this.host = host;'))};return next(url,context);},resolve(specifier,context,next){const parent=context.parentURL||'';if(parent.includes('/web/src/')){if(specifier==='react')return next('data:text/javascript,export const useSyncExternalStore=()=>{};',context);if(specifier==='./skin/brand')return next('data:text/javascript,export const skin={storage:"agentnet.messenger"};',context);if(specifier==='./optimistic.mjs')return next(new URL('static/optimistic.mjs',root).href,context);if(specifier==='./person-topics.mjs')return next(new URL('skins/shared/person-topics.mjs',root).href,context);if(specifier==='./linkText')return next(new URL('skins/shared/link-text.mjs',root).href,context);if(specifier.startsWith('.')&&!/\.(ts|tsx|mjs|js)$/.test(specifier))return next(new URL(specifier+'.ts',parent).href,context);}return next(specifier,context);}});
const {Store,unsentIn}=await import('../web/src/store.ts');
const {captureSchedule,submitSchedule}=await import('../web/src/features/Composer.schedule-model.ts');
const {scheduleSaves}=await import('../web/src/schedule-saves.ts');
const saved=new Map();let denied=false;
globalThis.localStorage={getItem:k=>saved.get(k)||null,setItem:(k,v)=>{if(denied)throw Error('denied');saved.set(k,v);},removeItem:k=>{if(denied)throw Error('denied');saved.delete(k);}};
globalThis.document={visibilityState:'hidden',hasFocus:()=>false,addEventListener(){},removeEventListener(){}};
const conv='a'.repeat(64),other='b'.repeat(64),file=new File(['original bytes'],'exact.txt');
const original={text:'original',topic:'topic',replyTo:'reply',files:[{key:'one',name:file.name,size:file.size,type:'',file}]};
let serial=0;
const attempt=(draft=original,body=draft.text)=>captureSchedule((++serial).toString(16).padStart(32,'0'),conv,body,draft,new Date(Date.now()+3600000).toISOString(),'UTC',{persons:['approved'],pids:[]});
function fixture(id='ws'+serial,state={}){const calls=[];const host={version:1,platform:'daemon',workspace:{id,handle:'exact'},workspaces:{state:()=>state},listen:()=>()=>{},api:async(path,body)=>{calls.push({path,body});if(path.startsWith('/api/overview'))return{version:'test',seq:1,me:{address:'me/device'},people:[],agents:[],threads:[],dms:[],review:[],needs_you:[],held:[],reminders:[],directory:{members:[]}};if(path.startsWith('/api/typing'))return{entries:[]};if(path.startsWith('/api/agents'))return{agents:[]};return[];}};return{host,calls,state};}
// Navigation unmount does not own settlement or choose a new chat.
{
 const f=fixture('navigation'),s=new Store(f.host),a=attempt();s.setDraft(conv,original);s.setDraft(other,{text:'other unchanged'});s.beginScheduleSave(conv,a);s.get().open={kind:'dm',id:other};const open=s.get().open;
 assert(unsentIn(s.get()));s.settleScheduleSave(conv,a.payload.id,'confirmed');assert.equal(s.draft(conv).text,'');assert.equal(s.draft(conv).files.length,0);assert.equal(s.draft(other).text,'other unchanged');assert.equal(s.get().open,open);assert(!s.get().scheduleSaves[conv]);s.stop();
}
// Stop -> late success mutates shared holder only; next start clears once.
{
 const f=fixture('late'),s=new Store(f.host),a=attempt();s.setDraft(conv,original);s.beginScheduleSave(conv,a);s.stop();const before=s.get();let mutations=0;s.subscribe(()=>mutations++);s.settleScheduleSave(conv,a.payload.id,'confirmed');assert.equal(s.get(),before);assert.equal(mutations,0);assert.equal(f.calls.length,0);
 const next=new Store({...f.host});let clears=0;const set=next.setDraft.bind(next);next.setDraft=(...args)=>{clears++;return set(...args);};await next.start();assert.equal(next.draft(conv).text,'');assert.equal(clears,1);assert.equal(f.calls.some(c=>c.path.startsWith('/api/schedule')),false);next.stop();
}
// A newer renderer's changed callback survives disposal of the old renderer.
{
 const f=fixture('callbacks'),a=attempt(),one=new Store(f.host);one.setDraft(conv,original);one.beginScheduleSave(conv,a);const two=new Store({...f.host});two.setDraft(conv,{...original,text:'new typing',files:[...original.files,{key:'new',file:new File(['new'],'new.txt')}]});one.stop();one.settleScheduleSave(conv,a.payload.id,'confirmed');assert.equal(two.draft(conv).text,'new typing');assert.equal(two.draft(conv).files.length,1);assert.equal(two.draft(conv).files[0].key,'new');assert(!two.get().scheduleSaves[conv]);two.stop();
}
// Lost response, navigation/remount, same-ID retry, no new intent or audience.
{
 const f=fixture('lost'),s=new Store(f.host),a=attempt();s.setDraft(conv,original);s.beginScheduleSave(conv,a);s.settleScheduleSave(conv,a.payload.id,'uncertain');s.stop();const next=new Store({...f.host});const slot=next.get().scheduleSaves[conv];assert.equal(slot.attempt,a);assert.throws(()=>next.beginScheduleSave(conv,attempt()),/original/);assert.deepEqual(slot.attempt.payload.audience.persons,['approved']);assert(Object.isFrozen(a.payload.audience.persons));
 let stages=0,ids=[];const api={stage:async f=>{assert.equal(f,file);return 'new-'+(++stages);},discard:async()=>{},createSchedule:async d=>{ids.push(d.id);return{id:d.id};}};next.beginScheduleSave(conv,a);await submitSchedule(api,a);next.settleScheduleSave(conv,a.payload.id,'confirmed');assert.equal(ids[0],a.payload.id);assert.equal(stages,1);assert.equal(next.draft(conv).text,'');next.stop();
}
// Topic mismatch, new reply/text, and exact actual File identity remain guarded.
for(const patch of [{topic:'new topic'},{newTopic:true},{text:'new text',replyTo:'new reply'},{files:[{...original.files[0],file:new File(['different'],'exact.txt')}]}]){
 const f=fixture('changed'+serial),s=new Store(f.host),a=attempt();s.setDraft(conv,{...original,...patch});s.beginScheduleSave(conv,a);s.settleScheduleSave(conv,a.payload.id,'confirmed');const now=s.draft(conv);if(patch.topic||patch.newTopic)assert.equal(now.text,original.text);if(patch.text)assert.equal(now.text,patch.text);if(patch.replyTo)assert.equal(now.replyTo,patch.replyTo);if(patch.files)assert.equal(now.files[0].file,patch.files[0].file);s.stop();
}
// Old-ID completion cannot clear a replacement slot/token.
{
 const f=fixture('stale'),s=new Store(f.host),a=attempt(),b=attempt();s.beginScheduleSave(conv,a);s.settleScheduleSave(conv,a.payload.id,'refused');s.beginScheduleSave(conv,b);s.settleScheduleSave(conv,a.payload.id,'confirmed');assert.equal(s.get().scheduleSaves[conv].id,b.payload.id);assert(saved.get('agentnet.messenger.schedule-save.stale').includes(b.payload.id));s.stop();
}
// Cold token startup has zero API calls, no Files/retry, and refuses a new ID.
{
 const id='f'.repeat(32),key='agentnet.messenger.schedule-save.reload';saved.set(key,JSON.stringify({v:1,conv:{[conv]:{id,conv,files:[{name:'exact.txt',size:14}]},[other]:{id:'INVALID',conv:other,files:[]}}}));
 const f=fixture('reload'),s=new Store(f.host);s.setDraft(conv,{text:'kept draft'});assert.equal(f.calls.length,0);assert.equal(s.get().scheduleSaves[conv].attempt,undefined);assert(!s.get().scheduleSaves[other]);assert.throws(()=>s.beginScheduleSave(conv,attempt()),/original/);s.settleScheduleSave(conv,id,'uncertain','Not confirmed; may commit later');assert(saved.get(key).includes(id));assert.equal(s.draft(conv).text,'kept draft');s.forgetScheduleSave(conv,id);assert.equal(s.draft(conv).text,'kept draft');assert(!s.get().scheduleSaves[conv]);assert(!saved.has(key));s.stop();
}
// Cold metadata survives reload without in-memory originals to lose.
{
 const id='e'.repeat(32),key='agentnet.messenger.schedule-save.cold-update';
 saved.set(key,JSON.stringify({v:1,conv:{[conv]:{id,conv,files:[{name:'remembered.txt',size:7}]}}}));
 const before=saved.get(key),first=fixture('cold-update'),cold=new Store(first.host);
 assert.equal(cold.get().scheduleSaves[conv].attempt,undefined);
 assert.equal(unsentIn(cold.get()),false,'cold token alone must allow browser self-update');
 assert.equal(first.calls.length,0);assert.throws(()=>cold.beginScheduleSave(conv,attempt()),/original/);cold.stop();
 const fresh=fixture('cold-update'),reloaded=new Store(fresh.host),slot=reloaded.get().scheduleSaves[conv];
 assert.equal(slot.id,id);assert.equal(slot.status,'uncertain');assert.equal(slot.attempt,undefined);
 assert.equal(unsentIn(reloaded.get()),false,'fresh ledger still allows reload with the durable cold token');
 assert.equal(saved.get(key),before,'reload retains the exact token');assert.equal(fresh.calls.length,0);
 assert.throws(()=>reloaded.beginScheduleSave(conv,attempt()),/original/,'cold recovery still blocks a replacement ID');
 reloaded.settleScheduleSave(conv,id,'uncertain','Not confirmed; a previous request may still commit');
 assert.equal(saved.get(key),before,'unconfirmed lookup outcome keeps the token');assert.equal(unsentIn(reloaded.get()),false);
 reloaded.setDraft(conv,{text:'ordinary draft'});assert(unsentIn(reloaded.get()),'ordinary draft guard remains');
 reloaded.setDraft(conv,{text:''});assert.equal(unsentIn(reloaded.get()),false);
 assert(unsentIn({...reloaded.get(),unsent:true}),'ordinary pending-send guard remains');
 reloaded.forgetScheduleSave(conv,id);assert(!saved.has(key));reloaded.stop();
}
// An actual attempt alone (without ordinary draft text/files) still guards reload.
{
 const f=fixture('memory-update'),s=new Store(f.host),a=attempt();
 assert.equal(Object.keys(s.get().drafts).length,0);s.beginScheduleSave(conv,a);
 assert(unsentIn(s.get()),'in-memory saving originals must guard reload');
 s.settleScheduleSave(conv,a.payload.id,'uncertain');assert(unsentIn(s.get()),'in-memory uncertain originals must guard reload');
 assert.equal(s.get().scheduleSaves[conv].attempt,a);s.forgetScheduleSave(conv,a.payload.id);assert.equal(unsentIn(s.get()),false);s.stop();
}
// Denied persistence still guards self-reload and permits explicit same-ID save.
{
 denied=true;const f=fixture('denied'),s=new Store(f.host),a=attempt();s.setDraft(conv,{text:''});s.beginScheduleSave(conv,a);assert.equal(s.get().scheduleSaves[conv].durable,false);assert(unsentIn(s.get()));s.settleScheduleSave(conv,a.payload.id,'uncertain');assert(unsentIn(s.get()));s.forgetScheduleSave(conv,a.payload.id);assert(!unsentIn(s.get()));s.stop();denied=false;
}
// Handle changes detach old notifications and cannot erase the new recovery token.
{
 const f=fixture('handle'),a=attempt(),old=scheduleSaves(f.host,()=>{});old.begin(conv,a);const otherHost={...f.host,workspace:{...f.host.workspace,handle:'new-handle'}};let changed=0;const current=scheduleSaves(otherHost,()=>changed++);old.settle(conv,a.payload.id,'confirmed');assert.equal(changed,0);assert(current.snapshot()[conv]);assert(saved.get('agentnet.messenger.schedule-save.handle').includes(a.payload.id));assert.throws(()=>old.begin(conv,a),/handle changed/);current.dispose();old.dispose();
}
console.log('PASS actual Store/holder lifetime: navigation, stopped late settle, callback ownership, same-ID remount, changed draft/topic/File, stale-ID token fence, cold no-auto guard, denied-storage unsent and workspace handle isolation');

// A purpose key is shared across skins, while draft confirmation belongs to its creator.
{
 const {scheduleSaves:standalone}=await import('../skins/shared/schedule-saves.mjs');
 const f=fixture('cross-skin'),a=attempt(),comic=scheduleSaves(f.host,()=>{}),skin=standalone({...f.host},()=>{},'standalone.test');
 comic.begin(conv,a);comic.settle(conv,a.payload.id,'uncertain');
 assert.throws(()=>skin.begin(conv,attempt()),/original/,'B cannot allocate over A uncertainty');
 assert.equal(skin.snapshot()[conv].owner,'agentnet.messenger');assert.equal(skin.snapshot()[conv].attempt,a);
 skin.begin(conv,a);assert.equal(skin.snapshot()[conv].owner,'agentnet.messenger','same-ID recovery keeps creating draft namespace');
 skin.settle(conv,a.payload.id,'confirmed');assert(!saved.has('agentnet.messenger.schedule-save.cross-skin'));
 assert.deepEqual(skin.takeConfirmed(),[],'B must not consume A confirmation');
 assert.throws(()=>skin.begin(conv,attempt()),/original/,'foreign confirmation stays for A');
 assert.equal(comic.takeConfirmed()[0].attempt,a);assert.deepEqual(comic.takeConfirmed(),[]);
 skin.dispose();comic.dispose();
}
for(const kind of ['comic','standalone']){
 const key='agentnet.messenger.schedule-save.cold-'+kind,id='d'.repeat(32);
 saved.set(key,JSON.stringify({v:1,conv:{[conv]:{id,conv,files:[{name:'cold.txt',size:4}]}}}));
 const f=fixture('cold-'+kind),{scheduleSaves:standalone}=await import('../skins/shared/schedule-saves.mjs');
 const ledger=kind==='comic'?scheduleSaves(f.host,()=>{}):standalone(f.host,()=>{},'standalone.test');
 assert.equal(ledger.snapshot()[conv].id,id);assert.equal(ledger.snapshot()[conv].attempt,undefined);assert.equal(ledger.snapshot()[conv].owner,undefined);assert.equal(f.calls.length,0);
 ledger.settle(conv,id,'uncertain');assert(saved.get(key).includes(id));assert.equal(f.calls.length,0);ledger.dispose();
}
console.log('PASS universal purpose key, cross-skin exact File/attempt ownership, foreign confirmed preservation, same-ID settlement and cold no-auto recovery in both renderers');
