import assert from 'node:assert/strict';
import { legacyDraft } from './legacy-drafts.mjs';
const id='a'.repeat(32), conv='b'.repeat(64);
const entry=(key,value)=>({key,value:JSON.stringify(value)});
assert.equal(legacyDraft(entry('dm:'+conv,{body:'draft'})).body,'draft');
const dm=legacyDraft(entry('dm:'+conv,{body:'send',request:{id,body:'send',conv}}));
assert.equal(dm.endpoint,'/api/dm/send');assert.equal(dm.request.id,id);
const req={id,body:'ask',to:'test/laptop',kind:'question',reply_to:id,agent_id:'my-agent'};
assert.deepEqual(legacyDraft(entry('thread:'+id,{body:'ask',request:req})).request,req);
for(const request of [{...req,id:'x'}, {...req,reply_to:'c'.repeat(32)}, {...req,body:'other'}, {...req,accept:true}, {...req,kind:'accept'}]) {
 assert.throws(()=>legacyDraft(entry('thread:'+id,{body:'ask',request})),/safely/);
}
assert.throws(()=>legacyDraft(entry('dm:'+conv,{body:'send',request:{id,body:'send',conv:'c'.repeat(64)}})),/safely/);
assert.throws(()=>legacyDraft(entry('../other',{body:'x'})));
console.log('Legacy drafts: exact target/ID/kind/body preserved; changed bindings and unknown authority refused');
