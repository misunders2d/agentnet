import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import {stripTypeScriptTypes} from 'node:module';
import {runInNewContext} from 'node:vm';
// An invited agent's banner says why its device cannot take the invitation
// now: offline, or suspended by the relay until it updates AgentNet.
const source=await readFile(new URL('../web/src/features/RoomPanel.model.ts',import.meta.url),'utf8');
const {hostNote}=runInNewContext(stripTypeScriptTypes(source.replace(/^import .*;$/gm,'')).replace(/\bexport /g,'')+'\n({hostNote})',{
  niceDevice:a=>({'admin/linux-laptop':'Linux laptop','admin/zenbook':'Zenbook'})[a]||a,
});
const o=(members,current=true)=>({me:{address:'admin/zenbook'},directory:{current,members}});
const at=(presence,suspended)=>({address:'admin/linux-laptop',presence,...suspended?{suspended}:{}});
assert.equal(hostNote(o([at('offline')]),'admin/linux-laptop'),'Linux laptop is offline · joins when it’s back');
assert.equal(hostNote(o([at('reconnecting')]),'admin/linux-laptop'),'Linux laptop is offline · joins when it’s back');
assert.equal(hostNote(o([at('connected')]),'admin/linux-laptop'),'');
assert.equal(hostNote(o([at('offline',true)]),'admin/linux-laptop'),'Linux laptop’s AgentNet is behind · update it there');
assert.equal(hostNote(o([at('connected',true)]),'admin/linux-laptop'),'Linux laptop’s AgentNet is behind · update it there');
// Nothing current, an unlisted device or this device: no claim.
assert.equal(hostNote(o([at('offline',true)],false),'admin/linux-laptop'),'');
assert.equal(hostNote(o([]),'admin/linux-laptop'),'');
assert.equal(hostNote(o([at('offline')]),'admin/zenbook'),'');
assert.equal(hostNote(null,'admin/linux-laptop'),'');
console.log('host note: offline, behind, connected and unknown');
