// Source-extraction regression only. No browser, backend or release coverage.
const assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path');
const {scanJsx,attrs}=require('./interaction_inventory.cjs');
let checks=0;
function check(name,fn){fn();checks++;console.log('PASS '+name);}
const after='<button onClick={() => next()}>Next</button>';
for(const [name,body] of [
  ['line comment',"// modal needn't restore } > it.\n next();"],
  ['block comment',"/* user's } > { comment */ next();"],
  ['quoted strings',`next("it's } >", '{ brace', "escaped \\\" quote");`],
  ['template expressions','next(`user\'s ${ {nested: `inner ${"}"}`}.nested } >`);'],
  ['nested braces','if (ready) { next({nested: {value: "}"}}); }'],
])check(name,()=>{
  const text='<Button onClick={() => { '+body+' }}>Close</Button>'+after;
  const nodes=scanJsx(text);assert.equal(nodes.length,2);
  assert.deepEqual(attrs(nodes[0]).map(x=>x.name),['onClick']);
  assert.equal(attrs(nodes[0])[0].value,'{() => { '+body+' }}');
  assert.equal(text.slice(attrs(nodes[0])[0].valueStart,attrs(nodes[0])[0].valueEnd),attrs(nodes[0])[0].value);
  assert.deepEqual(attrs(nodes[1]).map(x=>x.name),['onClick']);
});
check('TS generic candidates preserve following JSX',()=>{
  const text='const x=useRef<HTMLElement>(null); const y=useRef<Pick<Person, "id">[]>([]);'+after;
  const inputs=scanJsx(text).filter(n=>attrs(n).some(x=>x.name==='onClick'));
  assert.equal(inputs.length,1);assert.equal(inputs[0].tag,'button');
});
for(const [name,text] of [
 ['unclosed opening tag','<button onClick={next}'],
 ['unclosed expression','<button onClick={() => { next(); } />'],
 ['unclosed string','<button title="missing >'],
 ['unclosed block comment','<button onClick={() => { /* never closed } />'],
 ['unclosed template','<button onClick={() => next(`never closed)} />'],
])check(name,()=>assert.throws(()=>scanJsx(text),/unterminated|unclosed/i));
check('non-JSX fragment gap is explicit and later candidates survive',()=>{
 const warnings=[],nodes=scanJsx('<a title="unfinished '+after,warnings);
 assert.equal(warnings.length,1);assert.match(warnings[0].error,/unterminated/);
 assert.equal(nodes.length,1);assert.equal(nodes[0].tag,'button');
});
const expected={
 'Teams.tsx':[['Button','Close {noun} search'],['Button','Show more {noun}'],['select','value={team} onChange'],['Button','Add list’s people</Button>'],['Button','Retry list selection</Button>'],['Button','Cancel list selection</Button>'],['Button','Remove {personLabel(o, p.id)}']],
 // Nested JSX inside Timeline.footer remains grouped under Timeline. Verify
 // the enclosing tag and subsequent siblings; do not claim its child input.
 'Conversation.tsx':[['Timeline','key={open.id + ":"'],['button','aria-label="Stop selecting"'],['button','>Try again</button>']],
 'Conversation.topics.tsx':[['Button','key={id} variant="ghost"'],['input','id="rename-topic-name" value={title}'],['Button','>Open {ctx.dm.topics'],['Button','aria-busy={!!busy} icon={<IconRotateClockwise']],
};
for(const [file,anchors] of Object.entries(expected))check('actual source '+file,()=>{
 const source=fs.readFileSync(path.join(__dirname,'../web/src/features',file),'utf8');
 const nodes=scanJsx(source),found=new Set(nodes.filter(n=>attrs(n).some(a=>/^on[A-Z]/.test(a.name))).map(n=>source.slice(0,n.start).split('\n').length));
 for(const [tag,marker] of anchors){
  const at=source.indexOf(marker);assert(at>=0,file+' source anchor missing: '+marker);
  const start=source.lastIndexOf('<'+tag,at);assert(start>=0,file+' opening tag missing: '+marker);
  const line=source.slice(0,start).split('\n').length;
  assert.ok(found.has(line),file+':'+line+' missing input/callback source');
 }
});
console.log(JSON.stringify({checks_passed:checks,failed:0,skipped:0,scope:'source scanner only'}));
