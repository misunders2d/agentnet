import assert from 'node:assert/strict';
import * as wire from '../static/wire.mjs';
import {parseArchiveChunk} from '../static/historyarchive.mjs';
const utf8=new TextEncoder();
const sender=await wire.newKeys(),receiver=await wire.newKeys();
const pub=await wire.publicEntry(sender,'archive/desk'),destination=await wire.publicEntry(receiver,'archive/phone');
const fingerprint=await wire.fingerprint(pub),id=wire.newID();
const body=JSON.stringify({v:1,person:wire.newID(),roster:'a'.repeat(64),recipient:'peer/desk',recipient_key:fingerprint,item:JSON.parse(wire.historyJSON({id,lid:id,from:'archive/desk',from_key:fingerprint,ts:1,at:1000,kind:'message',body:'inert original',attachments:[]}))});
const envelope=JSON.parse(await wire.seal({v:2,id:wire.newID(),from:'archive/desk',to:'archive/phone',ts:1,kind:'message',sub:wire.SubDeviceHistory,replica:true,body},sender,destination));
const chunk={v:1,entries:[{envelope,blobs:[]}]};
const encoded=value=>utf8.encode(JSON.stringify(value));
assert.equal((await parseArchiveChunk(encoded(chunk),1)).entries[0].envelope.id,envelope.id);
let checks=1;
for(const mutation of [
 value=>value.extra=true,
 value=>value.v=2,
 value=>value.entries=[],
 value=>value.entries.push(structuredClone(value.entries[0])),
 value=>value.entries[0].blobs=null,
 value=>delete value.entries[0].blobs,
 value=>value.entries[0].extra=true,
 value=>value.entries[0].envelope.extra=true,
 value=>value.entries[0].envelope.kind='task',
 value=>value.entries[0].envelope.ts=0,
 value=>value.entries[0].envelope.sig=wire.b64(new Uint8Array(63)),
 value=>value.entries[0].envelope.session=wire.newID(),
 value=>value.entries[0].envelope.fallback=true,
 value=>value.entries[0].envelope.attn=true,
 value=>value.entries[0].envelope.chan='invalid',
 value=>value.entries[0].blobs=[{id:wire.newID(),ct:wire.b64(new Uint8Array([1]))}],
]){
 const malformed=structuredClone(chunk);mutation(malformed);await assert.rejects(()=>parseArchiveChunk(encoded(malformed),1));checks++;
}
await assert.rejects(()=>parseArchiveChunk(new Uint8Array([0xff]),1));checks++;
await assert.rejects(()=>parseArchiveChunk(utf8.encode(JSON.stringify(chunk)+'{}'),1));checks++;
await assert.rejects(()=>parseArchiveChunk(new Uint8Array(wire.HistoryArchiveMaxBytes+1),1));checks++;
// Shape and structural hash checks do not substitute for each child signature.
const altered=structuredClone(envelope);altered.to='another/phone';
const parsed=await parseArchiveChunk(encoded({v:1,entries:[{envelope:altered,blobs:[]}]}),1);
await assert.rejects(()=>wire.open(JSON.stringify(parsed.entries[0].envelope),receiver,'another/phone',pub));checks++;
console.log('Archive strict shape, bounds and original signature checks',checks);
