import assert from 'node:assert/strict';
import { chatList } from '../web/src/model.ts';

const person={person:'verified-person',label:'Vitalii',address:'vitalii/desk',state:'pinned'};
const conversation=(id,patch={})=>({id,kind:'dm',role:'member',peer:person,created:'2026-10-06T12:00:00Z',last_at:'2026-10-06T12:00:00Z',count:1,title:id,last:id,unread:1,held:0,...patch});
const old=conversation('laptop-original',{unread:2});
const recent=conversation('phone-original',{last_at:'2026-10-06T12:30:00Z',unread:3});
const empty=conversation('zenbook-empty',{last_at:'2026-10-06T13:00:00Z',count:0,title:'',last:'',unread:0});
const other=conversation('look-alike',{peer:{...person,person:'different-person',address:'other/desk'}});
const guest=conversation('guest-room',{role:'human_guest'});
const group=conversation('group-room',{kind:'group',title:'Team'});
const overview={dms:[empty,old,other,guest,recent,group],threads:[],needs_you:[{conv:old.id},{conv:recent.id},{conv:recent.id,decide_on:'another device'}]};
const rows=chatList(overview,{}),chat=rows.find(r=>r.key==='person:verified-person');
assert.equal(rows.length,4,'one human chat, separate look-alike identity, guest audience and group');
assert.equal(chat.open.id,recent.id,'latest populated conversation opens, not empty duplicate');
assert.equal(chat.unread,5,'unread aggregates every conversation once');
assert.equal(chat.needsYou,2,'local decisions aggregate, remote-owned decisions excluded');
assert.deepEqual(chat.conversations.map(c=>c.id),[empty.id,recent.id,old.id],'all distinct conversation IDs remain reachable');
assert.equal(overview.dms[0],empty,'projection does not reorder or merge source data');
assert.equal(rows.filter(r=>r.title==='Vitalii').length,3,'same label never establishes identity or guest-room membership');
console.log('One person chat: separate conversations, combined unread, exact identity and audience boundaries PASS');

// Preview focus uses the same displayed row, with exact IDs preferred over
// logical aliases. A malicious same-LID author cannot redirect navigation.
const { messageTarget } = await import('../web/src/model.ts');
const { previewMessageID } = await import('../static/engine.mjs');
const latest={id:'actual-copy',lid:'stable-original'};
assert.equal(previewMessageID([latest],latest),'stable-original');
assert.equal(messageTarget([latest],'stable-original'),latest);
const ambiguous={id:'other-copy',lid:'stable-original'};
assert.equal(previewMessageID([ambiguous,latest],latest),'actual-copy');
assert.equal(messageTarget([ambiguous,latest],'stable-original'),undefined);
const exact={id:'stable-original',lid:'elsewhere'};
assert.equal(previewMessageID([exact,latest],latest),'actual-copy');
assert.equal(messageTarget([latest,exact],'stable-original'),exact);
assert.equal(previewMessageID([{id:'legacy'}],{id:'legacy'}),'legacy');
const target=chatList({...overview,dms:[{...recent,last_id:'stable-original'}]},{}).find(r=>r.key==='person:verified-person');
assert.equal(target.open.focus,'stable-original');
assert.equal(target.open.id,recent.id);
console.log('Preview references PASS: exact populated root, stable alias, legacy fallback, duplicate-author LID refusal, physical-ID priority');
