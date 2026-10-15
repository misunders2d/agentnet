import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import {stripTypeScriptTypes} from 'node:module';
import {runInNewContext} from 'node:vm';
const source=await readFile(process.argv[2]||new URL('../web/src/features/RoomPanel.model.ts',import.meta.url),'utf8');
let previews=0;
const {exposure}=runInNewContext(stripTypeScriptTypes(source.replace(/^import .*;$/gm,'')).replace(/\bexport /g,'')+'\n({exposure})',{
  firstLine:s=>{previews++;return s?.trim()||'';},
});
const result=v=>JSON.parse(JSON.stringify(v));
const clock=s=>'T:'+s;
const empty={shared:[],missing:0};
// Empty invitation history cannot need any timeline/body inspection. In a
// long conversation each visible helper card used to format every body.
const blocked={role:'member',get messages(){throw Error('empty sharing read the timeline');}};
for(let i=0;i<4;i++)assert.deepEqual(result(exposure(blocked,empty,clock)),{cells:[],label:'Saw nothing from before',range:''});
assert.equal(previews,0,'empty helper cards do not parse any body previews');
assert.deepEqual(result(exposure(blocked,{...empty,missing:1},clock)),{cells:[],label:'Saw 1 message not on this computer',range:''});
assert.deepEqual(result(exposure(blocked,{...empty,missing:3},clock)),{cells:[],label:'Saw 3 messages not on this computer',range:''});
assert.deepEqual(result(exposure({role:'visitor',get messages(){throw Error('visitor empty sharing read the timeline');}},empty,clock)),{cells:[],label:'Saw nothing from before',range:''});
const visitor={role:'visitor',get messages(){throw Error('visitor shared history read the timeline');}};
assert.deepEqual(result(exposure(visitor,{shared:[],missing:2},clock)),{cells:[],label:'Saw 2 messages',range:''},'foreign-role wording is preserved');
assert.deepEqual(result(exposure(visitor,{shared:[{id:'b',at:'2'}],missing:0},clock)),{cells:[],label:'Saw 1 message',range:'T:2'});
const a={id:'a',at:'1',body:'First'},b={id:'b',at:'2',body:'Second'},c={id:'c',at:'3',body:'Third'};
const thread={role:'member',messages:[a,{id:'event',at:'1',event:'joined',body:'Participation'},b,{id:'deleted',at:'2',deleted:true,body:'Removed'},c,{id:'blank',at:'4',body:''}]};
assert.deepEqual(result(exposure(thread,{shared:[b,c],missing:0},clock)),{cells:[false,true,true],label:'Saw the last 2 messages',range:'T:2–T:3'},'nonempty exposure keeps timeline eligibility, order, cells and range');
assert.deepEqual(result(exposure(thread,{shared:[b],missing:1},clock)),{cells:[false,true],label:'Saw 2 messages',range:'T:2'},'partly missing shared history is preserved');
assert.deepEqual(result(exposure(thread,{shared:[{id:'absent'}],missing:2},clock)),{cells:[],label:'Saw 2 messages not on this computer',range:''},'nonempty unmatched history remains unchanged');
const many=Array.from({length:30},(_,i)=>({id:String(i),at:String(i),body:'Line '+i}));
const cells=exposure({messages:many},{shared:many,missing:0},clock).cells;
assert.equal(cells.length,14);assert.ok(cells.every(Boolean),'nonempty shared strip stays limited to its original14cells');
console.log('PASS empty helper cards skip all timeline/body previews; exact missing/foreign labels and nonempty exposure remain');
