import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';

const sources=[['../web/src/features/Settings.profile.tsx','copyWords'],['../skins/classic/src/entry.mjs','historyLine'],['../skins/zoom/src/entry.mjs','historyLine']];
const base={state:'done',done:0,total:0,delivery_known:true,queued:0,custody:0,delivered:0,blocked:0,deferred:0};
let checks=0;
for(const [path,name] of sources) {
 const source=readFileSync(new URL(path,import.meta.url),'utf8');
 const body=source.match(new RegExp('function '+name+'\\([^\\n]+\\{\\n([\\s\\S]*?)\\n\\}'))?.[1];
 assert(body,'actual source formatter '+name);
 const words=new Function('h',body.replace('const parts: string[]','const parts'));
 for(const [delta,want] of [[{},/No message history waiting/],[{delivery_known:false},/confirmation unavailable/],[{queued:3},/3 waiting to send/],[{custody:4},/4 on the server, waiting for the device/],[{blocked:2},/2 history copies need attention/],[{state:'running',deferred:7},/7 history items waiting for context/],[{state:'running'},/Preparing history/],[{delivered:9},/9 history copies delivered/],[{state:'ended'},/stopped/]]) {
  const value=words({...base,...delta});
  assert.match(value,want,path);assert.doesNotMatch(value,/Getting your chats|History queued|all chats synced/i,path);checks++;
 }
}
console.log('history source/receipt wording checks passed: '+checks);
