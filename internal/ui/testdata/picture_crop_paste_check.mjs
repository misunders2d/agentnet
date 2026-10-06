import assert from "node:assert/strict";
import { cleanPicturePNG, pictureCrop, pastePictures, validatePicture } from "../static/pictures.mjs";

const png=Buffer.from("iVBORw0KGgoAAAANSUhEUgAAABAAAAAQCAYAAAAf8/9hAAAAGUlEQVR4nGNwS9nynxLMMGrAqAGjBgwXAwD2dV0fJCKNsQAAAABJRU5ErkJggg==","base64");
function chunk(type,body) {
  const b=Buffer.alloc(body.length+12);b.writeUInt32BE(body.length);b.write(type,4);body.copy(b,8);
  let crc=0xffffffff;
  for(const n of b.subarray(4,-4)){crc^=n;for(let i=0;i<8;i++)crc=(crc>>>1)^((crc&1)?0xedb88320:0);}
  b.writeUInt32BE((crc^0xffffffff)>>>0,b.length-4);return b;
}
const metadata=Buffer.concat([png.subarray(0,33),chunk("iCCP",Buffer.from([99,0,0,120,156,3,0,0,0,0,1])),chunk("tEXt",Buffer.from("author\0Private source")),png.subarray(33)]);
assert.throws(()=>validatePicture(metadata),/metadata/);
const clean=cleanPicturePNG(metadata);assert.deepEqual(Buffer.from(clean),png);validatePicture(clean);
assert.deepEqual(Buffer.from(cleanPicturePNG(png)),png);
for(const bad of [new Uint8Array(),metadata.subarray(0,-1),Buffer.concat([png,Buffer.from([1])]),Buffer.concat([png.subarray(0,33),chunk("FAIL",Buffer.alloc(0)),png.subarray(33)])])assert.throws(()=>cleanPicturePNG(bad));
assert.deepEqual(pictureCrop(800,400),{zoom:1,x:400,y:200,side:400});
assert.deepEqual(pictureCrop(800,400,99,-100,999),{zoom:4,x:50,y:350,side:100});
assert.deepEqual(pictureCrop(400,800,.1,999,-1),{zoom:1,x:200,y:200,side:400});
const event=(files=[],text="")=>({clipboardData:{files,getData:()=>text},prevented:false,preventDefault(){this.prevented=true;}});
const file={name:"image.png",type:"image/png",size:10};let attached=[],inserted=[],errors=[],reads=0;
const add=files=>attached.push(...files),options={insertText:s=>inserted.push(s),error:s=>errors.push(s)};
let e=event([],"ordinary text");await pastePictures(e,add,options);assert.equal(e.prevented,false);
options.clipboardImage=async()=>{reads++;return file;};
e=event([file],"image URL");await pastePictures(e,add,options);assert.equal(e.prevented,true);assert.equal(reads,0);assert.equal(attached.length,1);assert.equal(inserted.length,0);
e=event([],"image URL");const pending=pastePictures(e,add,options);assert.equal(e.prevented,true,"native paste prevented synchronously");await pending;assert.equal(attached.length,2);assert.equal(inserted.length,0);
options.clipboardImage=async()=>null;e=event([],"plain text");await pastePictures(e,add,options);assert.deepEqual(inserted,["plain text"]);
options.clipboardImage=async()=>{throw Error("private clipboard contents must never escape");};e=event();await pastePictures(e,add,options);assert.equal(errors.length,1);assert(!errors[0].includes("private"));
e=event([],"text on native read error");await pastePictures(e,add,options);assert.deepEqual(inserted,["plain text","text on native read error"]);
delete options.clipboardImage;
e=event([],"standalone browser text");await pastePictures(e,add,options);assert.equal(e.prevented,false);
console.log(JSON.stringify({png:Buffer.from(clean).toString("base64"),metadata:metadata.toString("base64"),checks:19}));
