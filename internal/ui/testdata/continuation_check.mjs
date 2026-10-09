import assert from 'node:assert/strict';
import {Engine,memoryStore} from '../static/engine.mjs';
import * as wire from '../static/wire.mjs';
async function fixture() {
 const store=memoryStore(),posts=[],e=new Engine({store,base:'https://isolated.invalid',fetch:async(url,{body})=>{assert(url.endsWith('/v1/messages'));posts.push(body);return new Response(JSON.stringify({state:'custody'}));}});
 e.keys=await wire.newKeys();e.address='owner/phone';e.fp=await wire.fingerprint(await wire.publicEntry(e.keys,e.address));
 const recipient=await wire.publicEntry(await wire.newKeys(),'owner/desk'),pin={fingerprint:await wire.fingerprint(recipient)};
 e.me={state:'self',person:'a'.repeat(32),human_keys:[e.fp],devices:[{address:e.address,fingerprint:e.fp},{address:recipient.address,fingerprint:pin.fingerprint}]};
 e.sendKey=async()=>pin;e.pinned=async()=>pin;e.pubOf=async()=>recipient;e.ctlSupport=async(_address,_pin,cap)=>{assert.equal(cap,wire.CapContinuation);return [true,''];};
 return {e,store,posts,pin};
}
{
 const {e,pin}=await fixture(),m={id:'1'.repeat(32),lid:'2'.repeat(32),kind:'task',target:{address:'owner/desk',fingerprint:pin.fingerprint}},ref={id:m.lid,fingerprint:e.fp};
 const status={state:'needs_human',n:17,at:1700000000,attempt:3};
 const exec=e.execOn([{id:'3'.repeat(32),sub:wire.SubStatus,body:JSON.stringify(status),ref}],m.target.address,false);
 assert.equal(exec.attempt,3,'attempt is the host execution attempt, never the status counter');
 assert.deepEqual(e.continuationOf(m,e.fp,exec),{id:m.lid,key:e.fp,host:'owner/desk',attempt:3});
 assert.equal(e.continuationOf({...m,kind:'message'},e.fp,exec),null,'ordinary quoted messages stay inert');
 assert.equal(e.continuationOf(m,e.fp,{...exec,state:'answered'}),null);
 assert.equal(e.continuationOf(m,e.fp,{...exec,attempt:0}),null,'an older or unknown attempt cannot offer a continuation');
 e.me.human_keys=[];assert.equal(e.continuationOf(m,e.fp,exec),null,'agent host source has no continuation authority');e.stop();
}
{
 const {e,store,posts}=await fixture(),x={host:'owner/desk',id:'4'.repeat(32),key:e.fp,action:'continue',expect:'needs_human',attempt:1,text:'release budget',send_id:'5'.repeat(32)};
 await e.decide(x);await e.decide(x);assert.equal(posts.length,1,'stable answer retry does not resend or create a new decision');
 const row=await store.get('outbox',x.send_id);assert.equal(row.required_cap,wire.CapContinuation);assert.equal(JSON.parse(row.body).text,x.text);
 await assert.rejects(e.decide({...x,text:'different answer'}),/different decision/);
 const restarted=new Engine({store,base:e.base,fetch:e.fetch});Object.assign(restarted,{keys:e.keys,address:e.address,fp:e.fp,me:e.me,sendKey:e.sendKey,pubOf:e.pubOf,ctlSupport:e.ctlSupport,pinned:e.pinned});
 await restarted.decide(x);assert.equal(posts.length,1,'restart reuses exact durable decision');
 restarted.stop();e.stop();
}
{
 const {e,store,posts}=await fixture(),x={host:'owner/desk',id:'6'.repeat(32),key:e.fp,action:'continue',expect:'needs_human',attempt:1,send_id:'7'.repeat(32)};
 const results=await Promise.allSettled([e.decide({...x,text:'one'}),e.decide({...x,text:'two'})]);
 assert.equal(results.filter(r=>r.status==='fulfilled').length,1);assert.equal(posts.length,1);
 assert(['one','two'].includes(JSON.parse((await store.get('outbox',x.send_id)).body).text));e.stop();
}
{
 const {e,store,posts}=await fixture();e.ctlSupport=async()=>[false,'peer_update: update AgentNet on the host to continue waiting requests'];
 await assert.rejects(e.decide({host:'owner/desk',id:'8'.repeat(32),key:e.fp,action:'continue',expect:'needs_human',attempt:1,text:'answer',send_id:'9'.repeat(32)}),/peer_update/);
 assert.equal((await store.all('outbox')).length,0);assert.equal(posts.length,0,'unsupported host has no plain-message fallback');e.stop();
}
console.log('continuation checks passed');
{
 const {e,store,posts}=await fixture(),x={host:'owner/desk',id:'a'.repeat(32),key:e.fp,action:'resolve',expect:'needs_human',attempt:2,send_id:'b'.repeat(32)};
 e.ctlSupport=async(_host,_pin,cap)=>{assert.equal(cap,wire.CapOwnSyncV3);return [true,''];};
 await e.decide(x);await e.decide(x);
 assert.equal(posts.length,1,'exact resolution retry reuses durable decision');
 const row=await store.get('outbox',x.send_id);assert.equal(row.required_cap,wire.CapOwnSyncV3);assert.equal(JSON.parse(row.body).action,'resolve');
 assert.equal((await store.all('inbox')).length,0,'sending does not claim a resolved host status');
 await assert.rejects(e.decide({...x,attempt:3}),/different decision/);
 e.me.human_keys=[];await assert.rejects(e.decide({...x,send_id:'c'.repeat(32)}),/current own human/);
 e.stop();
}
console.log('own-human resolution checks passed');
{
 const {e,store,posts,pin}=await fixture(),x={host:'owner/desk',id:'c'.repeat(32),key:e.fp,action:'resolve',expect:'needs_human',attempt:2,send_id:'d'.repeat(32)};
 const fetch=e.fetch;e.fetch=async()=>{throw Error('synthetic offline');};
 e.ctlSupport=async()=>[true,''];await e.decide(x);
 const row=await store.get('outbox',x.send_id);assert.equal(row.state,'queued');e.fetch=fetch;
 e.ctlSupport=async()=>[false,'peer_update: own3 unavailable'];await e.post(row);
 assert.equal(posts.length,0,'a queued resolution cannot reach a downgraded host');
 assert.match((await store.get('outbox',x.send_id)).detail,/peer_update/);
 e.ctlSupport=async()=>[true,''];e.pinned=async()=>({...pin,fingerprint:'e'.repeat(64)});
 await assert.rejects(e.decide(x),/current verified key/);
 assert.equal(posts.length,0,'explicit retry cannot move a resolution to a changed key');e.stop();
}
console.log('queued resolution capability and key checks passed');
