// Client-owned package files. Native CacheStorage holds immutable snapshots;
// one small catalog response is published LAST, after every file is durable.
// No chat, key, token or remote request is stored here. The digest format and
// limits match static/skins.go. Existing browser APIs own persistence/fetching.
// No package may use a built-in skin's id, or "default" (skin-choice.mjs).
import { RESERVED as reserved, takenName } from './skin-choice.mjs';
const catalogName = 'agentnet-skin-catalog-v1';
const prefix = 'agentnet-skin-package-v1-';
const types = {js:'text/javascript; charset=utf-8',mjs:'text/javascript; charset=utf-8',css:'text/css; charset=utf-8',json:'application/json',png:'image/png',webp:'image/webp',svg:'image/svg+xml',woff2:'font/woff2'};
const enc = new TextEncoder(), decode = new TextDecoder('utf-8',{fatal:true});
const hex = bytes => [...new Uint8Array(bytes)].map(x=>x.toString(16).padStart(2,'0')).join('');
const hash = bytes => crypto.subtle.digest('SHA-256',bytes);
const fail = text => { throw new Error(text); };
const pathOK = s => typeof s==='string' && s.length>0 && !/[\\:%?#\x00-\x1f]/.test(s) && s.split('/').every(x=>x && x!=='.' && x!=='..');
const url = id => new URL('/local-skin-catalog/'+encodeURIComponent(id),location.origin).href;
export const supported = () => !!globalThis.caches && !!navigator.serviceWorker && !!navigator.locks && !!crypto.subtle;
export async function catalog() {
  if (!supported()) return [];
  const cache=await caches.open(catalogName), out=[];
  for(const key of await cache.keys()) {
    const response=await cache.match(key);
    try { const item=await response.json(); if(item.local && /^[0-9a-f]{64}$/.test(item.digest)) out.push(item); } catch (_) { /* incomplete/corrupt metadata is not executable */ }
  }
  return out;
}
export async function prepare(files) {
  files=[...files];
  if(!files.length || files.length>33) fail('Choose skin.json and at most 32 package files.');
  const named=new Map();
  for(const f of files) { const name=f.webkitRelativePath || f.name; if(!pathOK(name)||named.has(name))fail('Package paths must be distinct local relative paths.');named.set(name,f); }
  const manifests=[...named.keys()].filter(x=>x==='skin.json'||x.endsWith('/skin.json'));
  if(manifests.length!==1)fail('Choose exactly one skin.json manifest.');
  const root=manifests[0].slice(0,-9), manifest=named.get(manifests[0]);
  if(manifest.size>16384)fail('The manifest exceeds 16 KiB.');
  const raw=new Uint8Array(await manifest.arrayBuffer());
  if(raw.length>16384)fail('The manifest exceeds 16 KiB.');
  let m;try{m=JSON.parse(decode.decode(raw));}catch(_){fail('The manifest must be UTF-8 JSON.');}
  if(!m || typeof m!=='object' || Array.isArray(m))fail('Invalid AgentNet skin manifest.');
  const css=(name)=>name===undefined||name===''||(typeof name==='string'&&m.files.includes(name)&&name.endsWith('.css'));
  if(m.api!==1 || typeof m.id!=='string' || !/^[a-z][a-z0-9-]{0,47}$/.test(m.id)||reserved.includes(m.id)||typeof m.name!=='string'||!m.name.trim()||enc.encode(m.name).length>80||!Array.isArray(m.files)||!m.files.length||m.files.length>32||new Set(m.files).size!==m.files.length||!m.files.includes(m.entry)||!/^.+\.m?js$/.test(m.entry)||!css(m.style)||!css(m.document))fail('Invalid AgentNet skin manifest.');
  if(takenName(m.name))fail('“'+m.name.trim()+'” is the name of a skin built into AgentNet. A package needs a name of its own.');
  const assets=[], pieces=[raw];let total=raw.length;
  for(const name of m.files){
    if(!pathOK(name)||name==='skin.json'||!Object.hasOwn(types,name.split('.').at(-1)))fail('Unsupported package file: '+name);
    const file=named.get(root+name);if(!file)fail('Missing declared file: '+name);if(file.size>4*1024*1024)fail('A package file exceeds 4 MiB.');
    const bytes=new Uint8Array(await file.arrayBuffer());if(bytes.length>4*1024*1024)fail('A package file exceeds 4 MiB.');
    total+=bytes.length;if(total-raw.length>16*1024*1024)fail('The package exceeds 16 MiB.');
    assets.push({name,bytes,type:types[name.split('.').at(-1)]});pieces.push(enc.encode(name),new Uint8Array([0]),new Uint8Array(await hash(bytes)));
  }
  const joined=new Uint8Array(pieces.reduce((n,x)=>n+x.length,0));let offset=0;for(const part of pieces){joined.set(part,offset);offset+=part.length;}
  const digest=hex(await hash(joined));
  return {item:{api:1,id:'local:'+m.id,package_id:m.id,name:m.name,entry:m.entry,style:m.style||'',document:m.document||'',files:m.files,digest,local:true,size:total},assets};
}
export async function install(files) {
  if(!supported())fail('This browser cannot store skins.');
  const prepared=await prepare(files); // declared local File bytes only
  return navigator.locks.request('agentnet-local-skins',async()=>{
    const {item,assets}=prepared, list=await catalog();
    if(!list.some(x=>x.id===item.id)&&list.length>=32)fail('Remove a skin before importing another (limit 32).');
    const names=(await caches.keys()).filter(x=>x.startsWith(prefix));let total=0;
    for(const name of names){const cache=await caches.open(name);const ready=await cache.match(new URL('/local-skins/ready',location.origin));
      if(!ready){await caches.delete(name);continue;}const meta=await ready.json();total+=Number(meta.size)||0;
    }
    if(!names.includes(prefix+item.digest)&&total+item.size>64*1024*1024)fail('Skins stored in this browser use the 64 MiB limit. Remove one first.');
    const name=prefix+item.digest, cache=await caches.open(name);
    if(!await cache.match(new URL('/local-skins/ready',location.origin))){
      try { for(const f of assets)await cache.put(new URL('/local-skins/'+item.digest+'/'+f.name,location.origin),new Response(f.bytes,{headers:{'Content-Type':f.type,'X-Content-Type-Options':'nosniff','Cache-Control':'no-store'}}));
        await cache.put(new URL('/local-skins/ready',location.origin),Response.json({id:item.id,size:item.size}));
      }catch(e){await caches.delete(name);throw new Error('Skin not stored: browser storage is unavailable or full.');}
    }
    await (await caches.open(catalogName)).put(url(item.id),Response.json(item));
    return item;
  });
}
export async function remove(id) {
  return navigator.locks.request('agentnet-local-skins',async()=>{
    await (await caches.open(catalogName)).delete(url(id));
    // Old immutable snapshots stay during replacement, so an already open
    // interface never imports code from a different digest. Explicit removal
    // deletes all snapshots of this package in this browser only.
    for(const name of await caches.keys())if(name.startsWith(prefix)){const cache=await caches.open(name),r=await cache.match(new URL('/local-skins/ready',location.origin));if(r&&(await r.json()).id===id)await caches.delete(name);}
  });
}
const waitState=(worker,state)=>new Promise((resolve,reject)=>{
  const reached=()=>worker.state===state||(state==='installed'&&['activating','activated'].includes(worker.state));if(reached()){resolve();return;}const timer=setTimeout(()=>{worker.removeEventListener('statechange',change);reject(new Error('The skin worker is not ready. Close other AgentNet tabs and reopen this page.'));},15000);
  function change(){if(reached()||worker.state==='redundant'){clearTimeout(timer);worker.removeEventListener('statechange',change);reached()?resolve():reject(new Error('The skin worker could not be activated.'));}}
  worker.addEventListener('statechange',change);change();
});
export async function activate() {
  if(!supported())fail('This browser cannot load stored skins.');
  const reg=await navigator.serviceWorker.register('/sw.js');
  // Re-registering an existing worker can resolve before its update starts.
  // Wait for the explicit update job before inspecting installing/waiting;
  // otherwise the claim message goes to the previous push-only worker.
  // A previously installed asset worker remains usable while offline.
  await reg.update().catch(e=>{if(!reg.active)throw e;});
  if(reg.installing)await waitState(reg.installing,'installed');
  if(reg.waiting){const worker=reg.waiting;worker.postMessage({type:'agentnet-local-skins-activate'});await waitState(worker,'activated');}
  const worker=reg.active;if(!worker)fail('The skin worker is unavailable.');
  // Called only after explicit interface selection/trust. Claiming never
  // reloads tabs, fetches chats or changes their selected interface.
  await new Promise((resolve,reject)=>{const channel=new MessageChannel(),timer=setTimeout(()=>reject(new Error('Reload AgentNet to use stored skins.')),5000);channel.port1.onmessage=e=>{clearTimeout(timer);channel.port1.close();e.data?.ready?resolve():reject(new Error('The skin worker was refused.'));};worker.postMessage({type:'agentnet-local-skins-claim'},[channel.port2]);});
}
// manager draws the browser-local skin manager into root (host.manageLocalSkins):
// import a package (its files or its folder), the skins stored here and
// Remove. It returns its teardown: root is emptied and nothing it started
// reports into it any more.
export function manager(root,{changed}) {
  let live=true;
  const stop=()=>{live=false;root.replaceChildren();};
  root.replaceChildren();
  const text=(tag,value,cls)=>{const e=document.createElement(tag);e.textContent=value;if(cls)e.className=cls;return e;};
  root.append(text('h3','Skins stored in this browser'),text('p','Import a skin package you got from its author. Nothing is uploaded to your server. When you choose it, you are asked to trust it first: it can read your chats and act as you.'));
  if(!supported()){root.append(text('p','This browser cannot store skins.'));return stop;}
  const status=text('p',''),list=text('div','','items'),pickers=text('div','','pickers');status.setAttribute('role','status');
  const say=(s)=>{if(live)status.textContent=s;};
  const refresh=async()=>{const items=await catalog();if(!live)return;list.replaceChildren();for(const item of items){const row=text('p','','item');row.append(text('strong',item.name),text('span','Fingerprint '+item.digest.slice(0,12)));const del=text('button','Remove');del.type='button';del.setAttribute('aria-label','Remove '+item.name);del.onclick=async()=>{if(!confirm('Remove '+item.name+' from this browser? A tab using it opens Comic the next time it loads.'))return;try{await remove(item.id);await changed();await refresh();say('Removed from this browser.');}catch(_){say('Could not remove this skin.');}};row.append(del);list.append(row);}};
  const picker=(folder)=>{const label=text('label',folder?'Import a skin folder':'Import skin files'),input=document.createElement('input');input.type='file';input.multiple=true;if(folder)input.webkitdirectory=true;input.setAttribute('aria-label',label.textContent);label.append(input);input.onchange=async()=>{say('Checking the package…');try{const item=await install(input.files);await changed();await refresh();say('Stored '+item.name+'. Choose it under Skin: you review trust before it runs.');}catch(e){say(e.message);}finally{input.value='';}};return label;};
  pickers.append(picker(false));if('webkitdirectory' in document.createElement('input'))pickers.append(picker(true));
  root.append(pickers,status,list);refresh().catch(()=>say('Could not read the skins stored here.'));
  return stop;
}
