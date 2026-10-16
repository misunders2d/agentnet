// Exercise the actual pure Settings preview control using the pinned React.
const assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path');
const {createRequire}=require('node:module');
const web=createRequire(path.resolve(process.env.AGENTNET_WEB_MODULES,'../package.json'));
const React=web('react'),{renderToStaticMarkup}=web('react-dom/server');
const source=fs.readFileSync(path.join(__dirname,'../web/src/features/Settings.notify.tsx'),'utf8');
const compiled=web('esbuild').transformSync(source,{loader:'tsx',format:'cjs',jsx:'automatic',target:'es2022'});
const mod={exports:{}};
// Only the pure exported control is invoked; other section dependencies stay unused.
new Function('require','module','exports',compiled.code)(id=>id.startsWith('.')?{}:web(id),mod,mod.exports);
const Control=mod.exports.NotificationPreviewControl;
const render=n=>renderToStaticMarkup(React.createElement(Control,{n,busy:false,onChange:()=>assert.fail('render must not change preferences')}));
for(const [fallback,label] of [['hidden','Hidden'],['full','Full message preview'],['sender','Sender only']]) {
 for(const mode of ['', 'full','sender','hidden']) {
  const html=render({native:true,preview_default:fallback,preview:mode});
  assert(html.includes('Default ('+label+')'),html);
  const selected=html.match(/<option[^>]*value="([^"]*)"[^>]*selected=""/);
  assert.equal(selected?.[1],mode,html);
  for(const text of ['Hidden omits names and message text, while keeping fixed activity, reminder, decision or security notices.','Message previews','Full message preview','Sender only','Hidden','does not turn notifications on','lock screen','notification history','On macOS']) assert(html.includes(text),text);
 }
}
assert(!render({native:true}).includes('<select'));
assert(!render({native:true,preview_default:'unknown'}).includes('<select'));
const browser=render({native:false,preview_default:'full'});
assert(!browser.includes('<select')&&browser.includes('always hide message content'));
const busy=renderToStaticMarkup(React.createElement(Control,{n:{native:true,preview_default:'hidden'},busy:true,onChange:()=>{}}));
assert(busy.includes('disabled=""'));
console.log('Settings preview PASS: all modes, actual defaults, unsupported/browser limits, local privacy notes, busy state; pure rendering changes no consent');
