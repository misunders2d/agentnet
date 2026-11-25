package ui

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// The browser engine blocks as the Go client does (blocksync.go).
func TestBrowserBlockSync(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	out, err := exec.Command(node, "testdata/blocksync_engine_check.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Log(string(out))
}

// Shared vectors: the browser parser accepts exactly what protocol.ParseBlockSync does.
func TestBrowserBlockSyncWireMatchesGo(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	owner := protocol.NewID()
	m := protocol.BlockMark{Person: protocol.NewID(), Blocked: true, Revision: 1790000000000, Writer: "aaaaaaaa-bbbbbbbb-cccccccc-dddddddd"}
	r := protocol.BlockSync{V: 1, Person: owner, Roster: strings.Repeat("a", 64), Blocks: []protocol.BlockMark{m}}
	var cases []map[string]any
	add := func(raw string) {
		_, err := protocol.ParseBlockSync([]byte(raw))
		cases = append(cases, map[string]any{"raw": raw, "valid": err == nil})
	}
	raw, _ := json.Marshal(r)
	add(string(raw))
	add(strings.Replace(string(raw), `"blocked":true`, `"blocked":false`, 1))
	for _, field := range []string{"person", "blocked", "revision", "writer"} {
		var fields map[string]any
		json.Unmarshal(raw, &fields)
		delete(fields["blocks"].([]any)[0].(map[string]any), field)
		b, _ := json.Marshal(fields)
		add(string(b))
	}
	for _, value := range []string{"0", "-1", "1.5", "9007199254740992", "null"} {
		add(strings.Replace(string(raw), `"revision":1790000000000`, `"revision":`+value, 1))
	}
	add(strings.Replace(string(raw), `"blocked":true`, `"blocked":"yes"`, 1))
	add(strings.Replace(string(raw), `"v":1`, `"v":1,"extra":true`, 1))
	add(strings.Replace(string(raw), `"v":1`, `"v":2`, 1))
	self := r
	self.Blocks = []protocol.BlockMark{{Person: owner, Blocked: true, Revision: 1, Writer: m.Writer}}
	b, _ := json.Marshal(self)
	add(string(b))
	r.Blocks = []protocol.BlockMark{m, m}
	b, _ = json.Marshal(r)
	add(string(b))
	r.Blocks = nil
	b, _ = json.Marshal(r)
	add(string(b))
	valid := 0
	for _, c := range cases {
		if c["valid"].(bool) {
			valid++
		}
	}
	if valid != 2 {
		t.Fatalf("Go accepted %d vectors, want the 2 well-formed ones", valid)
	}
	input, _ := json.Marshal(cases)
	cmd := exec.Command(node, "--input-type=module", "-e", `import * as w from './static/wire.mjs';let s='';for await(const b of process.stdin)s+=b;const cases=JSON.parse(s);for(const c of cases){let valid=true;try{w.parseBlockSync(c.raw);}catch{valid=false;}if(valid!==c.valid)throw Error('Go/browser mismatch: '+c.raw);}console.log(cases.length+' block parser parity vectors PASS');`)
	cmd.Stdin = bytes.NewReader(input)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Log(string(out))
}
