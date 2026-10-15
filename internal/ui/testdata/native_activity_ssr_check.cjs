const path=require('node:path'),assert=require('node:assert/strict');
const {createRequire}=require('node:module');
const deps=createRequire(path.resolve(__dirname,'../web/package.json'));
const esbuild=deps('esbuild'),React=deps('react'),{renderToStaticMarkup}=deps('react-dom/server');
const result=esbuild.buildSync({entryPoints:[path.resolve(__dirname,'../web/src/features/NativeActivity.tsx')],bundle:true,platform:'node',format:'cjs',jsx:'automatic',write:false,logLevel:'silent'});
const moduleResult={exports:{}};
new Function('module','exports','require',result.outputFiles[0].text)(moduleResult,moduleResult.exports,deps);
const {NativeActivity}=moduleResult.exports;
const stamp='2026-10-15T22:00:00Z';
const render=detail=>renderToStaticMarkup(React.createElement(NativeActivity,{detail}));
const html=render('Reading files\nReported at '+stamp);
for(const expected of ['role="status"','Reading files','Native event reported','dateTime="'+stamp+'"','title="'+stamp+'"'])
 assert.ok(html.includes(expected),expected);
assert.ok(render('Using a tool').includes('Native activity'),'untimed detail is an honest fallback');
for(const raw of ['PRIVATE','Reading files\nReported at PRIVATE','Waiting for native input'])
 assert.equal(render(raw),'','unknown content and wait states keep existing presentation');
console.log('Native activity SSR safe line/exact report time/provenance PASS');
