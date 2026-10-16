import assert from "node:assert/strict";
import { sniffVideo } from "../static/pictures.mjs";
const enc = s => new TextEncoder().encode(s);
const box = (brand, compatible="") => {
  const b = new Uint8Array(16+compatible.length);
  new DataView(b.buffer).setUint32(0,b.length);
  b.set(enc("ftyp"),4);b.set(enc(brand),8);b.set(enc(compatible),16);return b;
};
for(const brand of ["isom","iso2","mp41","mp42","avc1","M4V "]) assert.equal(sniffVideo(box(brand)),"video/mp4");
assert.equal(sniffVideo(box("xxxx","isom")),"video/mp4");
assert.equal(sniffVideo(box("qt  ")),"video/quicktime");
assert.equal(sniffVideo(Uint8Array.of(0x1a,0x45,0xdf,0xa3)),"video/webm");
assert.equal(sniffVideo(enc("OggS")),"video/ogg");
for(const b of [new Uint8Array(),enc("<html>.mp4</html>"),enc("ftypisom"),box("zzzz"),box("isom").subarray(0,15),Uint8Array.of(0x1a,0x45,0xdf)]) assert.equal(sniffVideo(b),"");
const huge = new Uint8Array(4096);huge.set(box("isom"));new DataView(huge.buffer).setUint32(0,4096);assert.equal(sniffVideo(huge),"");
const offset = new Uint8Array(40);offset.set(box("isom"),7);assert.equal(sniffVideo(offset.subarray(7,23)),"video/mp4");
console.log("video container classifier PASS: known brands, bounded box, truncated/spoof, byte offsets");
