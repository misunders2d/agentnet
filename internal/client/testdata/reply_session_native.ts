// Private deterministic native fixture: loaded only by the opt-in isolated test.
import * as fs from 'node:fs';
import { spawnSync } from 'node:child_process';
import { createAssistantMessageEventStream } from '__STREAM__';
const ROOT='__ROOT__', BIN='__BIN__', HOME='__HOME__', PEER='__PEER__';
const log=(x:any)=>fs.appendFileSync(ROOT+'/events.jsonl',JSON.stringify({time:Date.now(),...x})+'\n');
function snapshot(ctx:any){ const file=ctx.sessionManager.getSessionFile();return {session:ctx.sessionManager.getSessionId(),leaf:ctx.sessionManager.getLeafId(),file,idle:ctx.isIdle(),inputs:ctx.sessionManager.getBranch().map((e:any)=>e.message??e).filter((e:any)=>e.customType==='agentnet-receiver').map((e:any)=>e.details)}; }
export default function(pi:any){
 globalThis.fetch=async()=>{log({event:'NETWORK_PROHIBITED'});throw Error('fixture external request prohibited');};
 let calls=0,toolIssued=false,nativeSession="";
 pi.registerProvider('synthetic-live-receiver',{baseUrl:'http://127.0.0.1:1/v1',apiKey:'synthetic-not-a-credential',api:'__API__',models:[{id:'fixture',name:'Synthetic native fixture',reasoning:false,input:['text'],cost:{input:0,output:0,cacheRead:0,cacheWrite:0},contextWindow:100000,maxTokens:100}],streamSimple:(model:any,context:any)=>{
  if(model.provider!=='synthetic-live-receiver'||model.id!=='fixture')throw Error('real provider prohibited');
  const text=JSON.stringify(context.messages),tool=!toolIssued&&text.includes('fixture:busy-start');if(tool)toolIssued=true;
  log({event:'stream',session:nativeSession,call:++calls,tool,remoteData:text.includes('native reply data')});
  const stream=createAssistantMessageEventStream(),message={role:'assistant',api:model.api,provider:model.provider,model:model.id,content:tool?[{type:'toolCall',id:'fixture-gate',name:'fixture_gate',arguments:{}}]:[{type:'text',text:'Synthetic completion only'}],stopReason:tool?'toolUse':'stop',usage:{input:0,output:0,cacheRead:0,cacheWrite:0,totalTokens:0,cost:{input:0,output:0,cacheRead:0,cacheWrite:0,total:0}},timestamp:Date.now()};
  queueMicrotask(()=>{stream.push({type:'start',partial:message});if(tool){stream.push({type:'toolcall_start',contentIndex:0,partial:message});stream.push({type:'toolcall_delta',contentIndex:0,delta:'{}',partial:message});stream.push({type:'toolcall_end',contentIndex:0,toolCall:message.content[0],partial:message});}stream.push({type:'done',reason:message.stopReason,message});stream.end(message);});return stream;
 }});
 pi.on('session_start',(_e:any,ctx:any)=>{nativeSession=ctx.sessionManager.getSessionId();log({event:'start',...snapshot(ctx)});});
 pi.on('message_end',(e:any,ctx:any)=>{if(e.message?.customType==='agentnet-receiver')log({event:'input',...snapshot(ctx),details:e.message.details});});
 pi.on('__END__',(_e:any,ctx:any)=>log({event:'end',...snapshot(ctx)}));
 pi.registerTool({name:'fixture_gate',label:'Synthetic boundary',description:'Hold synthetic native tool boundary',parameters:__PARAMETERS__,async execute(_id:any,_params:any,signal:any,_update:any,ctx:any){
  log({event:'tool-start',...snapshot(ctx)});
  while(!fs.existsSync(ROOT+'/release')&&!signal?.aborted)await new Promise(r=>setTimeout(r,25));
  log({event:'tool-release',...snapshot(ctx)});return {content:[{type:'text',text:'Synthetic boundary complete'}],details:{}};
 }});
 pi.registerCommand('receiver-probe',{description:'Private fixture command',async handler(args:string,ctx:any){
  if(args==='status'){log({event:'status',...snapshot(ctx)});return;}
  if(args==='branch'){
   const entry=ctx.sessionManager.getBranch().find((e:any)=>e.type==='message'&&e.message?.role==='user'&&JSON.stringify(e.message.content).includes('fixture:busy-start'));
   if(!entry)throw Error('missing synthetic branch point');
   if(ctx.branch){await ctx.branch(entry.id);log({event:'branch',...snapshot(ctx)});}
   else await ctx.fork(entry.id,{withSession:(current:any)=>log({event:'branch',...snapshot(current)})});
   return;
  }
  const r=spawnSync(BIN,['--home',HOME,'ask','--wait','0',PEER,'original local request '+args],{encoding:'utf8',timeout:10000});
  log({event:'request',tag:args,success:r.status===0,handle:process.env.AGENTNET_REPLY_SESSION});
 }});
}
