import assert from 'node:assert/strict';
import { rawLinkParts, shortLinkLabel } from '../skins/shared/link-text.mjs';
const url='https://example.test/'+('long/'.repeat(25))+'?q=x%2Fy&u=%E2%9C%93#frag';
for(const source of [url,'See '+url+'.','<'+url+'>','**'+url+'**','- '+url,'> '+url]) {
 const parts=rawLinkParts(source).filter(p=>typeof p!=='string');
 assert.equal(parts.length,1,source);assert.equal(parts[0].href,url);assert.equal(parts[0].label,'example.test/…');
}
for(const source of ['`'+url+'`','~~~\n'+url+'\n~~~','[Guide]('+url+')','    '+url]) assert.deepEqual(rawLinkParts(source),[source]);
assert.equal(shortLinkLabel('https://short.test','https://short.test'),'https://short.test');
assert.equal(shortLinkLabel('Descriptive label '+url,url),'Descriptive label '+url);
const credentials=url.replace('example.test','misleading.test@example.test');
assert.equal(shortLinkLabel(credentials,credentials),'misleading.test@example.test/…');
assert.equal(rawLinkParts(url+' and '+url).filter(p=>typeof p!=='string').length,2);
assert.equal(rawLinkParts('`'+url+'` then '+url).filter(p=>typeof p!=='string').length,1);
console.log('raw link parser vectors passed');
