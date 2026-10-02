// Receiver codec/age vectors plus production Engine capability publication; no grant or execution.
import * as wire from '../static/wire.mjs';
import {Engine,memoryStore} from '../static/engine.mjs';
import {createInterface} from 'node:readline';
let keys,address;
async function handle(r){
 if(r.op==='setup'){
  keys=await wire.newKeys();address=r.address;
  const engine=new Engine({store:memoryStore()}),records=[];
  engine.keys=keys;engine.address=address;engine.session='1'.repeat(32);
  engine.features=async()=>['caps','signals1'];engine.teamsService=()=>null;
  engine.call=async(method,path,body)=>{if(method!=='PUT'||path!=='/v1/caps')throw Error('unexpected fixture call');records.push(body);};
  for(const method of ['publishPerson','flushReceipts','retryHeld','recoverGroupIntents','flushOutbox','retryApproved','runHistory','runServes','keepFiles','reconcileNotify'])engine[method]=async()=>{};
  await engine.onConnect();if(records.length!==1)throw Error('normal Engine omitted/duplicated capability publication');
  return{public:wire.marshalPublic(await wire.publicEntry(keys,address)),caps_json:records[0]};
 }
 if(r.op==='record'){const route=wire.parseReceiverRoute(r.route),request=await wire.parseReceiverRequest(r.request),choice=wire.parseReceiverChoice(r.choice);return{route:wire.receiverRouteJSON(route),request:wire.receiverRequestJSON(request),choice:wire.receiverChoiceJSON(choice),digest:await wire.receiverDigest(route,request,choice)};}
 if(r.op==='operation'){const o=await wire.parseReceiverOperation(r.body,wire.parseReceiverRoute(r.route),r.replyTo||'',r.attachments||[]);return{json:wire.receiverOperationJSON(o)};}
 if(r.op==='native-vector'){
  for(const raw of [r.original,r.delegate,r.ready])await wire.validateReceiverRoute({sub:'',agent_id:'',status:'',ref:null,session:'',fallback:false,replica:false,...raw});
  const op=await wire.parseReceiverOperation(r.delegate.body,wire.parseReceiverRoute(r.delegate.receiver_route),'',r.delegate.attachments||[]);
  const ready=await wire.parseReceiverOperation(r.ready.body,wire.parseReceiverRoute(r.ready.receiver_route),r.ready.reply_to,[]);
  if(wire.receiverRouteJSON({...r.original.receiver_route,op:'delegate'})!==wire.receiverRouteJSON(r.delegate.receiver_route)||wire.receiverRouteJSON({...r.ready.receiver_route,op:'delegate'})!==wire.receiverRouteJSON(r.delegate.receiver_route))throw Error('native vector routes differ');
  return{delegate_body:wire.receiverOperationJSON(op),ready_body:wire.receiverOperationJSON(ready),digest:await wire.receiverDigest(r.delegate.receiver_route,op.request,op.receiver)};
 }
 if(r.op==='history'){const h=wire.parseHistory(r.json);return{json:wire.historyJSON(h)};}
 if(r.op==='hash')return{hash:await wire.groupHistoryContentHash(r.conv,r.inner)};
 if(r.op==='seal')return{json:await wire.seal({...r.inner,from:address,root:r.inner.root?wire.rootJSON(wire.parseConvRoot(r.inner.root)):''},keys,await wire.parsePublic(JSON.parse(r.to)))};
 if(r.op==='open'){const n=await wire.open(r.json,keys,address,await wire.parsePublic(JSON.parse(r.from)));return{body:n.body,route:n.receiver_route||null};}
 throw Error('unknown receiver fixture operation');
}
for await(const line of createInterface({input:process.stdin})){let out;try{out=await handle(JSON.parse(line));}catch(e){out={error:e.message};}process.stdout.write(JSON.stringify(out)+'\n');}
