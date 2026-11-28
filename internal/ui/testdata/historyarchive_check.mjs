import assert from 'node:assert/strict';
import * as wire from '../static/wire.mjs';
import {parseArchiveChunk,parseArchivePart,compressPart,inflatePart} from '../static/historyarchive.mjs';
const utf8=new TextEncoder();
const sender=await wire.newKeys(),receiver=await wire.newKeys();
const pub=await wire.publicEntry(sender,'archive/desk'),destination=await wire.publicEntry(receiver,'archive/phone');
const fingerprint=await wire.fingerprint(pub),id=wire.newID();
const body=JSON.stringify({v:1,person:wire.newID(),roster:'a'.repeat(64),recipient:'peer/desk',recipient_key:fingerprint,item:JSON.parse(wire.historyJSON({id,lid:id,from:'archive/desk',from_key:fingerprint,ts:1,at:1000,kind:'message',body:'inert original',attachments:[]}))});
const envelope=JSON.parse(await wire.seal({v:2,id:wire.newID(),from:'archive/desk',to:'archive/phone',ts:1,kind:'message',sub:wire.SubDeviceHistory,replica:true,body},sender,destination));
const chunk={v:2,entries:[{envelope,blobs:[]}]};
const encoded=value=>utf8.encode(JSON.stringify(value));
assert.equal((await parseArchiveChunk(encoded(chunk),1)).entries[0].envelope.id,envelope.id);
let checks=1;
for(const mutation of [
 value=>value.extra=true,
 value=>value.v=1,
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
const parsed=await parseArchiveChunk(encoded({v:2,entries:[{envelope:altered,blobs:[]}]}),1);
await assert.rejects(()=>wire.open(JSON.stringify(parsed.entries[0].envelope),receiver,'another/phone',pub));checks++;
// Compressed parts: round trip, and every malformed or oversized part fails closed.
const part=await compressPart(encoded(chunk));
assert.equal((await parseArchivePart(part,1)).entries[0].envelope.id,envelope.id);checks++;
assert.ok(part.length<encoded(chunk).length,'gzip shrinks the base64 child ciphertext and JSON framing');checks++;
const bomb=await compressPart(new Uint8Array(wire.HistoryArchiveMaxBytes+1));
assert.ok(bomb.length<wire.HistoryArchiveMaxPart);
await assert.rejects(()=>inflatePart(bomb),/inflates past its bound/);checks++;
await assert.rejects(()=>parseArchivePart(encoded(chunk),1),/Corrupt/);checks++; // uncompressed JSON is not a part
const corrupt=part.slice();corrupt[corrupt.length-6]^=1;await assert.rejects(()=>parseArchivePart(corrupt,1));checks++;
await assert.rejects(()=>parseArchivePart(new Uint8Array([...part,0]),1));checks++;
await assert.rejects(()=>parseArchivePart(new Uint8Array([...part,...part]),1));checks++;
await assert.rejects(()=>parseArchivePart(part.subarray(0,part.length-4),1));checks++;
await assert.rejects(()=>inflatePart(new Uint8Array(wire.HistoryArchiveMaxPart+1)),/exceeds bound/);checks++;
await assert.rejects(()=>parseArchivePart(part,2));checks++; // signed count must match
const v1=await compressPart(encoded({...chunk,v:1}));await assert.rejects(()=>parseArchivePart(v1,1));checks++;
assert.throws(()=>wire.parseHistoryArchive(JSON.stringify({v:1,person:wire.newID(),roster:'a'.repeat(64),count:1,format:'agentnet-history-v1'})));checks++;
assert.throws(()=>wire.parseHistoryArchive(JSON.stringify({v:1,person:wire.newID(),roster:'a'.repeat(64),count:wire.HistoryArchiveMaxEntries+1,format:wire.HistoryArchiveFormat})));checks++;
wire.parseHistoryArchive(JSON.stringify({v:1,person:wire.newID(),roster:'a'.repeat(64),count:wire.HistoryArchiveMaxEntries,format:wire.HistoryArchiveFormat}));checks++;
console.log('Archive strict shape, bounds, compression and original signature checks',checks);
