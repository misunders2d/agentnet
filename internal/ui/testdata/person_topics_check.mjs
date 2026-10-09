import assert from 'node:assert/strict';
import {personRoots,personTopicEntries,personMainRoot,mainPreferences} from '../skins/shared/person-topics.mjs';
const peer={person:'person-one',label:'Casey'},roots=[{id:'r1',created:'2026-01-01',peer,count:2,title:'Original',unread:2,guests:1},{id:'r2',created:'2026-01-02',peer,count:1,title:'Message deleted',guests:0},{id:'empty',created:'2026-01-03',peer,count:0,guests:0}];
const excluded=[{id:'guest',peer,role:'human_guest'},{id:'group',peer,kind:'group'},{id:'other',peer:{person:'person-two',label:'Casey'}}];
assert.deepEqual(personRoots({dms:[...roots,...excluded]},{id:'r1',peer}),roots);
const native={id:'native',title:'Native discussion',count:1,state:'archived',unread:1};
const views={r1:{topics:[native],messages:[{id:'main',body:'Original',unread:true},{id:'native-turn',topic:'native',body:'Native'}]},r2:{messages:[{id:'deleted',body:'PRIVATE REMOVED TEXT',deleted:true}]},empty:{messages:[]}};
const flat=personTopicEntries(roots,views);
assert.deepEqual(flat.map(t=>[t.conv,t.topic]),[['r1',''],['r1','native'],['r2',''],['empty','']]);
assert.equal(flat[0].unread,1);assert.equal(flat[1].state,'archived');assert.equal(flat[1].native,native);
assert.equal(flat[2].title,'Message deleted');assert.equal(flat[2].last,'Message deleted');assert.equal(flat[3].title,'Empty topic');
assert(!JSON.stringify(flat.map(({title,last})=>({title,last}))).includes('PRIVATE REMOVED TEXT'));
assert.equal(flat[0].guests,1);assert.equal(flat[2].guests,0);
assert.deepEqual(personTopicEntries([...roots].reverse(),views).map(t=>t.key),flat.map(t=>t.key));
// A chosen existing Main retains its history; other roots stay flat topics.
// Recovering older signed roots cannot replace the persisted UI preference.
const main={id:'chosen-main',created:'2026-02-01',peer,count:0};
assert.deepEqual(personTopicEntries([...roots,main],{...views,'chosen-main':{messages:[]}},main.id).map(t=>t.key),flat.map(t=>t.key));
const recovered={id:'recovered-old',created:'2025-01-01',peer,count:0};
assert.deepEqual(personTopicEntries([main,...roots,recovered],views,main.id).map(t=>t.conv),['recovered-old','r1','r1','r2','empty']);
console.log('flat person topics PASS: exact root/native routes, verified person isolation, guest boundary, archived/empty/deleted entries, current text privacy');

assert.equal(personMainRoot([{id:'empty-new',count:0,created:'2025-01-01'},{id:'older',count:1,created:'2026-01-01',last_at:'2028-01-01'},{id:'newer',count:1,created:'2026-01-02',last_at:'2029-01-01'}]),'older');
assert.equal(personMainRoot([{id:'b',count:1,created:'same'},{id:'a',count:1,created:'same'}]),'a');
console.log('initial Main PASS: immutable creation order, populated preference, deterministic ID tie-break');
const data=new Map(),storage={getItem:k=>data.get(k)||null,setItem:(k,v)=>data.set(k,v)};
let preference=mainPreferences('workspace-one',()=>storage);
assert.equal(preference(roots,peer.person),'r1');
assert.equal(preference([recovered,...roots],peer.person),'r1','late older history cannot change Main');
assert.equal(preference([{...recovered,count:3},...roots],peer.person),'r1','late populated older root cannot change persisted Main');
preference=mainPreferences('workspace-one',()=>storage);
assert.equal(preference([recovered,...roots],peer.person),'r1','reload retains exact Main');
assert.equal(mainPreferences('workspace-two',()=>storage)([recovered,...roots],peer.person),'r1','initial populated preference remains independent of empty recovered root');
assert.equal(preference(roots.filter(r=>r.id!=='r1'),peer.person),'r2','removed Main no longer selected');
assert.equal(preference(excluded,peer.person),'','guest/group roots cannot retain Main');
const mismatched=mainPreferences('workspace-one',()=>storage);
assert.equal(mismatched([{...roots[0],id:'r2',peer:{person:'other'}}],peer.person),'','stored root must belong to verified person');
console.log('Main UI preference PASS: late sync, reload, workspace identity, removed/guest/mismatched-root exclusion');

assert.deepEqual(personTopicEntries([{id:"root",count:1,peer}],{root:{messages:[{topic:"native",body:"Kept"}],topics:[native]}}).map(t=>t.topic),["native"],"erased Main does not leave a ghost beside surviving native topics");
