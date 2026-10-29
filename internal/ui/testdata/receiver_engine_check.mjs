import {Engine,memoryStore} from '../static/engine.mjs';
const e=new Engine({store:memoryStore(),base:'http://127.0.0.1:1',fetch:()=>{throw Error('unexpected network');}});
let sends=0;for(const name of ['sendDM','askAgent'])e[name]=async()=>{sends++;return{state:'sent'};};
for(const route of ['/api/dm/send','/api/dm/agent/ask']) {
 for(const receiver of [{kind:'managed_agent',agent_id:'a'.repeat(32),instructions:'local only',mode:'question'},{kind:'live_session',session_handle:'opaque'},{kind:'human',binding_id:'takeover'},{kind:'human',preset:'injected'},'invalid']) {
  const before=sends;let refused=false;try{await e.api(route,{body:'request',files:[{name:'exact-file',bytes:new Uint8Array([1,2,3])}],reply_receiver:receiver});}catch(err){refused=err.message.includes('reply receiver');}
  if(!refused||sends!==before)throw Error('unsupported reply receiver silently ignored on '+route);
 }
 await e.api(route,{body:'ordinary human path',reply_receiver:{kind:'human'}});
}
if(sends!==2)throw Error('explicit human path changed');
// An old device conversation takes no new turn (MEL-713), with or without a receiver.
let readOnly=false;try{await e.api('/api/send',{to:'bob/desk',body:'request',reply_receiver:{kind:'human'}});}catch(err){readOnly=err.message.includes('read only');}
if(!readOnly||sends!==2)throw Error('old device conversation took a new turn');
console.log('browser receiver refusal ok: 10 refusals before send/files, 2 human paths, old device send refused');
