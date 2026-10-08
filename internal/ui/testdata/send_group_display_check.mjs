import assert from 'node:assert/strict';
import {groupedSends} from '../static/optimistic.mjs';
const a={id:'a',pid:'one',origin:'ui',kind:'question',body:'Same text',send_group:'a'.repeat(32),send_group_author:'exact-key',attachments:[{name:'a.txt',size:3}]};
const b={...a,id:'b',pid:'two'};
const reply={id:'reply',kind:'answer',body:'Done',reply_to:'a'};
const rows=groupedSends([a,reply,b]);
assert.deepEqual(rows.map(r=>[r.m.id,r.compact]),[['a',false],['b',true],['reply',false]]);
assert.equal(rows[1].m,b,'each target retains its original row and actions');
for(const changed of [{send_group:''},{send_group_author:'other-key'},{body:'Different'},{topic:'other'},{kind:'task'},{pid:'one'},{agent_id:'codex'},{origin:'agent:codex'},{deleted:true},{edited:true,text:'Changed'},{excerpt_pid:'context'},{proposal:{}},{attachments:[{name:'different.txt',size:3}]}]){
 const list=groupedSends([a,{...b,...changed}]);
 assert(list.every(r=>!r.compact),JSON.stringify(changed));
}
assert(groupedSends([{...a,send_group:undefined},{...b,send_group:undefined}]).every(r=>!r.compact),'same text never groups legacy sends');
const failed={...b,_local:true,_failed:true,_retry(){},state_text:'Failed'};
assert.equal(groupedSends([a,failed])[1].m._retry,failed._retry,'failed child keeps only its own retry');
console.log('PASS send group display: exact metadata, per-target actions and safe ungrouped fallback');
