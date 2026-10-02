import {Engine,memoryStore} from '../static/engine.mjs';
const e=new Engine({store:memoryStore(),base:'http://127.0.0.1:1',fetch:()=>{throw Error('unexpected network');}});
let sends=0;for(const name of ['sendDirect','sendDM','askAgent'])e[name]=async()=>{sends++;return{state:'sent'};};
for(const route of ['/api/send','/api/dm/send','/api/dm/agent/ask']) {
 for(const receiver of [{kind:'managed_agent',agent_id:'a'.repeat(32),instructions:'local only',mode:'question'},{kind:'live_session',session_handle:'opaque'},{kind:'human',binding_id:'takeover'},{kind:'human',preset:'injected'},'invalid']) {
  const before=sends;let refused=false;try{await e.api(route,{body:'request',files:[{name:'exact-file',bytes:new Uint8Array([1,2,3])}],reply_receiver:receiver});}catch(err){refused=err.message.includes('reply receiver');}
  if(!refused||sends!==before)throw Error('unsupported reply receiver silently ignored on '+route);
 }
 await e.api(route,{body:'ordinary human path',reply_receiver:{kind:'human'}});
}
if(sends!==3)throw Error('explicit human path changed');
console.log('browser receiver refusal ok: 15 refusals before send/files, 3 human paths');
