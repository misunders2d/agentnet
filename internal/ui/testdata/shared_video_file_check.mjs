import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {createHash} from 'node:crypto';
import {mountVideoFile,videoFileName,INLINE_VIDEO_LIMIT} from '../skins/shared/video-file.mjs';
import {sniffVideo} from '../static/pictures.mjs';
// Reuse the complete existing container vectors, not a second classifier.
await import('./video_container_check.mjs');

// Same owner-document event harness convention as shared_visible_rows_check.
class Events {
 listeners=new Map();
 addEventListener(k,f){const set=this.listeners.get(k)||new Set();set.add(f);this.listeners.set(k,set);}
 removeEventListener(k,f){this.listeners.get(k)?.delete(f);}
 fire(k){for(const f of [...(this.listeners.get(k)||[])])f({type:k,target:this,currentTarget:this});}
}
class Element extends Events {
 constructor(tag,document){super();this.tagName=tag.toUpperCase();this.ownerDocument=document;this.children=[];this.style={};this.attributes=new Map();this.textContent='';this.hidden=false;this.disabled=false;this.mediaOps=[];}
 get isConnected(){return this.connected??this.parentNode?.isConnected??false;}
 append(...nodes){for(const node of nodes){node.remove();node.parentNode=this;this.children.push(node);}}
 remove(){if(this.parentNode){this.parentNode.children=this.parentNode.children.filter(n=>n!==this);this.parentNode=null;}}
 contains(node){return this===node||this.children.some(n=>n.contains(node));}
 setAttribute(k,v){this.attributes.set(k,String(v));}
 getAttribute(k){return this.attributes.get(k)??null;}
 removeAttribute(k){this.attributes.delete(k);if(k==='src')this.mediaOps.push({op:'remove-src',connected:this.isConnected});}
 set src(v){this.setAttribute('src',v);}get src(){return this.getAttribute('src')||'';}
 click(){if(!this.disabled)return this.onclick?.({currentTarget:this});}
 play(){this.mediaOps.push({op:'play',connected:this.isConnected});this.paused=false;return this.ownerDocument.play(this);}
 pause(){this.mediaOps.push({op:'pause',connected:this.isConnected,src:this.src});this.paused=true;}
 load(){this.mediaOps.push({op:'load',connected:this.isConnected,src:this.src});}
}
const all=n=>[n,...n.children.flatMap(all)],text=n=>[n.textContent,...n.children.map(text)].join(' ');
const tick=async()=>{for(let i=0;i<5;i++)await new Promise(r=>setImmediate(r));};
const deferred=()=>{let resolve,reject;const promise=new Promise((a,b)=>{resolve=a;reject=b;});return{promise,resolve,reject};};
const bytes=Uint8Array.of(0x1a,0x45,0xdf,0xa3),encode=s=>new TextEncoder().encode(s);
let checks=0;
function fixture(options={}) {
 const document=new Events(),window=new Events(),created=[],revoked=[],reads=[],downloads=[];let visible=true,allowed=true,native,off=0;
 document.visibilityState='visible';document.hidden=false;document.defaultView=window;document.createElement=tag=>new Element(tag,document);document.play=()=>Promise.resolve();window.Blob=Blob;
 window.URL={createObjectURL(blob){assert(blob instanceof Blob);const url='blob:owned-'+created.length;created.push({blob,url});return url;},revokeObjectURL(url){revoked.push(url);}};
 const root=document.createElement('div');root.connected=true;
 const file={name:'received.webm',size:bytes.length,openable:true,...options.file};
 const load=()=>{reads.push({id:'exact-message',index:8,dir:'in'});return options.load?options.load():Promise.resolve({bytes});};
 const controller=mountVideoFile({root,file,load,sniffVideo:options.sniffVideo||sniffVideo,download:()=>{downloads.push('existing-download');return options.download?.();},allowed:()=>allowed&&visible,onVisibility:fn=>{native=fn;return()=>{off++;native=null;};}});
 const button=name=>{const node=all(root).find(n=>n.tagName==='BUTTON'&&(n.getAttribute('aria-label')===name||n.textContent===name));assert(node,'missing button '+name);return node;};
 const video=()=>all(root).find(n=>n.tagName==='VIDEO');
 const stop=()=>{controller.destroy();assert.equal(off,1);assert.equal(root.children.length,0);assert.equal([...document.listeners.values(),...window.listeners.values()].reduce((n,s)=>n+s.size,0),0);controller.destroy();assert.equal(off,1);};
 return {root,file,document,window,controller,created,revoked,reads,downloads,button,video,stop,get off(){return off;},setAllowed(v){allowed=v;},native(v){visible=v;native?.(v);},hidden(v){document.hidden=v;document.visibilityState=v?'hidden':'visible';document.fire('visibilitychange');}};
}
const ambient=['window','document','fetch','IntersectionObserver','MutationObserver'];const saved=ambient.map(k=>[k,Object.getOwnPropertyDescriptor(globalThis,k)]);
for(const key of ambient)Object.defineProperty(globalThis,key,{configurable:true,get(){throw Error('ambient '+key+' used instead of supplied root');}});
try {
 assert.equal(INLINE_VIDEO_LIMIT,16*1024*1024);for(const name of ['a.mp4','b.M4V','c.mov','d.webm','e.ogv','f.ogg'])assert(videoFileName(name));for(const name of ['video.mp4.html','safe.svg','video',''])assert.equal(videoFileName(name),false);checks++;
 {
  const f=fixture();assert.equal(f.reads.length,0);assert.equal(f.created.length,0);assert(!f.video());assert(text(f.root).includes('received.webm'));assert(text(f.root).includes('received.webm · 4 bytes'));
  assert.equal(f.root.children[0].className,'an-video-file');assert(all(f.root).some(n=>n.className==='an-video-actions'));
  for(const b of all(f.root).filter(n=>n.tagName==='BUTTON')){assert.equal(b.type,'button');assert.deepEqual(b.className.split(/\s+/),['btn','an-hit']);}
  f.button('Download received.webm').click();await tick();assert.equal(f.downloads.length,1);assert.equal(f.reads.length,0);
  const play=f.button('Play video received.webm');play.click();play.click();await tick();assert.deepEqual(f.reads,[{id:'exact-message',index:8,dir:'in'}]);assert.equal(f.created.length,1);assert.equal(f.created[0].blob.type,'video/webm');assert.equal(f.created[0].blob.size,bytes.length);assert.deepEqual(new Uint8Array(await f.created[0].blob.arrayBuffer()),bytes);
  const media=f.video();assert(media);assert.equal(media.className,'an-video-player');assert.equal(media.controls,true);assert.equal(media.playsInline,true);assert.equal(media.preload,'auto');assert.notEqual(media.autoplay,true);assert.equal(media.mediaOps.filter(x=>x.op==='play').length,1);
  f.button('Close video received.webm').click();assert.equal(f.video(),undefined);assert.deepEqual(media.mediaOps.slice(-3).map(x=>x.op),['pause','remove-src','load']);assert.equal(media.src,'');assert.equal(media.paused,true);assert.deepEqual(f.revoked,[f.created[0].url]);
  f.controller.deactivate();f.stop();assert.equal(f.revoked.length,1);checks++;
 }
 for(const file of [{size:INLINE_VIDEO_LIMIT+1},{size:0},{size:-1},{size:NaN},{size:1.5},{size:bytes.length,openable:false},{size:bytes.length,availability:'unavailable'}]){
  const f=fixture({file});assert.equal(all(f.root).filter(n=>n.getAttribute('aria-label')==='Play video received.webm').length,0);assert.equal(f.reads.length,0);assert.equal(f.created.length,0);f.button('Download received.webm').click();await tick();assert.equal(f.downloads.length,1);f.stop();checks++;
 }
 {
  const held=deferred(),f=fixture({file:{size:INLINE_VIDEO_LIMIT},load:()=>held.promise});f.button('Play video received.webm').click();assert.equal(f.reads.length,1,'exact limit is eligible');f.controller.deactivate();held.resolve({bytes});await tick();assert.equal(f.created.length,0);f.stop();checks++;
 }
 for(const [name,body,declared] of [['spoof.mp4',encode('<html>PRIVATE</html>'),20],['oversized-return.webm',new Uint8Array(INLINE_VIDEO_LIMIT+1),4],['different-size.webm',bytes,8],['untyped.webm',new DataView(new ArrayBuffer(4)),4]]){
  const size=name==='spoof.mp4'?body.byteLength:declared;let sniffs=0;const f=fixture({file:{name,size},load:async()=>({bytes:body}),sniffVideo:b=>{sniffs++;return sniffVideo(b);}});f.button('Play video '+name).click();await tick();assert.equal(f.created.length,0);assert.equal(sniffs,name==='spoof.mp4'?1:0,'size/type verified before sniff');assert.equal(f.video(),undefined);assert(!text(f.root).includes('PRIVATE'));assert(text(f.root).includes('Download'));f.stop();checks++;
 }
 {
  const f=fixture({load:async()=>{throw Error('/private/secret plaintext');}});f.button('Play video received.webm').click();await tick();assert.equal(f.created.length,0);assert(!text(f.root).includes('private'));assert(text(f.root).includes('Download'));f.stop();checks++;
 }
 {
  const f=fixture();f.document.play=()=>Promise.reject(new DOMException('Gesture is gone','NotAllowedError'));f.button('Play video received.webm').click();await tick();assert(f.video());assert.equal(f.revoked.length,0);assert(text(f.root).includes('video controls'));assert(!text(f.root).includes('can’t be played'));
  f.document.play=()=>Promise.resolve();await f.video().play();assert.equal(f.reads.length,1);f.stop();assert.equal(f.revoked.length,1);checks++;
 }
 for(const code of [3,4]){
  const f=fixture();f.button('Play video received.webm').click();await tick();const v=f.video();v.error={code,message:'/private codec detail'};v.fire('error');assert.equal(f.video(),undefined);assert(text(f.root).includes('can’t be played'));assert(!text(f.root).includes('private'));assert.equal(f.revoked.length,1);f.stop();assert.equal(f.revoked.length,1);checks++;
 }
 for(const cause of ['deactivate','destroy','hidden','pagehide','native']){
  const f=fixture();f.button('Play video received.webm').click();await tick();const v=f.video();assert(v.isConnected);
  if(cause==='deactivate')f.controller.deactivate();else if(cause==='destroy')f.controller.destroy();else if(cause==='hidden')f.hidden(true);else if(cause==='pagehide')f.window.fire('pagehide');else f.native(false);
  assert.deepEqual(v.mediaOps.slice(-3).map(x=>x.op),['pause','remove-src','load']);assert(v.mediaOps.at(-3).connected,'pause while still connected on '+cause);assert.equal(v.src,'');assert.equal(f.revoked.length,1,cause);
  if(cause==='hidden'){f.hidden(false);f.button('Play video received.webm').click();await tick();assert.equal(f.created.length,2);}
  if(cause==='pagehide'){f.window.fire('pageshow');f.button('Play video received.webm').click();await tick();assert.equal(f.created.length,2);}
  if(cause==='native'){f.native(true);f.button('Play video received.webm').click();await tick();assert.equal(f.created.length,2);}
  f.stop();assert.equal(f.created.length,f.revoked.length);assert.equal(new Set(f.revoked).size,f.revoked.length);checks++;
 }
 for(const cause of ['close','deactivate','destroy','hidden','pagehide','native','removed','allowed']){
  const held=deferred(),f=fixture({load:()=>held.promise});f.button('Play video received.webm').click();assert(text(f.root).includes('Loading'));
  if(cause==='close')f.button('Close video received.webm').click();else if(cause==='deactivate')f.controller.deactivate();else if(cause==='destroy')f.controller.destroy();else if(cause==='hidden')f.hidden(true);else if(cause==='pagehide')f.window.fire('pagehide');else if(cause==='native')f.native(false);else if(cause==='removed')f.root.children[0].remove();else f.setAllowed(false);
  held.resolve({bytes});await tick();assert.equal(f.created.length,0,'late bytes discarded after '+cause);assert.equal(f.video(),undefined);f.stop();checks++;
 }
 {
  const first=deferred(),second=deferred();let call=0;const f=fixture({load:()=>++call===1?first.promise:second.promise});f.button('Play video received.webm').click();f.button('Close video received.webm').click();f.button('Play video received.webm').click();second.resolve({bytes});await tick();const current=f.video();first.reject(Error('old private failure'));await tick();assert.equal(f.video(),current);assert.equal(f.created.length,1);assert.equal(f.revoked.length,0);assert(!text(f.root).includes('private'));f.stop();checks++;
 }
 {
  const oldPlay=deferred(),f=fixture();let plays=0;f.document.play=()=>++plays===1?oldPlay.promise:Promise.resolve();f.button('Play video received.webm').click();await tick();f.button('Close video received.webm').click();f.button('Play video received.webm').click();await tick();oldPlay.reject(new DOMException('late','NotAllowedError'));await tick();assert(!text(f.root).includes('video controls'));assert.equal(f.created.length,2);assert.equal(f.revoked.length,1);f.stop();checks++;
 }
 {
  const f=fixture();f.setAllowed(false);f.button('Play video received.webm').click();await tick();assert.equal(f.reads.length,0);f.setAllowed(true);f.controller.deactivate();f.button('Play video received.webm').click();await tick();assert.equal(f.reads.length,0);f.stop();checks++;
 }
 if(process.env.AGENTNET_VIDEO_SAMPLE){
  const body=new Uint8Array(readFileSync(process.env.AGENTNET_VIDEO_SAMPLE)),f=fixture({file:{name:'sample.webm',size:body.length},load:async()=>({bytes:body})});f.button('Play video sample.webm').click();await tick();assert.equal(f.created[0].blob.type,'video/webm');assert.equal(f.created[0].blob.size,body.length);f.stop();console.log('sample bytes '+body.length+' SHA256 '+createHash('sha256').update(body).digest('hex')+'; classification/Blob only, no real decoding');checks++;
 }
} finally {for(const [key,descriptor] of saved)if(descriptor)Object.defineProperty(globalThis,key,descriptor);else delete globalThis[key];}
console.log('PASS shared video file '+checks+' groups: lazy exact callback, limits/magic, typed Blob, NotAllowed/decode, rooted DOM, all lifecycle paths/late discard and exact once revoke; fake DOM is not real playback');
