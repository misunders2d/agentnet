// Exercise the actual timeline projection without mounting React. Day/run
// boundaries must survive the timestamp-cost fix, including DST and invalid input.
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {stripTypeScriptTypes} from 'node:module';
import {groupedSends} from '../static/optimistic.mjs';

const source=readFileSync(new URL('../web/src/features/Conversation.timeline.tsx',import.meta.url),'utf8');
const body=stripTypeScriptTypes(source.slice(source.indexOf('function build('),source.indexOf('function flash(')));
const NativeDate=Date,zone=process.env.TZ;
let parses=0;
const CountingDate=new Proxy(NativeDate,{construct(target,args){parses++;return Reflect.construct(target,args);}});
const build=Function('groupedSends','ev','whoWrote','isRequest','dayLabel','RUN','Date',body+';return build;')(
 groupedSends,m=>!!m.event,m=>({key:m.from,mine:m.dir==='out'}),m=>['question','task'].includes(m.kind),s=>s,300000,CountingDate);
try{
 for(const tz of ['UTC','Europe/Kyiv','America/New_York','Asia/Kathmandu']){
  process.env.TZ=tz;
  const dates=['2026-03-08T06:59:00Z','2026-03-08T07:01:00Z','2026-03-08T07:06:00Z','2026-03-08T23:59:00Z','2026-03-09T00:01:00Z','invalid'];
  const messages=dates.map((at,i)=>({id:String(i),at,kind:'message',from:'self',dir:'out',body:'Fixture'}));
  parses=0;const result=build(messages,{},'2'),rows=result.filter(r=>r.type==='msg');
  assert.equal(parses,messages.length,'one timestamp conversion per retained message');
  assert.deepEqual(rows.map(r=>r.m.id),messages.map(m=>m.id),'every original remains in order');
  const same=(a,b)=>new NativeDate(a).toDateString()===new NativeDate(b).toDateString();
  assert.equal(rows[0].last,!same(dates[0],dates[1]));
  assert.equal(rows[1].first,!same(dates[0],dates[1]));
  assert.equal(rows[1].last,true,'unread boundary separates authorship runs');
  assert.equal(rows[2].first,true,'five-minute gap starts a new run');
  assert.equal(rows[3].last,!same(dates[3],dates[4]),'local midnight respects device timezone');
  assert.equal(rows[4].first,!same(dates[3],dates[4]));
  assert.equal(rows[5].first,true,'invalid timestamps never merge author runs');
  assert.equal(result.filter(r=>r.type==='new').length,1);
 }
 const messages=Array.from({length:10000},(_,i)=>({id:String(i),at:'2026-10-26T12:00:00Z',from:'self',dir:'out',kind:'message'}));
 parses=0;assert.equal(build(messages,{}).filter(r=>r.type==='msg').length,10000);assert.equal(parses,10000);
 console.log('timeline local-day/DST/unread boundaries and linear timestamp conversion PASS');
}finally{if(zone===undefined)delete process.env.TZ;else process.env.TZ=zone;}
