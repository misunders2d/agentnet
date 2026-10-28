package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// Exercise real filesystem idle wakes, replacement of the native status slot,
// and native-persistence acknowledgement of the next prompt's complete input.
func TestPiAndOMPIdleActivityDoesNotAppendMessages(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture command is a shell script")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	if err := exec.Command(node, "-e", "process.exit(Number(process.versions.node.split('.')[0]) >= 23 ? 0 : 1)").Run(); err != nil {
		t.Skip("node too old to load TypeScript directly")
	}
	for _, harness := range []string{"pi", "omp"} {
		t.Run(harness, func(t *testing.T) {
			dir := t.TempDir()
			home := filepath.Join(dir, "home")
			if err := os.Mkdir(home, 0700); err != nil {
				t.Fatal(err)
			}
			stub := filepath.Join(dir, "agentnet")
			write := func(name string, data []byte, mode os.FileMode) {
				t.Helper()
				if err := os.WriteFile(name, data, mode); err != nil {
					t.Fatal(err)
				}
			}
			write(stub, []byte(`#!/bin/sh
in=$(cat)
printf '%s\n' "$in" >> "$STUB_DIR/calls"
case "$in" in *'"Ack"'*) exit 0 ;; esac
[ -f "$STUB_DIR/fail" ] && exit 1
cat "$STUB_DIR/reply"
`), 0700)
			bin, _ := json.Marshal(stub)
			h, _ := json.Marshal(home)
			kind, _ := json.Marshal(harness)
			ext := bytes.Replace(piExtension, []byte(`"__AGENTNET_BIN__"`), bin, 1)
			ext = bytes.Replace(ext, []byte(`"__AGENTNET_HOME__"`), h, 1)
			ext = bytes.Replace(ext, []byte(`"__AGENTNET_HARNESS__"`), kind, 1)
			write(filepath.Join(dir, "extension.ts"), ext, 0600)
			write(filepath.Join(dir, "check.mjs"), []byte(piIdleStatusHarness), 0600)
			cmd := exec.CommandContext(t.Context(), node, filepath.Join(dir, "check.mjs"), dir, home)
			cmd.Env = append(os.Environ(), "STUB_DIR="+dir, "AGENTNET_BACKGROUND=")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("native status fixture: %v\n%s", err, out)
			}
		})
	}
}

const piIdleStatusHarness = `
import assert from "node:assert/strict";
import fs from "node:fs";
const [dir,home] = process.argv.slice(2), handlers = {}, sent = [], toasts = [], statuses = new Map();
let statusCalls = 0, sid = "S1", branch = [];
const file = dir+"/session.jsonl", calls = dir+"/calls";
fs.writeFileSync(calls, "");
const count = word => fs.readFileSync(calls,"utf8").split("\n").filter(l=>l.includes(word)).length;
const sleep = ms => new Promise(resolve=>setTimeout(resolve,ms));
async function until(fn) { const end=Date.now()+5000; while(!fn()) {if(Date.now()>end)throw Error("idle wake did not settle");await sleep(10);} }
const response = ids => JSON.stringify({text:"AgentNet: current activity\n"+ids.map(id=>"- "+id+" message; state needs_human").join("\n"),ack:{pos:7,has_pos:true,release:""}});
fs.writeFileSync(dir+"/reply",response(["A","B","C"]));
const renderers = new Map();
const pi = {on:(name,fn)=>handlers[name]=fn,sendMessage:m=>sent.push(m),registerMessageRenderer:(kind,fn)=>renderers.set(kind,fn)};
(await import(dir+"/extension.ts")).default(pi);
const ui = {notify:text=>toasts.push(text),setStatus:(key,text)=>{statusCalls++;if(text===undefined)statuses.delete(key);else statuses.set(key,text);}};
const ctx = {isIdle:()=>true,ui,sessionManager:{getSessionId:()=>sid,getSessionFile:()=>file,getBranch:()=>branch}};
const oldCards = Array.from({length:6},(_,i)=>({id:'old-'+i,type:'custom_message',customType:'agentnet',content:'Old private metadata '+i,details:i?{token:'old-token-'+i}:{}}));
const receiver = {id:'exact',type:'custom_message',customType:'agentnet-receiver',content:'Exact selected reply'};
branch=[...oldCards,receiver];
const retained = JSON.stringify(branch);
await handlers.session_start({},ctx);
const render = renderers.get('agentnet');
assert.equal(renderers.size,1,'only passive AgentNet cards get a renderer');
assert.equal(renderers.has('agentnet-receiver'),false,'selected replies retain native presentation');
for(const message of oldCards){
 assert.deepEqual(render(message,{expanded:false},{}).render(80),['Earlier AgentNet activity (expand to view)']);
 assert.equal(render(message,{expanded:true},{}),undefined,'expanded history uses native full renderer');
 assert(render(message,{expanded:false},{}).render(12)[0].length<=12,'compact label respects terminal width');
}
assert.equal(JSON.stringify(branch),retained,'reopening never rewrites stored history');
assert.equal(sent.length,0,"session start must not append a transcript snapshot");
assert.equal(statuses.get("agentnet"),"AgentNet: activity");
assert.equal(count('"Ack"'),0);
let wake=0;
async function idle(text) {
 fs.writeFileSync(dir+"/reply",text);const before=statusCalls;
 fs.writeFileSync(home+"/agent.db-wal",String(++wake));
 await until(()=>statusCalls>before);
 assert.equal(sent.length,0,"changing activity must not append transcript snapshots");
 assert.equal(toasts.length,0,"changing activity must not add repeated toasts");
 assert(statuses.size<=1,"one native status slot only");
 assert.equal(count('"Ack"'),0,"passive status must not consume prompt input");
}
for(const ids of [["B","C"],["C"],["C","B"],["C","B","D"],["D"]])await idle(response(ids));
await idle("");assert.equal(statuses.size,0,"successful empty check clears activity");
await idle(response(["D"]));
fs.writeFileSync(dir+"/fail","");
// Waiting for the serialized prompt hook also waits for any prior idle check.
await handlers.before_agent_start({});
assert.equal(statuses.get("agentnet"),"AgentNet: activity","a nonzero exit must not clear activity");
fs.unlinkSync(dir+"/fail");
fs.writeFileSync(dir+"/reply",response(["C","D"]));
const prompt = await handlers.before_agent_start({});
assert(prompt.message.content.includes("- C ")&&prompt.message.content.includes("- D "),"prompt keeps the complete current list");
assert.equal(render(prompt.message,{expanded:false},{}),undefined,'fresh prompt keeps native full presentation');
assert.equal(count('"Ack"'),0,"returning prompt input is not native persistence");
branch=[{id:"prompt",type:"custom_message",customType:"agentnet",details:prompt.message.details}];
fs.writeFileSync(file,JSON.stringify({type:"session",id:sid})+"\n"+branch.map(e=>JSON.stringify(e)).join("\n")+"\n");
await handlers.message_end({},ctx);await until(()=>count('"Ack"')===1);
assert.equal(sent.length,0);assert.equal(toasts.length,0);
sid="S2";branch=[];await handlers.session_start({},ctx);
assert.deepEqual(render(prompt.message,{expanded:false},{}).render(80),['Earlier AgentNet activity (expand to view)'],'prior native-session notices become historical on switch');
assert.equal(statuses.size,1);assert.equal(count('"Ack"'),1);
await handlers.session_shutdown({});
assert.equal(statuses.size,0,"shutdown clears its native status slot");
assert.equal(sent.length,0);assert.equal(toasts.length,0);
console.log("native status replacement, shrink, reorder, new activity, empty/error, prompt persistence and lifecycle passed");
`
