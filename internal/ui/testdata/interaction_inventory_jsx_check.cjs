// Compiler-source ownership regressions. Discovery is not rendered coverage.
const fs=require('node:fs'),os=require('node:os'),path=require('node:path'),assert=require('node:assert/strict');
const {jsxSourceNodes,attrs,tag}=require('./interaction_inventory.cjs');
async function main(){
 const dir=fs.mkdtempSync(path.join(os.tmpdir(),'agr-jsx-source-')),sources={
  nested:`const v=<Sheet onOpenChange={close} footer={<div><Button onClick={() => save('')}>Reset</Button><Button type="submit" form="rename-topic">Save</Button></div>}><input onChange={edit}/></Sheet>;`,
  conditional:`const v=<><Timeline onSelect={select} footer={left && (<div><Button onClick={bringBack}>Bring back</Button></div>)} /><button onClick={next}>Next</button></>;`,
  strings:`const v=<Field<Value> title={"<button onClick={fake}>"} onChange={update} footer={<div>Don't <Button onClick={real}>go</Button></div>} />;`,
  literals:'const v=<Box description={`literal <button onClick={fake}> ${"not markup"}`} footer={/* user\'s > } comment */ <button onClick={()=>{const x={a:"}"};return x;}}>Go</button>} />;',
  spreads:'const v=<Box {...props} footer={<button {...buttonProps} onClick={real}>Go</button>} />;',
 };
 let checks=0;const check=(name,fn)=>{fn();checks++;console.log('PASS '+name);};
 try{
  const files=Object.entries(sources).map(([name,text])=>{const file=path.join(dir,name+'.tsx');fs.writeFileSync(file,text);return file;});
  const parsed=await jsxSourceNodes(files);const nodes=name=>parsed.get(path.join(dir,name+'.tsx'));
  check('nested attributes belong only to their own physical node',()=>{
   assert.deepEqual(attrs(nodes('nested')[0]).map(a=>a.name),['onOpenChange']);
   const buttons=nodes('nested').filter(n=>n.tag==='Button');assert.equal(buttons.length,2);
   assert.deepEqual(attrs(buttons[0]).map(a=>a.name),['onClick']);assert.deepEqual(attrs(buttons[1]),[]);
   assert.equal(nodes('nested').filter(n=>n.tag==='input').length,1);
  });
  check('conditional footer discovers child and preserves following sibling offsets',()=>{
   const ns=nodes('conditional');assert.deepEqual(attrs(ns[0]).map(a=>a.name),['onSelect']);
   const child=ns.find(n=>n.tag==='Button'),sibling=ns.find(n=>n.tag==='button');assert(child&&sibling);
   assert.equal(child.start,sources.conditional.indexOf('<Button'));assert.equal(sibling.start,sources.conditional.indexOf('<button'));
   for(const n of ns)assert.equal(sources.conditional.slice(n.start,n.end).at(-1),'>');
  });
  check('generic JSX and quoted markup add no fake controls',()=>{
   const ns=nodes('strings');assert.deepEqual(ns.map(n=>n.tag),['Field','div','Button']);
   assert.equal(sources.strings.slice(ns[0].start,ns[0].tagEnd),'<Field<Value>');
   assert.deepEqual(attrs(ns[0]).map(a=>a.name),['title','onChange']);
  });
  check('template literals comments and nested braces preserve only real ownership',()=>{
   const ns=nodes('literals');assert.deepEqual(ns.map(n=>n.tag),['Box','button']);assert.deepEqual(attrs(ns[0]),[]);
   const a=attrs(ns[1])[0];assert.equal(a.name,'onClick');assert.equal(sources.literals.slice(a.valueStart,a.valueEnd),a.value);
  });
  check('opaque spreads remain explicit without fabricated handlers',()=>{
   const ns=nodes('spreads');assert.deepEqual(ns.map(n=>n.tag),['Box','button']);
   assert.deepEqual(ns.map(n=>n.opaqueSpreads),[1,1]);assert.deepEqual(attrs(ns[0]),[]);
   assert.deepEqual(attrs(ns[1]).map(a=>a.name),['onClick']);
  });
  const root=path.resolve(__dirname,'../web/src/features'),real=['Conversation.tsx','Conversation.topics.tsx','Teams.tsx'].map(f=>path.join(root,f));
  const actual=await jsxSourceNodes(real);
  for(const [file,line,tag] of [['Conversation.tsx',116,'Button'],['Conversation.topics.tsx',353,'Button'],['Conversation.topics.tsx',354,'Button'],['Conversation.topics.tsx',359,'input']])check('actual source '+file+':'+line,()=>{
   const text=fs.readFileSync(path.join(root,file),'utf8');assert(actual.get(path.join(root,file)).some(n=>n.tag===tag&&text.slice(0,n.start).split('\n').length===line));
  });
  for(const [file,expected] of [
   ['Conversation.tsx',[[111,['onSelect']],[116,['onClick']]]],
   ['Conversation.topics.tsx',[[349,['onOpenChange','title']],[353,['onClick']],[354,[]],[357,['onSubmit']],[359,['onChange']]]],
   ['Teams.tsx',[[270,['onClick']],[277,['onClick']]]],
  ])check('actual own attributes '+file,()=>{
   const text=fs.readFileSync(path.join(root,file),'utf8'),ns=actual.get(path.join(root,file));
   for(const [line,names] of expected){const n=ns.find(n=>text.slice(0,n.start).split('\n').length===line);assert(n,file+':'+line);assert.deepEqual(attrs(n).map(a=>a.name),names,file+':'+line);}
  });
  // Tag the same source spans, then parse again: parent wrappers must not consume
  // descendant handlers, generic type arguments or following siblings.
  await tag(dir);const tagged=await jsxSourceNodes(files);
  check('tagging preserves nested ownership and wraps every own handler once',()=>{
   for(const file of files){
    const before=parsed.get(file),after=tagged.get(file);assert.deepEqual(after.map(n=>n.tag),before.map(n=>n.tag));
    for(let i=0;i<before.length;i++){
     assert.deepEqual(attrs(after[i]).map(a=>a.name),attrs(before[i]).map(a=>a.name));
     for(const a of attrs(after[i]).filter(a=>/^on[A-Z]/.test(a.name))){
      assert.equal((a.value.match(/globalThis\.__anTrack/g)||[]).length,1);assert(a.value.includes('/'+before[i].tag+'/'));
     }
    }
    const text=fs.readFileSync(file,'utf8'),expected={nested:4,conditional:3,strings:2,literals:1,spreads:1}[path.basename(file,'.tsx')];
    assert.equal((text.match(/ data-an-control=/g)||[]).length,expected,'no false-positive control tags: '+file);
    if(file.endsWith('strings.tsx'))assert(text.includes('<Field<Value> data-an-control='));
   }
  });
  for(const [name,text] of [
   ['unclosed JSX','const v=<Sheet footer={<button onClick={go}>Go</button>}'],
   ['unclosed comment','const v=<button onClick={()=>{/* missing }}/>;'],
   ['unclosed template','const v=<button title={`missing}/>;'],
  ]){
   const file=path.join(dir,name+'.tsx');fs.writeFileSync(file,text);
   await assert.rejects(jsxSourceNodes([file]),/JSX parser syntax error/);checks++;console.log('PASS reject '+name);
  }
  console.log(JSON.stringify({checks_passed:checks,failed:0,skipped:0,scope:'source ownership only',rendered_cases:0}));
 }finally{fs.rmSync(dir,{recursive:true,force:true});}
}
main().catch(e=>{console.error(e.stack);process.exitCode=1;});
