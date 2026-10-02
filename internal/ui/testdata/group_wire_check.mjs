// Pure Go/JS group protocol vectors; no Engine, transport, membership or worker.
import * as wire from '../static/wire.mjs';
import { createInterface } from 'node:readline';
let keys, address;
const decode = new TextDecoder();
const parse = {root:wire.parseConvRoot, admission:wire.parseGroupAdmission, state:wire.parseGroupState, withdrawal:wire.parseGroupWithdrawal, commit:wire.parseGroupCommit, context:wire.parseGroupContext, journal:wire.parseGroupJournal, carrier:wire.parseGroupCarrier};
const json = {root:wire.rootJSON, admission:wire.groupAdmissionJSON, state:wire.groupStateJSON, withdrawal:wire.groupWithdrawalJSON, commit:wire.groupCommitJSON, context:wire.groupContextJSON, journal:wire.groupJournalJSON, carrier:wire.groupCarrierJSON};
const canonical = {root:wire.rootCanonical, admission:wire.groupAdmissionCanonical, state:wire.groupStateCanonical, withdrawal:wire.groupWithdrawalCanonical, commit:wire.groupCommitCanonical};
const sign = {root:wire.signGroupRoot, admission:wire.signGroupAdmission, state:wire.signGroupState, withdrawal:wire.signGroupWithdrawal, commit:wire.signGroupCommit};
async function resolver(raw) { const rosters = await Promise.all((raw || []).map(wire.parseRoster)); for (const r of rosters) await wire.verifyFirst(r); const hashes = await Promise.all(rosters.map(wire.rosterHash)); return (person, hash) => rosters.find((r,i) => r.person === person && hashes[i] === hash); }
async function handle(r) {
 if(r.op==='setup'){address=r.address;keys=await wire.newKeys();const roster=await wire.newRoster(keys,address,'Browser <&> Ю');return{public:wire.marshalPublic(await wire.publicEntry(keys,address)),roster:wire.rosterJSON(roster)};}
 if(r.op==='record'){const value=parse[r.type](r.json);return{json:json[r.type](value),canonical:canonical[r.type]?decode.decode(canonical[r.type](value)):null,hash:r.type==='root'?await wire.rootID(value):r.type==='state'?await wire.groupStateHash(value):r.type==='admission'?await wire.groupAdmissionHash(value):null};}
 if(r.op==='sign'){const value=parse[r.type](r.json),signed=await sign[r.type](keys,value);return{json:json[r.type](signed),canonical:decode.decode(canonical[r.type](signed))};}
 if(r.op==='verify'){
  const resolve=await resolver(r.rosters),root=wire.parseGroupRoot(r.root),state=r.state?wire.parseGroupState(r.state):null,previous=r.previous?wire.parseGroupState(r.previous):null,withdrawals=(r.withdrawals||[]).map(wire.parseGroupWithdrawal);
  if(r.type==='context')await wire.verifyGroupContext(wire.parseGroupContext(r.json),resolve);
  if(r.type==='admission')await wire.verifyGroupAdmission(wire.parseGroupAdmission(r.json),resolve);
  if(r.type==='withdrawal')await wire.verifyGroupWithdrawal(wire.parseGroupWithdrawal(r.json),state,resolve);
  if(r.type==='state')await wire.verifyGroupState(state,root,previous,resolve,withdrawals);
  if(r.type==='current'){const records=(r.records||[]).map(wire.parseGroupCommit);await wire.verifyGroupCurrent(state,root,wire.parseGroupCommit(r.authority),resolve,seq=>records.find(c=>c.seq===seq),withdrawals);}
  if(r.type==='proof'){const old=(r.records||[]).map(wire.parseGroupCommit),head=await wire.verifyGroupProofPage(root,wire.parseGroupJournal(r.json),resolve,r.realm,{previous:r.previousCommit?wire.parseGroupCommit(r.previousCommit):null,known:seq=>old.find(c=>c.seq===seq)});return{seq:head?.seq,hash:head?.hash};}
  return{effective:state?(await wire.effectiveGroupMembers(state,withdrawals)).map(m=>m.person):null};
 }
 if(r.op==='caps'){const c=await wire.newCaps(keys,address,'1'.repeat(32));return{caps:c.caps};}
 if(r.op==='legacy-root'){wire.parseRoot(r.json);return{};}
 if(r.op==='history'){const a=wire.parseGroupAdmission(r.json);return{allowed:wire.groupAllowsHistory(a,r.ref)};}
 if(r.op==='seal')return{json:await wire.seal({...r.inner,from:address,root:r.inner.root?JSON.stringify(r.inner.root):''},keys,await wire.parsePublic(JSON.parse(r.to)))};
 if(r.op==='open'){const n=await wire.open(r.json,keys,address,await wire.parsePublic(JSON.parse(r.from)));return{body:n.body,root:n.root,sub:n.sub,conv:n.conv};}
 throw Error('unknown group wire operation');
}
for await(const line of createInterface({input:process.stdin})){let out;try{out=await handle(JSON.parse(line));}catch(e){out={error:e.message};}process.stdout.write(JSON.stringify(out)+'\n');}
