// Source-built actual Conversation/Store. This module starts no browser/server.
const {topicRenameFixture}=require('./c1_topic_rename_fixture.cjs');
function entry(config){
 // Validate every configuration field before generating a bundle.
 topicRenameFixture(config);
 return `
import React,{useEffect} from 'react';import {createRoot} from 'react-dom/client';
import {Store,useStore} from './src/store';import {StoreContext} from './src/context';import {OwnedContext} from './src/owned';
import {Conversation} from './src/features/Conversation';
const backend=(${topicRenameFixture.toString()})(${JSON.stringify(config)});
const fixture=globalThis.fixture={backend,requests:()=>backend.witness()};
globalThis.__c1Fired=[];
globalThis.__anTrack=(id,fn)=>typeof fn!=='function'?fn:function(...args){
 globalThis.__c1Fired.push({id,at_ms:performance.now()});
 if(fixture.noop===id){args[0]?.preventDefault?.();return;}
 return fn.apply(this,args);
};
const host={platform:'daemon',workspace:{id:'c1-rename-synthetic',state:'enrolled'},listen(){return()=>{}},api:backend.api};
const root=document.getElementById('root'),app=document.getElementById('app'),portals=document.getElementById('portals');
const store=new Store(host);await store.start();
// Fixture setup only. Observed Rename/Reset/Save/typing must use trusted UI input.
await store.open({kind:'dm',id:backend.conv});store.setDraft(backend.conv,{...store.draft(backend.conv),topic:backend.topic});
fixture.state=()=>{const s=store.get(),t=s.views[backend.conv]?.topics?.find(t=>t.id===backend.topic);return{title:t?.title,busy:Object.values(s.topicBusy),pending:backend.witness().pending,open:s.open,tab:s.tab}};
function Scene(){const open=useStore(store,s=>s.open);useEffect(()=>{globalThis.__c1RenameMounted=true;},[]);return open?<Conversation o={open}/>:null;}
createRoot(app).render(<OwnedContext.Provider value={{root,app,portals}}><StoreContext.Provider value={store}><Scene/></StoreContext.Provider></OwnedContext.Provider>);
`;
}
// Read presented source-owned elements. Store/backend state is a separate
// witness; a value in either object never proves a painted intended result.
function snapshot(){
 if(globalThis.__c1RenameSnapshotFault)throw Error('Injected rename observer fault');
 const visible=el=>{
  if(!el||!el.getClientRects().length)return false;
  const s=getComputedStyle(el),r=el.getBoundingClientRect();
  if(s.display==='none'||s.visibility!=='visible'||Number(s.opacity)===0)return false;
  let top=Math.max(0,r.top),bottom=Math.min(innerHeight,r.bottom),left=Math.max(0,r.left),right=Math.min(innerWidth,r.right);
  for(let p=el.parentElement;p;p=p.parentElement){const style=getComputedStyle(p);if(style.display==='none'||style.visibility!=='visible'||Number(style.opacity)===0)return false;if(/auto|scroll|hidden|clip/.test(style.overflowY)){const box=p.getBoundingClientRect();top=Math.max(top,box.top);bottom=Math.min(bottom,box.bottom);}if(/auto|scroll|hidden|clip/.test(style.overflowX)){const box=p.getBoundingClientRect();left=Math.max(left,box.left);right=Math.min(right,box.right);}}
  return right-left>=24&&bottom-top>=16;
 };
 const menus=[...document.querySelectorAll('nav button')].filter(b=>(b.getAttribute('aria-label')||'').endsWith('open topic, menu'));
 if(menus.length!==1)throw Error('Expected exactly one current topic menu');
 const menu=menus[0],dialogs=[...document.querySelectorAll('[role="dialog"]')].filter(visible);
 const field=document.querySelector('form[aria-label="Write a message"] textarea');
 const mirror=document.querySelector('form[aria-label="Write a message"] [data-composer-measure]');
 const name=document.querySelector('#rename-topic-name');
 return{topic_label:menu.getAttribute('aria-label'),topic_text:menu.textContent.trim(),topic_visible:visible(menu),rename_open:dialogs.some(d=>d.querySelector('#rename-topic-name')),
  name:name?.value??null,name_visible:visible(name),composer:field?.value??null,composer_mirror:mirror?.textContent??null,composer_visible:visible(mirror),
  backend:fixture.backend.witness()};
}
const bindings={
 reset:'agentnet:internal/ui/web/src/features/Conversation.topics.tsx#save/Button/onClick#1',
 save:'agentnet:internal/ui/web/src/features/Conversation.topics.tsx#save/Button/#1',
 name:'agentnet:internal/ui/web/src/features/Conversation.topics.tsx#save/input/onChange#1',
 submit:'agentnet:internal/ui/web/src/features/Conversation.topics.tsx#save/form/onSubmit#1',
 composer:'agentnet:internal/ui/web/src/features/Composer.tsx#candidate/Field/onChange,onSelect,onBlur,onScroll,onKeyDown,onPaste#1',
 composer_native_alias:'agentnet:internal/ui/web/src/features/Composer.parts.tsx#Field/textarea/#1',
};
module.exports={entry,snapshot,bindings};
