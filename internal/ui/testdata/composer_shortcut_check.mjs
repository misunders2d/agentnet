import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import {stripTypeScriptTypes} from 'node:module';
import {runInNewContext} from 'node:vm';
const source=await readFile(process.argv[2]||new URL('../web/src/features/Composer.tsx',import.meta.url),'utf8');
const fn=source.match(/^  function keys\([\s\S]*?^  }/m)?.[0];assert.ok(fn,'actual composer keyboard handler');
let opened=0,sent=0,prevented=0;
const env={dm:{id:'current'},closed:null,sending:false,open:false,spans:[],offered:[],openMentions:()=>opened++,send:()=>sent++};
const keys=runInNewContext('('+stripTypeScriptTypes(fn)+')',env);
const event=patch=>({key:'2',code:'Digit2',ctrlKey:true,metaKey:false,altKey:false,shiftKey:false,nativeEvent:{isComposing:false,keyCode:50},preventDefault:()=>prevented++,...patch});
for(const patch of [{},{key:'ц'},{ctrlKey:false,metaKey:true,key:'"'},{ctrlKey:true,metaKey:true}]){
 opened=sent=prevented=0;keys(event(patch));assert.equal(opened,1,'physical Ctrl/Cmd+Digit2 opens existing picker regardless of layout character');assert.equal(prevented,1);assert.equal(sent,0);
}
for(const patch of [{ctrlKey:false},{altKey:true},{shiftKey:true},{code:'Numpad2'},{code:'Digit3'},{nativeEvent:{isComposing:true,keyCode:50}},{nativeEvent:{isComposing:false,keyCode:229}}]){
 opened=sent=prevented=0;keys(event(patch));assert.equal(opened,0,'ordinary/IME/other shortcut is untouched');assert.equal(prevented,0);assert.equal(sent,0);
}
for(const patch of [{dm:undefined},{closed:{title:'Closed'}},{sending:true}]){
 Object.assign(env,{dm:{id:'current'},closed:null,sending:false},patch);opened=sent=prevented=0;keys(event({}));assert.equal(opened,0,'unavailable picker has no interception or new candidates');assert.equal(prevented,0);assert.equal(sent,0);
}
console.log('PASS actual composer shortcut: physical Ctrl/Cmd+Digit2 only; existing picker; no send; composition/AltGr/Shift/unmodified/unsupported contexts untouched');
