// Transport/source configuration only; zero rendered qualification.
const assert=require('node:assert/strict');
const {topicRenameFixture}=require('./c1_topic_rename_fixture.cjs');
const {entry}=require('./c1_topic_rename.cjs');
async function main(){
 let count=0;const config={placement:'person',mode:'held',history:10};
 for(const bad of [null,{...config,placement:'unknown'},{...config,mode:'unknown'},{...config,history:0}]){assert.throws(()=>entry(bad),/C1 rename fixture/);count++;}
 const f=topicRenameFixture(config),read=await f.api('/api/dm?id='+f.conv);
 assert.equal(read.messages.length,10);assert.equal(read.kind,'dm');assert.equal(read.topics[0].title,'Original renamed topic');count++;
 const typing=await f.api('/api/typing?conv='+f.conv);assert.equal(typing.scope.conv,f.conv);assert.equal(typing.preferences.send,false);count++;
 let finished=false;const promise=f.api('/api/topic/rename',{conv:f.conv,id:f.topic,title:'New title'}).then(()=>{finished=true;});
 await Promise.resolve();assert.equal(finished,false);assert.equal(f.witness().pending,1);assert.equal(f.witness().confirmed_title,'Original renamed topic');count++;
 f.settle(1,'resolve');await promise;assert.equal(f.witness().confirmed_title,'New title');assert.equal(f.witness().pending,0);count++;
 assert.throws(()=>f.settle(1,'resolve'),/unknown\/completed/);count++;
 await assert.rejects(f.api('/api/topic/rename',{conv:'wrong',id:f.topic,title:'Bad'}),/exact named topic/);count++;
 const reset=f.api('/api/topic/rename',{conv:f.conv,id:f.topic,title:''});f.settle(2,'resolve');await reset;assert.equal(f.witness().confirmed_title,'Automatic topic title');count++;
 const failing=topicRenameFixture({...config,placement:'topicbar',mode:'failing',history:1});
 assert.equal((await failing.api('/api/dm?id='+failing.conv)).kind,'group');
 await assert.rejects(failing.api('/api/topic/rename',{conv:failing.conv,id:failing.topic,title:'No'}),/Synthetic rename refused/);assert.equal(failing.witness().confirmed_title,'Original renamed topic');count++;
 await assert.rejects(f.api('/api/groups/new',{title:'Unexpected'}),/Unexpected C1 rename request/);assert.equal(f.witness().unexpected.length,1);count++;
 const source=entry(config);assert(source.includes("import {Conversation} from './src/features/Conversation'"));assert(source.includes('await store.open'));assert(!source.includes('__c1RenameReady'));count++;
 console.log(JSON.stringify({fixture_checks_passed:count,failed:0,skipped:0,rendered_cases:0,release_qualified:false}));
}
main().catch(e=>{console.error(e.stack);process.exitCode=1;});
