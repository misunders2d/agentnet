package main

import (
	"os/exec"
	"testing"
)

func TestAppBridgeCheckShimKeepsExactRoutes(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	if out, err := exec.CommandContext(t.Context(), node, "--input-type=module", "-e", appCheckShimCases).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

const appCheckShimCases = `
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import vm from 'node:vm';
const code=readFileSync('../../desktop/src-tauri/src/clipboard.js','utf8');
const origin='http://127.0.0.1:17443',calls=[],fetches=[];
const result={version:'v0.8.9',latest:'v0.8.10',state:'available'};
const fetchPage=async(...args)=>{fetches.push(args);return 'page response';};
const listeners=[];
const window={fetch:fetchPage,addEventListener:(type,fn,capture)=>listeners.push({type,fn,capture}),__TAURI_INTERNALS__:{invoke:async(command,args)=>{
 calls.push({command,...args});return command==='agentnet_clipboard_text'?'pasted':{status:200,body:JSON.stringify(result)};
}}};
vm.runInNewContext(code,{window,location:{href:origin+'/',origin},URL,Request,Response,performance});
assert.equal(calls.length,0,'mount never checks');
// Clipboard text only after the person's own (trusted) paste, once.
assert.deepEqual(listeners.map(l=>[l.type,l.capture]),[['paste',true]]);
assert.equal(await window.__agentnetNativeClipboardText(),null,'no paste, no read');
listeners[0].fn({isTrusted:false});
assert.equal(await window.__agentnetNativeClipboardText(),null,'a synthetic paste never reads');
listeners[0].fn({isTrusted:true});
assert.equal(await window.__agentnetNativeClipboardText(),'pasted');
assert.deepEqual(calls.splice(0),[{command:'agentnet_clipboard_text',key:'__AGENTNET_PASTE_KEY__'}]);
assert.equal(await window.__agentnetNativeClipboardText(),null,'one read per paste');
assert.equal(calls.length,0);
for(const input of ['/api/app/check',new Request(origin+'/api/app/check')]){
 const response=await window.fetch(input);
 assert.deepEqual(await response.json(),result);
 assert.equal(response.headers.get('Cache-Control'),'no-store');
 assert.deepEqual(calls.at(-1),{command:'agentnet_app_controls',action:'check',body:null});
}
assert.equal(calls.length,2);assert.equal(fetches.length,0);
for(const [input,init] of [
 ['/api/app/check',{method:'POST',body:'{}'}],
 ['/api/app/update',undefined],
 ['/api/app/check?origin=https://other.test',undefined],
 ['https://other.test/api/app/check',undefined],
 ['/api/app/arbitrary',undefined]
]) assert.equal(await window.fetch(input,init),'page response');
assert.equal(calls.length,2,'unmatched requests never invoke native controls');
assert.equal(fetches.length,5);
await window.fetch('/api/app/update',{method:'POST',body:'{}'});
assert.equal(calls.at(-1).action,'update','existing update route retained');
const browser={fetch:fetchPage};
vm.runInNewContext(code,{window:browser});
assert.equal(browser.fetch,fetchPage);
assert.equal(browser.__agentnetNativeAppJSON,undefined);
console.log('app check shim routes passed');
`
