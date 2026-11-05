#!/usr/bin/env node
/* Phase-one source inventory and source-preserving test tagger. */
const fs = require('node:fs');
const path = require('node:path');
const { pathToFileURL } = require('node:url');
const root = path.resolve(__dirname, '../../..');
const typeScriptRoot = path.join(root, 'internal/ui/web/node_modules/typescript');
const skinsRoot = process.env.AGENTNET_SKINS_ROOT || '/media/misunderstood/DATA/projects/agentnet-skins';
const ignored = new Set(['onLoad','onLoadedData','onLoadedMetadata','onCanPlay','onCanPlayThrough','onAnimationStart','onAnimationEnd','onAnimationIteration','onTransitionEnd']);
const intrinsic = new Set(['a','button','input','textarea','select','summary','details','option']);
const primitives = new Set(['Menu.Item','Menu.Trigger','Dialog.Close','Popover.Trigger','Collapsible.Trigger','Switch.Root','Radio.Root']);
const roles = new Set(['button','link','menuitem','menuitemcheckbox','menuitemradio','tab','switch','radio','checkbox','combobox','textbox','option','slider','spinbutton','treeitem']);
const userEvents = new Set(['keydown','keyup','paste','drop','dragstart','dragover','dragend','contextmenu']);

function files(dir,pred){if(!fs.existsSync(dir))return[];return fs.readdirSync(dir,{withFileTypes:true}).flatMap(e=>{const f=path.join(dir,e.name);return e.isDirectory()?files(f,pred):pred(f)?[f]:[];}).sort();}
function rel(f,base=root){return path.relative(base,f).split(path.sep).join('/');}
function line(text,at){return text.slice(0,at).split('\n').length;}
function unquote(value){const m=value.match(/^\s*(?:"([\s\S]*)"|'([\s\S]*)'|\{\s*["']([\s\S]*?)["']\s*\})\s*$/);return m?(m[1]??m[2]??m[3]??''):'';}
function componentBefore(text,at){const m=[...text.slice(0,at).matchAll(/(?:export\s+)?(?:function|class)\s+([A-Za-z_$][\w$]*)|(?:const|let|var)\s+([A-Za-z_$][\w$]*)\s*=\s*(?:forwardRef\s*\(|(?:async\s*)?\(?[^=;{}]*\)?\s*=>)/g)].at(-1);return m?(m[1]||m[2]):'Element';}
function lexicalCheck(scanner,text,file){try{scanner.createScanner(true,undefined,text).scan();}catch(e){throw Error(`TypeScript scanner failed for ${file}: ${e.message}`);}}
// Bound attribute expressions lexically. Comments cannot open strings or alter
// brace depth; template interpolations use the same boundary rules recursively.
// This remains a source-candidate extractor, not a JavaScript/TypeScript parser.
function quotedEnd(text,start){
  const quote=text[start];
  for(let p=start+1;p<text.length;p++){
    if(text[p]==='\\'){p++;continue;}
    if(text[p]===quote)return p+1;
    if(quote==='`'&&text[p]==='$'&&text[p+1]==='{')p=expressionEnd(text,p+1)-1;
  }
  throw Error(`unterminated quoted literal at ${start}`);
}
function expressionEnd(text,start){
  let depth=1;
  for(let p=start+1;p<text.length;p++){
    const c=text[p];
    if(c==='/'&&text[p+1]==='/'){
      const newline=text.indexOf('\n',p+2);
      if(newline<0)break;
      p=newline;continue;
    }
    if(c==='/'&&text[p+1]==='*'){
      const end=text.indexOf('*/',p+2);
      if(end<0)throw Error(`unterminated block comment at ${p}`);
      p=end+1;continue;
    }
    if(c==='"'||c==="'"||c==='`'){p=quotedEnd(text,p)-1;continue;}
    if(c==='{')depth++;
    else if(c==='}'&&!--depth)return p+1;
  }
  throw Error(`unterminated attribute expression at ${start}`);
}
function scanJsx(text,fragmentWarnings){
  const out=[];let i=0;
  while(i<text.length){
    const start=text.indexOf('<',i);if(start<0)break;
    if(!/^<\/?(?:[a-z][\w-]*|[A-Z][\w$.]*)(?:\s|\/?>)/.test(text.slice(start))){i=start+1;continue;}
    if(text[start+1]==='/'){i=start+2;continue;}
    const m=text.slice(start).match(/^<([A-Za-z_$][\w$.-]*)/);
    if(!m){i=start+1;continue;}
    const tag=m[1],tagEnd=start+1+tag.length;let p=tagEnd;
    try{
      for(;p<text.length;p++){
        const c=text[p];
        if(c==='{'){p=expressionEnd(text,p)-1;continue;}
        if(c==='"'||c==="'"){p=quotedEnd(text,p)-1;continue;}
        if(c==='>')break;
      }
      if(p>=text.length)throw Error(`unterminated opening tag <${tag}> at ${start}`);
    }catch(error){
      // Non-JSX modules contain concatenated HTML/regex fragments (not JSX).
      // Expose their heuristic extraction gaps; never silently drop the tail.
      // TS/TSX inventory and tagging omit this argument and fail immediately.
      if(!fragmentWarnings)throw error;
      fragmentWarnings.push({line:line(text,start),tag,error:error.message});
      i=start+1;continue;
    }
    out.push({start,tag,tagEnd,end:p+1,attrs:text.slice(tagEnd,p),attrsStart:tagEnd});i=p+1;
  }
  return out;
}
function attrs(node){const out=[],rx=/\b(on[A-Z][\w$-]*|role|tabIndex|aria-label|title)\s*=\s*/g;let m;while((m=rx.exec(node.attrs))){const p=rx.lastIndex;let end=p;if(node.attrs[p]==='{')end=expressionEnd(node.attrs,p);else if(node.attrs[p]==='"'||node.attrs[p]==="'")end=quotedEnd(node.attrs,p);else{while(end<node.attrs.length&&!/[\s/>]/.test(node.attrs[end]))end++;}const value=node.attrs.slice(p,end);out.push({name:m[1],value,start:node.attrsStart+m.index,valueStart:node.attrsStart+p,valueEnd:node.attrsStart+end});rx.lastIndex=end;}return out;}
// Single auditable policy: every JSX tag, callback, role, primitive and listener uses this classifier.
function classify(node,text,file,repo,population,excluded){const a=attrs(node),all=a.filter(x=>/^on[A-Z]/.test(x.name)),handlers=all.filter(x=>!ignored.has(x.name));for(const x of all.filter(x=>ignored.has(x.name)))excluded[x.name]=(excluded[x.name]||0)+1;const role=unquote(a.find(x=>x.name==='role')?.value||''),label=unquote((a.find(x=>x.name==='aria-label')||a.find(x=>x.name==='title'))?.value||''),base=node.tag.split('.').at(-1).toLowerCase();let kind='';if(primitives.has(node.tag))kind='base-ui-primitive';else if(handlers.length&&/^[A-Z]/.test(node.tag))kind='component-callback';else if(intrinsic.has(base))kind='intrinsic';else if(roles.has(role))kind='interactive-role';else if(a.some(x=>x.name==='tabIndex'))kind='tabindex';else if(handlers.length)kind='handler';return kind?{population,kind,element:node.tag,component:componentBefore(text,node.start),handlers:handlers.map(x=>x.name),literal_label:label,source_ref:{repo,path:file,line:line(text,node.start)},node,handlerAttrs:handlers}:null;}
function listenerRows(text,file,repo,population,excluded){const rows=[],add=(kind,element,event,at)=>{if(['load','animationend','animationstart','transitionend'].includes(event)){excluded['listener:'+event]=(excluded['listener:'+event]||0)+1;return;}rows.push({population,kind,element,component:componentBefore(text,at),handlers:[event],literal_label:'',source_ref:{repo,path:file,line:line(text,at)}});};let m;const native=/\b(window|document|globalThis|[A-Za-z_$][\w$]*)\.addEventListener\s*\(\s*["']([^"']+)["']/g;while((m=native.exec(text))){const[,target,event]=m;if(['window','document','globalThis'].includes(target)||userEvents.has(event))add('global-listener',target,'addEventListener:'+event,m.index);}const helper=/\bon\s*\(\s*[^,\n]+,\s*["']([^"']+)["']/g;while((m=helper.exec(text)))add('listener-helper','on','on:'+m[1],m.index);const factory=/\bel\s*\(\s*["'](button|input|textarea|select|a|summary)["']\s*,\s*\{([\s\S]{0,800}?)\}/g;while((m=factory.exec(text))){const handlers=[...m[2].matchAll(/\b(on(?:click|change|input|submit|keydown|keyup|paste|drop|drag\w*|contextmenu))\s*:/gi)].map(x=>x[1]);if(handlers.length)rows.push({population,kind:'factory-control',element:m[1],component:componentBefore(text,m.index),handlers,literal_label:'',source_ref:{repo,path:file,line:line(text,m.index)}});}return rows;}
function wrapperRows(text,file,repo,population){const rows=[],rx=/(?:export\s+)?(?:function\s+([A-Za-z_$][\w$]*)[^{}]*\{|const\s+([A-Za-z_$][\w$]*)[^=]*=\s*(?:forwardRef\s*\()?[^{}]*=>\s*\{)([\s\S]*?)(?=\n\}|\nexport |\nconst |$)/g;let m;while((m=rx.exec(text)))if(/<[A-Za-z_$][\w$.-]*\b[^>]*\{\.\.\.[^}]+\}/.test(m[3]))rows.push({population,kind:'forwarding-wrapper',element:m[1]||m[2],component:m[1]||m[2],handlers:[],literal_label:'',source_ref:{repo,path:file,line:line(text,m.index)}});return rows;}
function sourceRows(list,population,repo,base,scanner){const rows=[],excluded={},extractionWarnings=[];for(const abs of list){const text=fs.readFileSync(abs,'utf8');lexicalCheck(scanner,text,abs);const file=rel(abs,base),fragmentWarnings=/\.tsx?$/.test(file)?undefined:[];let nodes;try{nodes=scanJsx(text,fragmentWarnings);}catch(e){throw Error(file+': '+e.message);}if(fragmentWarnings)extractionWarnings.push(...fragmentWarnings.map(w=>({path:file,...w})));for(const n of nodes){const r=classify(n,text,file,repo,population,excluded);if(r)rows.push(r);}rows.push(...listenerRows(text,file,repo,population,excluded),...wrapperRows(text,file,repo,population));}return{rows,excluded,extractionWarnings};}
function nativeRows(list,population,repo,base){const rows=[],excluded={},add=(abs,event,lineNo)=>{if(/^(load|animation|transition)/i.test(event)){excluded[event]=(excluded[event]||0)+1;return;}rows.push({population,kind:population==='native-android'?'native-bridge-or-listener':'native-command-or-event',element:event,component:'native',handlers:[event],literal_label:'',source_ref:{repo,path:rel(abs,base),line:lineNo}});};for(const abs of list){let command=false;fs.readFileSync(abs,'utf8').split(/\r?\n/).forEach((s,i)=>{if(population==='native-desktop'){if(/#\[tauri::command\]/.test(s)){command=true;return;}const fn=command&&s.match(/\bfn\s+([A-Za-z_$][\w$-]*)/);if(fn){add(abs,'tauri-command:'+fn[1],i+1);command=false;return;}if(/on_menu_event/.test(s))add(abs,'menu-event',i+1);if(/page_event/.test(s))add(abs,'page-event',i+1);if(/addEventListener/.test(s))add(abs,'addEventListener',i+1);return;}const m=s.match(/(?:addEventListener|setOn[A-Z][\w]*Listener|@JavascriptInterface|\bfun\s+)([A-Za-z_$][\w$-]*)?/);if(m)add(abs,m[1]||'event',i+1);});}return{rows,excluded};}
function finalize(rows){const seen=new Map();for(const r of rows){const sig=`${r.source_ref.repo}:${r.source_ref.path}#${r.component}/${r.element}/${r.handlers.join(',')}`;const n=(seen.get(sig)||0)+1;seen.set(sig,n);r.definition_id=`${sig}#${n}`;}return rows.sort((a,b)=>a.definition_id.localeCompare(b.definition_id));}
function sources(){const src=path.join(root,'internal/ui/web/src'),good=f=>/\.(tsx|ts)$/.test(f)&&!/(^|[./])(test|spec)\.[^.]+$|__tests__/.test(f),skin=s=>path.join(skinsRoot,'skins',s);return{comic:files(src,good),overlay:['office','pulp'].flatMap(s=>files(path.join(skin(s),'overlay'),f=>f.endsWith('.tsx'))),standalone:['classic','holonet','zoom'].flatMap(s=>files(path.join(skin(s),'src'),f=>path.dirname(f).endsWith('/src')&&f.endsWith('.mjs'))),static:[...files(path.join(root,'internal/ui/static'),f=>/\.(js|mjs)$/.test(f)),...files(path.join(root,'internal/ui/skins/shared'),f=>f.endsWith('.mjs'))],desktop:files(path.join(root,'desktop/src-tauri/src'),f=>/\.(rs|js|ts)$/.test(f)),android:files(path.join(root,'mobile/android/app/src/main'),f=>/\.(kt|java|xml)$/.test(f))};}
async function scanner(){return import(pathToFileURL(path.join(typeScriptRoot,'dist/ast/scanner.js')).href);}
async function inventory(){const ts=await scanner(),s=sources(),populations={},all=[],add=(name,x)=>{const rows=finalize(x.rows.map(r=>({...r,population:name})));populations[name]={count:rows.length,excluded_non_user_handlers:x.excluded,non_jsx_fragment_warnings:x.extractionWarnings||[]};all.push(...rows);};add('comic-react',sourceRows(s.comic,'comic-react','agentnet',root,ts));add('overlay',sourceRows(s.overlay,'overlay','agentnet-skins',skinsRoot,ts));add('standalone',sourceRows(s.standalone,'standalone','agentnet-skins',skinsRoot,ts));add('static',sourceRows(s.static,'static','agentnet',root,ts));add('native-desktop',nativeRows(s.desktop,'native-desktop','agentnet',root));add('native-android',nativeRows(s.android,'native-android','agentnet',root));const comicText=[path.join(root,'internal/ui/web/build.sh'),...s.comic].filter(fs.existsSync).map(f=>fs.readFileSync(f,'utf8')).join('\n');return{schema:'agentnet-interaction-inventory/v1',generated_from:{agentnet_root:root,skins_root:fs.existsSync(skinsRoot)?skinsRoot:null,typescript_compiler_scanner:'7.0.2'},classification:{function:'classify',excluded_non_user_handlers:[...ignored],note:'Source-candidate discovery only: lexical bootstrap is not full JS/TS validation. TS/TSX boundary errors fail; non-JSX HTML fragments expose warnings. Nested JSX prop children, generic ambiguity and dynamic instances remain unclosed; this is not a complete semantic control denominator.'},populations,static_files:s.static.map(f=>({path:rel(f),loaded_by_comic_build:comicText.includes(path.basename(f))})),controls:all.sort((a,b)=>a.definition_id.localeCompare(b.definition_id))};}
async function tag(dir){const ts=await scanner(),canonical=path.join(root,'internal/ui/web/src'),list=files(dir,f=>/\.(tsx|ts)$/.test(f)&&!/(^|[./])(test|spec)\.[^.]+$|__tests__/.test(f));for(const abs of list){const text=fs.readFileSync(abs,'utf8');lexicalCheck(ts,text,abs);const rows=[],excluded={},file=rel(path.join(canonical,path.relative(dir,abs)));for(const n of scanJsx(text)){const r=classify(n,text,file,'agentnet','comic-react',excluded);if(r)rows.push(r);}finalize(rows);const inserts=[];for(const r of rows){inserts.push({at:r.node.tagEnd,text:` data-an-control=${JSON.stringify(r.definition_id)}`});for(const a of r.handlerAttrs){if(!a.value.startsWith('{')||!a.value.endsWith('}'))continue;inserts.push({at:a.valueStart+1,text:`(globalThis.__anTrack||((_,f)=>f))(${JSON.stringify(r.definition_id+'#'+a.name)},`},{at:a.valueEnd-1,text:')'});}}let out=text;for(const x of inserts.sort((a,b)=>b.at-a.at))out=out.slice(0,x.at)+x.text+out.slice(x.at);fs.writeFileSync(abs,out);}return{tagged_source:dir,files:list.length};}
async function main(){const a=process.argv.slice(2),tagAt=a.indexOf('--tag'),outAt=a.indexOf('--output');if(tagAt>=0){if(!a[tagAt+1])throw Error('usage: --tag <srcdir>');process.stdout.write(JSON.stringify(await tag(path.resolve(a[tagAt+1])))+'\n');return;}const x=await inventory(),json=JSON.stringify(x,null,2);if(outAt>=0){if(!a[outAt+1])throw Error('usage: --output <file>');fs.writeFileSync(a[outAt+1],json+'\n');}process.stdout.write(json+'\n');for(const [population,p] of Object.entries(x.populations))for(const w of p.non_jsx_fragment_warnings)process.stderr.write('interaction inventory non-JSX fragment warning: '+population+' '+JSON.stringify(w)+'\n');process.stderr.write('interaction inventory: '+Object.entries(x.populations).map(([k,v])=>`${k}=${v.count}`).join(' ')+` total=${x.controls.length}\n`);}
module.exports={scanJsx,attrs};
if(require.main===module)main().catch(e=>{process.stderr.write(`interaction inventory failed: ${e.stack||e}\n`);process.exitCode=1;});
