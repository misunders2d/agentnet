import assert from "node:assert/strict";
import { cleanPicturePNG, pictureCrop, pastePictures, pasteTable, tableMarkdown, validatePicture } from "../static/pictures.mjs";

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
// MEL-714: Ctrl+Shift+V keeps text even when the clipboard also has a picture
// (Excel cells); the browser's own plain-text paste inserts it.
options.clipboardImage=async()=>{reads++;return file;};const before=attached.length,readsBefore=reads;
e=event([file],"ASIN1\tB0TEST\nASIN2\tB0NEXT");await pastePictures(e,add,{...options,plain:true});assert.equal(e.prevented,false);assert.equal(attached.length,before);assert.equal(reads,readsBefore);
e=event([],"cells");await pastePictures(e,add,{...options,plain:true});assert.equal(e.prevented,false);assert.equal(attached.length,before);assert.equal(reads,readsBefore);
e=event([],"");await pastePictures(e,add,{...options,plain:true});assert.equal(attached.length,before+1,"a picture alone still pastes");
delete options.clipboardImage;
e=event([],"standalone browser text");await pastePictures(e,add,options);assert.equal(e.prevented,false);
// MEL-828 report: WPS Office cells in the desktop app (WebKitGTK) reach the
// paste event with no text and no file, and the native picture read finds
// none, so nothing was pasted. The native clipboard text now wins, before any
// picture read; a picture with no text still attaches; browser text keeps
// today's order and never reads native text.
{
  let nativeReads=0,textReads=0;const got=[],att=[];
  const opts={insertText:s=>got.push(s),error:()=>{},clipboardImage:async()=>{nativeReads++;return null;},clipboardText:async()=>{textReads++;return "SKU-1\r\nSKU-2\r\n";}};
  let ev=event([],"");const p=pastePictures(ev,f=>att.push(...f),opts);assert.equal(ev.prevented,true,"prevented synchronously");await p;
  assert.deepEqual(got,["SKU-1\nSKU-2"]);assert.equal(nativeReads,0,"no picture read when the clipboard has text");assert.equal(att.length,0);
  ev=event([],"");await pastePictures(ev,f=>att.push(...f),{...opts,plain:true});assert.deepEqual(got,["SKU-1\nSKU-2","SKU-1\nSKU-2"],"plain paste gets the native text too");
  opts.clipboardText=async()=>{textReads++;return null;};opts.clipboardImage=async()=>{nativeReads++;return file;};
  ev=event([],"");await pastePictures(ev,f=>att.push(...f),opts);assert.equal(att.length,1,"a screenshot still attaches");
  textReads=0;ev=event([],"image URL");await pastePictures(ev,f=>att.push(...f),opts);assert.equal(textReads,0,"browser text present: native text unread");assert.equal(att.length,2,"a browser image copy still attaches");
  const errs=[];opts.error=s=>errs.push(s);
  opts.clipboardText=async()=>{throw Error("This text is too long to paste.");};opts.clipboardImage=async()=>{nativeReads++;return file;};
  const readsNow=nativeReads,attNow=att.length;
  ev=event([],"");await pastePictures(ev,f=>att.push(...f),opts);assert.equal(got.length,2);assert.equal(att.length,attNow,"too-long text never attaches a picture instead");assert.equal(nativeReads,readsNow);assert.equal(errs.length,1);
  // A box that takes no files still recovers the text, and leaves files alone.
  opts.clipboardText=async()=>null;const before=nativeReads;ev=event([],"");await pastePictures(ev,f=>att.push(...f),{...opts,files:false});assert.equal(nativeReads,before,"no picture read where files are refused");assert.equal(att.length,attNow);
  opts.clipboardText=async()=>"cells";ev=event([],"");await pastePictures(ev,f=>att.push(...f),{...opts,files:false});assert.deepEqual(got.at(-1),"cells");
  ev=event([file],"");await pastePictures(ev,f=>att.push(...f),{...opts,files:false});assert.equal(ev.prevented,false);assert.equal(att.length,attNow);
  // A browser (no native reads) still attaches a pasted file.
  ev=event([file],"");await pastePictures(ev,f=>att.push(...f),{insertText:()=>{}});assert.equal(att.length,attNow+1);
  // One trailing line break (a spreadsheet row end) is dropped, others kept.
  opts.clipboardText=async()=>"a\n\n";ev=event([],"");await pastePictures(ev,f=>att.push(...f),opts);assert.equal(got.at(-1),"a\n");
}
// MEL-714: spreadsheet cells (an HTML table on the clipboard) paste as a
// Markdown table, as GitHub's composer does; one column stays plain lines;
// Ctrl+Shift+V keeps the text as it is; a browser image copy is no table.
const typed=(types,files=[])=>({clipboardData:{files,getData:t=>types[t]||""},prevented:false,preventDefault(){this.prevented=true;}});
const excel='<html><body><table><tr><td>ASIN</td><td>Units</td></tr></table></body></html>';
let got=[];e=typed({"text/plain":'ASIN\tUnits\r\nB0TEST\t12\r\n"two\nlines"\t"say ""hi"" | x"\r\n',"text/html":excel},[file]);
assert.equal(pasteTable(e,s=>got.push(s)),true);assert.equal(e.prevented,true);
assert.deepEqual(got,["| ASIN | Units |\n| --- | --- |\n| B0TEST | 12 |\n| two lines | say \"hi\" \\| x |"]);
got=[];e=typed({"text/plain":"B0ONE\r\nB0TWO\r\n","text/html":excel});assert.equal(pasteTable(e,s=>got.push(s)),true);assert.deepEqual(got,["B0ONE\nB0TWO"],"one column stays lines");
got=[];e=typed({"text/plain":"ASIN\tUnits\r\n","text/html":excel});assert.equal(pasteTable(e,s=>got.push(s),true),false);assert.equal(e.prevented,false,"plain-text paste untouched");
e=typed({"text/plain":"https://example.com/cat.png","text/html":'<img src="https://example.com/cat.png">'},[file]);assert.equal(pasteTable(e,s=>got.push(s)),false);assert.equal(e.prevented,false,"image copy is not a table");
assert.equal(tableMarkdown("a\tb\n\tc"),"| a | b |\n| --- | --- |\n| \u00a0 | c |");
console.log(JSON.stringify({png:Buffer.from(clean).toString("base64"),metadata:metadata.toString("base64"),checks:35}));
