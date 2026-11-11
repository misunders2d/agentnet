import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import {stripTypeScriptTypes} from 'node:module';
import {runInNewContext} from 'node:vm';
// Owner, 2026-11-11: bringing an agent into a group must offer every real
// message of the chat as context, including replies of the owner's own
// agents and messages sent from the owner's other devices. On any one
// device those arrive as copies (replica). Only guest excerpts and messages
// of an earlier DM stay out.
const source=await readFile(process.argv[2]||new URL('../web/src/features/RoomPanel.model.ts',import.meta.url),'utf8');
const {shareable}=runInNewContext(stripTypeScriptTypes(source.replace(/^import .*;$/gm,'')).replace(/\bexport /g,'')+'\n({shareable})',{
  firstLine:s=>s?.trim()||'',eventKind:()=>'',agentName:()=>'',agentWhere:()=>'',deviceKind:()=>'',niceDevice:()=>'',personName:()=>'',
});
const msgs=[
  {id:'human',body:'David on his Mac'},
  {id:'mine-here',body:'Me on this laptop',dir:'out'},
  {id:'mine-phone',body:'Me on my phone',dir:'out',replica:true},
  {id:'own-pi',body:'Your Pi answered on Bezos',origin:'agent:pi',dir:'out',replica:true},
  {id:'own-codex',body:'Your Codex answered',origin:'agent:codex',dir:'out',replica:true},
  {id:'other-agent',body:'David’s Codex answered',origin:'agent:codex'},
  {id:'excerpt',body:'An excerpt shared with a guest',excerpt_pid:'p1',replica:true},
  {id:'old-dm',body:'From an earlier DM',other_dm:true},
  {id:'event',body:'joined',event:'joined'},
  {id:'gone',body:'deleted',deleted:true},
];
const ids=shareable({role:'member',messages:msgs}).map(m=>m.id);
assert.deepEqual(ids,['human','mine-here','mine-phone','own-pi','own-codex','other-agent'],'every real message is shareable, own copies and agents included');
console.log('PASS bring-in context offers own agents’ and own other devices’ messages; guest excerpts and earlier DMs stay out');
