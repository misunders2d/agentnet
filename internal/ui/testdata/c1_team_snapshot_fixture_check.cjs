// Fixture transport contract only; no rendered control or latency acceptance.
const assert=require('node:assert/strict');
const {teamSnapshotFixture,snapshotConfig}=require('./c1_team_snapshot_fixture.cjs');
const {checkAppWitness}=require('./c1_team_snapshot_cases.cjs');
async function main(){
 let checks=0;async function check(name,fn){await fn();checks++;console.log('PASS '+name);}
 await check('metadata consumed and detached from caller mutations',async()=>{
  const config=snapshotConfig(),f=teamSnapshotFixture(config);config.teams[0].name='Wrong';config.snapshot.persons=[];
  const view=await f.teams();assert.equal(view.teams[0].name,'List A');view.teams[0].name='Wrong again';
  assert.equal((await f.teams()).teams[0].name,'List A');
  const work=f.snapshot(['list-a']);assert.equal(f.witness().pending,1);f.settle(1,'resolve');
  assert.deepEqual((await work).persons.map(p=>p.id),['person-1','person-2','self']);
 });
 await check('held Promise remains pending until explicit settlement',async()=>{
  const f=teamSnapshotFixture(snapshotConfig());let done=false;const work=f.snapshot(['list-a']).then(v=>{done=true;return v;});
  await Promise.resolve();await Promise.resolve();assert.equal(done,false);assert.equal(f.witness().pending,1);
  f.settle(1,'resolve');await work;assert.equal(done,true);assert.equal(f.witness().resolved,1);
 });
 await check('failing backend actually rejects',async()=>{
  const f=teamSnapshotFixture(snapshotConfig('failing'));await assert.rejects(f.snapshot(['list-a']),/Synthetic snapshot refusal/);
  assert.equal(f.witness().failed,1);assert.equal(f.witness().pending,0);
 });
 await check('normal backend resolves consumed snapshot',async()=>{
  const f=teamSnapshotFixture(snapshotConfig('normal'));const result=await f.snapshot(['list-a']);
  assert.equal(result.at,1700000000);assert.equal(f.witness().resolved,1);
 });
 await check('old request can complete after newer request',async()=>{
  const f=teamSnapshotFixture(snapshotConfig()),first=f.snapshot(['list-a']),second=f.snapshot(['list-b']);
  f.settle(2,'resolve',{persons:[{id:'newer'}],at:2});assert.equal((await second).persons[0].id,'newer');
  assert.equal(f.witness().pending,1);f.settle(1,'resolve',{persons:[{id:'older'}],at:1});
  assert.equal((await first).persons[0].id,'older');
 });
 await check('retry uses a separate exact request',async()=>{
  const f=teamSnapshotFixture(snapshotConfig()),first=f.snapshot(['list-a']);const rejected=assert.rejects(first,/refused/);
  f.settle(1,'reject','refused');await rejected;const retry=f.snapshot(['list-a']);f.settle(2,'resolve');await retry;
  assert.deepEqual(f.witness().requests.map(r=>r.teams),[['list-a'],['list-a']]);
 });
 await check('malformed fixture and unknown requests fail closed',async()=>{
  assert.throws(()=>teamSnapshotFixture({}),/explicit backend mode/);
  assert.throws(()=>teamSnapshotFixture({...snapshotConfig(),teams:[]}),/teams metadata/);
  assert.throws(()=>teamSnapshotFixture({...snapshotConfig(),snapshot:{persons:[],at:0}}),/timestamp/);
  const f=teamSnapshotFixture(snapshotConfig());assert.throws(()=>f.snapshot(['unknown']),/exact known list/);
  assert.throws(()=>f.settle(1,'resolve'),/unknown or completed/);
 });
 await check('app side-effect witness requires explicit zero outcomes',async()=>{
  const valid={group_creates:0,group_invites:0,unexpected_mutations:[],navigation:[],submit_events:[],parent_submit_handlers:[],state:{group_creation:false,has_open:false,message_landing:false},dialog:'group',group_name:'Synthetic'};
  checkAppWitness(valid);assert.throws(()=>checkAppWitness({}));
  for(const field of ['group_creates','group_invites'])assert.throws(()=>checkAppWitness({...valid,[field]:1}));
  for(const field of ['unexpected_mutations','navigation','submit_events','parent_submit_handlers'])assert.throws(()=>checkAppWitness({...valid,[field]:[{}]}));
  assert.throws(()=>checkAppWitness({...valid,dialog:null}));
 });
 console.log(JSON.stringify({checks_passed:checks,failed:0,skipped:0,scope:'synthetic transport contract only',rendered_cases:0,release_qualified:false}));
}
main().catch(e=>{console.error(e.stack);process.exitCode=1;});
