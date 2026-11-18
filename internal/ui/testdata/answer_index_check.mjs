// answerTo through its per-chat index finds exactly what the whole-chat scan it
// replaced found (MEL-748 S5), over random chats with logical-id duplicates,
// device-thread messages, quotations and local rows.
import assert from 'node:assert/strict';
import { registerHooks, stripTypeScriptTypes } from 'node:module';
import { readFileSync } from 'node:fs';
import { pathToFileURL } from 'node:url';
const root=pathToFileURL(process.env.AGENTNET_SOURCE+'/internal/ui/');
registerHooks({load(url,context,next){
 if(url.includes('/web/src/')&&url.endsWith('.ts'))return {format:'module',shortCircuit:true,source:stripTypeScriptTypes(readFileSync(new URL(url),'utf8').replace('constructor(readonly host: Host) {','constructor(host: Host) { this.host = host;'))};
 return next(url,context);
},resolve(specifier,context,next){
 const parent=context.parentURL||'';
 if(parent.includes('/web/src/')){
  if(specifier==='react')return next('data:text/javascript,export const useSyncExternalStore=()=>{};',context);
  if(specifier==='./skin/brand')return next('data:text/javascript,export const skin={storage:"agentnet.messenger"};',context);
  if(specifier==='./optimistic.mjs')return next(new URL('static/optimistic.mjs',root).href,context);
  if(specifier==='./person-topics.mjs')return next(new URL('skins/shared/person-topics.mjs',root).href,context);
  if(specifier==='./linkText')return next(new URL('skins/shared/link-text.mjs',root).href,context);
  if(specifier.startsWith('.')&&!/\.(ts|tsx|mjs|js)$/.test(specifier))return next(new URL(specifier+'.ts',parent).href,context);
 }
 return next(specifier,context);
}});
const {answerTo,isRequest,isReply,isThreadMsg,excerpt}=await import(new URL('web/src/features/Message.model.ts',root));
// The scan answerTo was before its index, verbatim.
function scanned(m,all,ctx){
  if (!isRequest(m) || m._local || excerpt(m)) return undefined;
  return all.find((x) => {
    if (!isReply(x) || x._local || excerpt(x) || !x.reply_to) return false;
    const logical = !isThreadMsg(m) && !!m.lid && x.reply_to === m.lid;
    if (x.reply_to !== m.id && !logical) return false;
    if (logical && all.some(other => other !== m && !isThreadMsg(other) && other.lid === m.lid && other.id !== m.id)) return false;
    if (isThreadMsg(m)) {
      if (!isThreadMsg(x)) return false;
      const host = m.target?.address || m.to;
      const from = x.from || (x.dir === "out" ? ctx?.overview?.me.address : "");
      return !!host && from === host && (m.target?.agent_id || "") === (x.agent_id || "") &&
        (!x.history || !m.target?.fingerprint || x.from_key === m.target.fingerprint);
    }
    return !isThreadMsg(x) && !!m.pid && !!m.target?.address && x.verified_agent && x.pid === m.pid &&
      x.from === m.target.address && (x.agent_id || "") === (m.target.agent_id || "");
  });
}
let seed=7;const rnd=n=>{seed=(seed*1103515245+12345)%2147483648;return (seed>>>16)%n;}; // high bits: the low ones of this generator cycle
const pick=a=>a[rnd(a.length)];
const ids=['a','b','c','d','e','f','g','h'],hosts=['h1','h2'],ctx={overview:{me:{address:'h1'}}};
let checked=0,found=0;
for(let chat=0;chat<3000;chat++){
  const all=[];
  for(let i=0,n=1+rnd(14);i<n;i++){
    const m={id:pick(ids)+i,kind:pick(['question','task','answer','result','message']),dir:pick(['in','out'])};
    if(rnd(2))m.lid=rnd(2)?pick(ids):'L'+i; // shared logical ids, and unique ones
    if(rnd(3))m.reply_to=rnd(2)?pick(all.length?all:[{id:'zz'}]).id:pick(ids);
    if(rnd(3))m.author='x';
    if(rnd(6)===0)m._local=true;
    if(rnd(8)===0)m.excerpt_pid='p';
    m.pid=pick(['p1','p2']);m.from=pick(hosts);m.to=pick(hosts);m.verified_agent=!!rnd(2);
    if(rnd(2))m.agent_id=pick(['','ag']);
    if(rnd(2))m.target={address:pick(hosts),agent_id:pick(['','ag']),fingerprint:rnd(2)?'fp':''};
    if(rnd(4)===0){m.history=true;m.from_key=pick(['fp','other']);}
    all.push(m);
    if(isRequest(m)&&rnd(2)){ // a reply from its executor, by id or logical id, now or later
      m.target??={address:pick(hosts),agent_id:'',fingerprint:''};m._local=false;m.excerpt_pid=undefined;
      const r={id:'r'+i+m.id,kind:pick(['answer','result']),dir:'in',reply_to:m.lid&&rnd(2)?m.lid:m.id,pid:m.pid,verified_agent:true,
        from:m.target?.address||m.to,agent_id:m.target?.agent_id||'',...(m.author?{author:'y'}:{})};
      if(rnd(4)===0)r.from=pick(hosts);
      all.splice(rnd(2)?all.length:rnd(all.length+1),0,r);
    }
  }
  for(const m of all){const a=scanned(m,all,ctx),b=answerTo(m,all,ctx);assert.equal(b,a,JSON.stringify({m,all}));checked++;if(a)found++;}
}
assert(found>20,'the chats found answers: '+found);
console.log(`answerTo through its index equals the whole-chat scan: ${checked} requests, ${found} answered PASS`);
