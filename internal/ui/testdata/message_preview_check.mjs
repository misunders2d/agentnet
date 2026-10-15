import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import {runInNewContext} from 'node:vm';
const source=await readFile(process.argv[2]||new URL('../web/src/features/Message.tsx',import.meta.url),'utf8');
const expression=source.match(/^  const remindable = .*;$/m)?.[0];assert.ok(expression,'message reminder projection exists');
let reads=0;
const project=runInNewContext('(function(m,reminder,remind){'+expression+';return remindable;})',{
 shownText:m=>m.edited?m.text:m.body,
 firstLine:(s,n)=>{reads++;return s.split('\n')[0].slice(0,n);},
});
const original={id:'message',body:'Retained original\nFull body'},edited={...original,edited:true,text:'Edited preview\nFull edited body'};
for(const m of [original,edited]){
 reads=0;const hidden=project(m,undefined,null);assert.equal(hidden.id,m.id);assert.equal(reads,0,'unmounted reminder and absent line do not read a full message preview');
 for(const [reminder,remind] of [[{},null],[undefined,true],[undefined,false],[{},true],[{},false]]){
  reads=0;const shown=project(m,reminder,remind);assert.equal(shown.id,m.id);assert.equal(shown.text,m.edited?'Edited preview':'Retained original');assert.equal(reads,1,'visible line or mounted sheet keeps its exact preview, including closing animation');
 }
}
console.log('PASS hidden reminder previews deferred; existing reminders, newly opened and closing sheets preserve original/edited text');
