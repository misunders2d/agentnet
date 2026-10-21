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
for (const [action,result] of [['status',{version:'v0.8.17',app_update_supported:true}],['check',{state:'available',latest:'v0.8.18'}],['update',{state:'installer',message:'Android installer opened.'}]]) {
 const pending=window.__agentnetNativeAppJSON(action,action==='update'?{}:undefined);
 assert.equal(sent.at(-1).type,'app:'+action);
 assert.deepEqual(Object.keys(sent.at(-1)).sort(),['id','type'],'native owns installation selection');
 bridge.onmessage({data:JSON.stringify({id:sent.at(-1).id,ok:true,result})});
 assert.deepEqual(JSON.parse(JSON.stringify(await pending)),result);
}
const beforeInvalid=sent.length;
await assert.rejects(window.__agentnetNativeAppJSON('cli'),/Unsupported/);
for (const body of [{url:'https://attacker.test/update.apk'},{path:'/data/foreign.apk'},{version:'v99.0.0'},null,[],'']) {
 await assert.rejects(window.__agentnetNativeAppJSON('update',body),/installation options/);
}
assert.equal(sent.length,beforeInvalid,'invalid app actions never reach native');
const updateFailure=window.__agentnetNativeAppJSON('update',{});
bridge.onmessage({data:JSON.stringify({id:sent.at(-1).id,ok:false,error:'Release APK is unavailable. Try again later.'})});
await assert.rejects(updateFailure,/APK is unavailable/);
// Keep the existing exact-correlation assertions independent of added calls.
sent.length=0;
const first=window.__agentnetAndroid.connection(), second=window.__agentnetAndroid.notificationStatus();
bridge.onmessage({data:JSON.stringify({id:sent[1].id,ok:true,granted:true,app_granted:true,channel:{importance:3,sound:true,vibration:false}})});
const actualStatus=await second;
assert.equal(actualStatus.granted,true);
assert.deepEqual(JSON.parse(JSON.stringify(actualStatus.channel)),{importance:3,sound:true,vibration:false},'legacy channel choices reach the skin unchanged');
bridge.onmessage({data:'bad-json'});
bridge.onmessage({data:JSON.stringify({id:'unknown',ok:true})});
bridge.onmessage({data:JSON.stringify({id:sent[0].id,ok:true,enabled:false,badging:true})});
const connectionStatus=await first;
assert.equal(connectionStatus.enabled,false,'out-of-order replies keep exact call identity');
assert.equal(connectionStatus.badging,true,'saved connection badging reaches the skin without changing it');
const connectionSettings=window.__agentnetAndroid.connectionSettings();
assert.equal(sent.at(-1).type,'connection:settings');
assert.deepEqual(Object.keys(sent.at(-1)).sort(),['id','type'],'native alone chooses the connection channel');
bridge.onmessage({data:JSON.stringify({id:sent.at(-1).id,ok:true,opened:true})});
assert.equal((await connectionSettings).opened,true);
const settings=window.__agentnetAndroid.notificationSettings();
assert.equal(sent.at(-1).type,'notifications:settings');
assert.deepEqual(Object.keys(sent.at(-1)).sort(),['id','type'],'channel settings target is chosen by native, not supplied by a skin');
bridge.onmessage({data:JSON.stringify({id:sent.at(-1).id,ok:true,opened:true})});
assert.equal((await settings).opened,true);
const failed=window.__agentnetAndroid.setConnection(true);
bridge.onmessage({data:JSON.stringify({id:sent.at(-1).id,ok:false,error:'permission denied'})});
await assert.rejects(failed,/permission denied/);
const scan=window.__agentnetAndroid.scanQR();
assert.equal(sent.at(-1).type,'qr:scan');
bridge.onmessage({data:JSON.stringify({id:sent.at(-1).id,ok:true,text:'agentnet-link-v2:test'})});
assert.equal((await scan).text,'agentnet-link-v2:test','scan returns text without opening or enrolling');
const canceledScan=window.__agentnetAndroid.scanQR();
bridge.onmessage({data:JSON.stringify({id:sent.at(-1).id,ok:true,canceled:true})});
assert.equal((await canceledScan).canceled,true);
// Foreground state reaches skins through host.android only (no page globals or events).
assert.equal(window.__agentnetAndroid.visible(),true);
const seenVisible=[];const stopVisible=window.__agentnetAndroid.onVisibility(v=>seenVisible.push(v));
window.agentnetNativeVisibility(false);assert.equal(window.__agentnetAndroid.visible(),false);
window.agentnetNativeVisibility(1);assert.equal(window.__agentnetAndroid.visible(),true);
stopVisible();window.agentnetNativeVisibility(false);
assert.deepEqual(seenVisible,[false,true],'onVisibility reports each change until unsubscribed');
assert.equal('__agentnetNativeVisible' in window,false);window.agentnetNativeVisibility(true);
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
