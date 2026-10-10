import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { runInNewContext } from 'node:vm';
const script = await readFile(new URL('./android.js', import.meta.url), 'utf8');
const window = {};
runInNewContext(script, {window});
assert.equal(window.__agentnetPlatform, undefined, 'ordinary browsers must not become Android hosts');
const sent = [], events = new Map(), errors = [];
class Anchor { constructor(href) { this.href=href; this.download='report.md'; } hasAttribute(n) { return n === 'download'; } }
const bridge = {postMessage(value) { sent.push(JSON.parse(value)); }};
window.AgentNetAndroid=bridge; window.alert=e=>errors.push(e);
const document = {addEventListener:(name,fn)=>events.set(name,fn)};
runInNewContext(script, {window,document,location:{origin:'http://127.0.0.1:40000'},HTMLAnchorElement:Anchor,
 Uint8Array, btoa:s=>Buffer.from(s,'binary').toString('base64'), fetch:async()=>({ok:true,blob:async()=>new Blob(['x'.repeat(66000)])})});
assert.equal(window.__agentnetPlatform, 'android');
const first=window.__agentnetAndroid.connection(), second=window.__agentnetAndroid.notificationStatus();
bridge.onmessage({data:JSON.stringify({id:sent[1].id,ok:true,granted:true})});
assert.equal((await second).granted,true);
bridge.onmessage({data:'bad-json'});
bridge.onmessage({data:JSON.stringify({id:'unknown',ok:true})});
bridge.onmessage({data:JSON.stringify({id:sent[0].id,ok:true,enabled:false})});
assert.equal((await first).enabled,false,'out-of-order replies keep exact call identity');
const failed=window.__agentnetAndroid.setConnection(true);
bridge.onmessage({data:JSON.stringify({id:sent.at(-1).id,ok:false,error:'permission denied'})});
await assert.rejects(failed,/permission denied/);
assert.equal(window.agentnetNativeBack(),false);
const remove=window.__agentnetOnBack(()=>true);
assert.equal(window.agentnetNativeBack(),true);remove();assert.equal(window.agentnetNativeBack(),false);
let prevented=false;
events.get('click')({composedPath:()=>[new Anchor('blob:https://attacker.test/file')],preventDefault:()=>{prevented=true;}});
assert.equal(prevented,false,'foreign blob never crosses native export bridge');
const start=sent.length;
events.get('click')({composedPath:()=>[new Anchor('blob:http://127.0.0.1:40000/file')],preventDefault:()=>{prevented=true;}});
assert.equal(prevented,true);
const tick=()=>new Promise(resolve=>setImmediate(resolve));
await tick();assert.equal(sent[start].type,'export:start');assert.equal(sent[start].size,66000);
bridge.onmessage({data:JSON.stringify({id:sent[start].id,ok:true,transfer:'one'})});
let received='';let next=start+1;
for (let i=0;i<3;i++) {
 await tick();const part=sent[next++];assert.equal(part.type,'export:chunk');assert.equal(part.transfer,'one');
 const data=Buffer.from(part.data,'base64').toString();assert.ok(data.length<=32768);received+=data;
 bridge.onmessage({data:JSON.stringify({id:part.id,ok:true,written:received.length})});
}
await tick();assert.equal(received,'x'.repeat(66000));assert.equal(sent[next].type,'export:finish');
bridge.onmessage({data:JSON.stringify({id:sent[next].id,ok:true,saved:false})});
await tick();assert.deepEqual(errors,[],'cancelled Android save is not an error');
console.log('Android shared-host bridge: correlation, Back, origin and bounded export passed');
