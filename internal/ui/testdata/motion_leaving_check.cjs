// Exercise real React context through the actual nested Swap component.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { createRequire } = require('node:module');
const modules = process.env.AGENTNET_WEB_MODULES;
assert(modules, 'point AGENTNET_WEB_MODULES at the existing pinned node_modules');
const web = createRequire(path.resolve(modules, '../package.json'));
const React = web('react'), { renderToStaticMarkup } = web('react-dom/server');
const source = fs.readFileSync(path.join(__dirname, '../web/src/ui/Motion.tsx'), 'utf8');
const compiled = web('esbuild').transformSync(source, {loader:'tsx',format:'cjs',jsx:'automatic',target:'es2022'});
const mod = { exports:{} };
new Function('require','module','exports',compiled.code)(web,mod,mod.exports);
const { Swap, Leaving, useLeaving } = mod.exports;
function Probe() { return React.createElement('span',{'data-leaving':String(useLeaving())}); }
for(const parent of [false,true]) for(const depth of [1,3]) {
  let child=React.createElement(Probe);
  for(let i=0;i<depth;i++) child=React.createElement(Swap,{id:'stable-'+i,enter:'',leave:'',ms:220},child);
  const html=renderToStaticMarkup(React.createElement(Leaving.Provider,{value:parent},child));
  assert(html.includes('data-leaving="'+parent+'"'),'parent '+parent+' must reach current child through '+depth+' nested Swaps: '+html);
}
console.log('nested Swap Leaving PASS: false remains false; parent true reaches current children at one and three levels');
