import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import {stripTypeScriptTypes} from 'node:module';
import {nativeActivityDetail} from '../skins/shared/native-activity.mjs';
import {nativeActivityDetail as comicActivityDetail} from '../web/src/features/NativeActivity.model.ts';

// Bind the entire parser to the approved Comic source, without a second sanitizer.
const modelURL=new URL('../web/src/features/NativeActivity.model.ts',import.meta.url);
const model=await readFile(modelURL,'utf8');
const source=await readFile(new URL('../skins/shared/native-activity.mjs',import.meta.url),'utf8');
assert.equal(source,stripTypeScriptTypes(model.slice(0,model.indexOf('\nexport function currentNativeActivity'))));

// Execute the existing Comic vectors unchanged, substituting only the parser import.
const vectors=await readFile(new URL('./native_activity_check.mjs',import.meta.url),'utf8');
const oldImport="import { nativeActivityDetail, currentNativeActivity } from '../web/src/features/NativeActivity.model.ts';";
assert.equal(vectors.split(oldImport).length,2);
const replacement=`import {nativeActivityDetail} from '${new URL('../skins/shared/native-activity.mjs',import.meta.url)}';\nimport {currentNativeActivity} from '${modelURL}';`;
await import('data:text/javascript;base64,'+Buffer.from(vectors.replace(oldImport,replacement)).toString('base64'));

const fixed=['Working','Reading files','Editing files','Running a command','Searching','Using a tool','Preparing answer'];
for(const label of fixed)assert.deepEqual(nativeActivityDetail(label),{label,at:undefined});
for(const label of ['Checking the current queue','Reviewing the latest service status','Checking état','Checking status…','𐐀'.repeat(120)]) {
 const detail='Thinking: '+label+'\nReported at 2028-02-29T22:00:00Z';
 assert.deepEqual(nativeActivityDetail(detail),{label,at:'2028-02-29T22:00:00Z'});
 assert.deepEqual(nativeActivityDetail(detail),comicActivityDetail(detail));
}
for(const suffix of ['2026-02-30T22:00:00Z','2026-10-15T25:00:00Z','2026-10-15T22:00:00.000Z','2026-10-15T22:00:00+00:00','2026-10-15','2026-10-15T22:00:00Z\nReported at 2026-10-15T22:00:00Z']) {
 assert.equal(nativeActivityDetail('Reading files\nReported at '+suffix),null);
}
for(const value of ['𐐀'.repeat(121),' Checking status','Checking status ','Checking\tstatus','Checking  status','Checking status.','Checking ｓｔａｔｕｓ','Checking a123456789012345','Checking\u2066status','Using password value','Checking C:\\private\\file']) {
 const detail='Thinking: '+value;
 assert.equal(nativeActivityDetail(detail),null);
 assert.equal(comicActivityDetail(detail),null);
}
console.log('PASS shared native activity: exact stripped Comic parser, all existing Comic vectors, fixed/changing Unicode summaries, rune/time bounds and unsafe detail refusals');
