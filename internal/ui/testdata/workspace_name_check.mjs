import assert from 'node:assert/strict';
import { WorkspaceShell, BrowserMemberships } from '../static/workspaces.mjs';
const a={id:'default',name:'Old',endpoint:'https://a.example',realm:'a'.repeat(32),handle:'1'.repeat(32),address:'fixture/laptop',state:'enrolled'};
const calls=[];
const shell=new WorkspaceShell({fetch:async(url,options)=>{calls.push(url);return {ok:true,json:async()=>url==='/api/workspaces/rename'?{...a,name:JSON.parse(options.body).name}:{ok:true}};}});
const original=shell.register(a);shell.state().draft={body:'Retain me',reply:'earlier-message'};
const renamed=await shell.rename(a.id,'Product operations');assert.equal(renamed.name,'Product operations');assert.equal(renamed.handle,a.handle);assert.equal(shell.active,a.id);assert.equal(shell.state().draft.body,'Retain me');await original.api('/api/send',{body:'original draft'});assert.equal(calls[1],'/workspaces/default/'+a.handle+'/api/send');
await assert.rejects(shell.rename(a.id,'a\nb'),/readable/);await assert.rejects(shell.rename(a.id,'a\u0085b'),/readable/);await assert.rejects(shell.rename(a.id,'x'.repeat(121)),/readable/);
const cleared=await shell.rename(a.id,'   ');assert.equal(cleared.name,'');
const store=new Map([['agentnet.workspaces.v1',JSON.stringify([a])]]),storage={getItem:key=>store.get(key),setItem:(key,value)=>store.set(key,value)};
const browserShell=new WorkspaceShell();const memberships=new BrowserMemberships({shell:browserShell,storage});const engine={};browserShell.register(a,engine);browserShell.state().draft={body:'Browser draft'};
await browserShell.rename(a.id,'Offline name');assert.equal(JSON.parse(storage.getItem('agentnet.workspaces.v1'))[0].name,'Offline name');assert.equal(browserShell.members.get(a.id).engine,engine);assert.equal(browserShell.state().draft.body,'Browser draft');
const reopened=new BrowserMemberships({shell:new WorkspaceShell(),storage});assert.equal(reopened.records[0].name,'Offline name');assert.equal(reopened.records[0].address,a.address);assert.equal(reopened.records[0].realm,a.realm);
const other={...a,id:'b'.repeat(32),handle:'2'.repeat(32),endpoint:'https://b.example',name:'Offline name'};browserShell.register(other,{});assert.equal(browserShell.list().length,2);assert.notEqual(browserShell.list()[0].id,browserShell.list()[1].id);
// The workspace's own name rides each browser binding (hub_name) from its engine.
engine.workspaceName='Mellanni';assert.equal(browserShell.list().find(b=>b.id===a.id).hub_name,'Mellanni');assert.equal(browserShell.list().find(b=>b.id===other.id).hub_name,undefined);
// adoptDefault's own record has no label: the workspace's name shows.
const fresh=new BrowserMemberships({shell:new WorkspaceShell(),storage:{getItem:()=>null,setItem:()=>{}},newID:()=>'3'.repeat(32),testLoopback:true});const adopted=fresh.adoptDefault({base:'https://relay.example',address:'fixture/phone',fetch:async()=>({ok:true})});assert.equal(adopted.workspace.name,'');
console.log('Workspace alias persistence, clearing, C1 refusal, hub_name, offline browser registry, same transport/drafts and equal-name isolation PASS');
