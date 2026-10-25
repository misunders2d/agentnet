package ui

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestBrowserTopicTransfer(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	if out, err := exec.CommandContext(t.Context(), node, "testdata/topictransfer_engine_check.mjs").CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	} else {
		t.Log(string(out))
	}
}

func TestBrowserTopicTransferRealIndexedDB(t *testing.T) {
	testOwnDeviceModuleIndexedDB(t, "topictransfer_count_check.mjs", "topicTransferResult")
}

func TestBrowserTopicLocationSync(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	if out, err := exec.CommandContext(t.Context(), node, "testdata/topiclocationsync_engine_check.mjs").CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	} else {
		t.Log(string(out))
	}
}

func TestBrowserTopicLocationSyncRealIndexedDB(t *testing.T) {
	testOwnDeviceModuleIndexedDB(t, "topiclocationsync_engine_check.mjs", "topicLocationSyncResult")
}

func TestBrowserTopicTransferDirect(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	if out, err := exec.CommandContext(t.Context(), node, "testdata/topictransfer_direct_check.mjs").CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	} else {
		t.Log(string(out))
	}
}

func TestBrowserTopicTransferCapacity(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	if out, err := exec.CommandContext(t.Context(), node, "testdata/topictransfer_capacity_check.mjs").CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	} else {
		t.Log(string(out))
	}
}

func TestBrowserTopicTransferUpgradeRealIndexedDB(t *testing.T) {
	testOwnDeviceModuleIndexedDB(t, "topictransfer_upgrade_check.mjs", "topicTransferUpgradeResult")
}

func TestBrowserTopicLocationSyncWireMatchesGo(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	x := protocol.TopicLocation{Scope: strings.Repeat("a", 64), Topic: protocol.NewID(), Destination: strings.Repeat("b", 64), DestinationTopic: "", Operation: protocol.NewID(), At: 1700000000, Writer: "aaaaaaaa-bbbbbbbb-cccccccc-dddddddd", Revision: 1}
	base := protocol.TopicLocationSync{V: 2, Person: protocol.NewID(), Roster: strings.Repeat("c", 64), Locations: []protocol.TopicLocation{x}}
	var cases []map[string]any
	add := func(raw string) {
		_, err := protocol.ParseTopicLocationSync([]byte(raw))
		cases = append(cases, map[string]any{"raw": raw, "valid": err == nil})
	}
	for _, change := range []func(*protocol.TopicLocation){
		func(p *protocol.TopicLocation) {},
		func(p *protocol.TopicLocation) { p.Scope = "alice/desk" },
		func(p *protocol.TopicLocation) { p.DestinationTopic = protocol.NewID() },
		func(p *protocol.TopicLocation) {
			p.At, p.Revision = protocol.MaxTopicTitleRevision, protocol.MaxTopicTitleRevision
		},
		func(p *protocol.TopicLocation) { p.Revision = 0 },
		func(p *protocol.TopicLocation) { p.Revision = protocol.MaxTopicTitleRevision + 1 },
		func(p *protocol.TopicLocation) { p.At = 0 },
		func(p *protocol.TopicLocation) { p.At = protocol.MaxTopicTitleRevision + 1 },
		func(p *protocol.TopicLocation) { p.Scope = "unknown scope" },
		func(p *protocol.TopicLocation) { p.Destination = "not a conversation" },
	} {
		r := base
		r.Locations = []protocol.TopicLocation{x}
		change(&r.Locations[0])
		add(marshal(t, r))
	}
	raw := marshal(t, base)
	for _, value := range []string{
		strings.Replace(raw, `"destination_topic":"",`, "", 1),
		strings.Replace(raw, `"destination_topic":""`, `"destination_topic":null`, 1),
		strings.Replace(raw, `"v":2`, `"v":1`, 1),
		strings.Replace(raw, `"v":2`, `"v":2,"extra":true`, 1),
		strings.Replace(raw, `"writer"`, `"task":true,"writer"`, 1),
		raw + strings.Repeat(" ", 65537-len(raw)),
	} {
		add(value)
	}
	dup := base
	dup.Locations = []protocol.TopicLocation{x, x}
	add(marshal(t, dup))
	var orders []map[string]any
	for _, change := range []func(*protocol.TopicLocation){
		func(p *protocol.TopicLocation) { p.Revision++ },
		func(p *protocol.TopicLocation) { p.At++ },
		func(p *protocol.TopicLocation) { p.Writer = "ffffffff-ffffffff-ffffffff-ffffffff" },
		func(p *protocol.TopicLocation) { p.Operation = strings.Repeat("f", 32) },
		func(p *protocol.TopicLocation) { p.Destination = strings.Repeat("f", 64) },
		func(p *protocol.TopicLocation) { p.DestinationTopic = strings.Repeat("f", 32) },
	} {
		y := x
		change(&y)
		for _, pair := range [][2]protocol.TopicLocation{{x, y}, {y, x}} {
			orders = append(orders, map[string]any{"a": pair[0], "b": pair[1], "newer": pair[0].Newer(pair[1])})
		}
	}
	cmd := exec.CommandContext(t.Context(), node, "--input-type=module", "-e", `import * as w from './static/wire.mjs';let input='';for await(const part of process.stdin)input+=part;const {cases,orders}=JSON.parse(input);for(const [i,c]of cases.entries()){let valid=true;try{w.parseTopicLocationSync(c.raw);}catch{valid=false;}if(valid!==c.valid)throw Error('Go/browser location parser differs at '+i);}for(const [i,o]of orders.entries())if(w.topicLocationNewer(o.a,o.b)!==o.newer)throw Error('Go/browser location order differs at '+i);console.log('topic location parser vectors',cases.length,'orders',orders.length);`)
	data, _ := json.Marshal(map[string]any{"cases": cases, "orders": orders})
	cmd.Stdin = bytes.NewReader(data)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	} else {
		t.Log(string(out))
	}
}

func TestBrowserTopicTransferFilesRealIndexedDB(t *testing.T) {
	testOwnDeviceModuleIndexedDB(t, "topictransfer_files_check.mjs", "topicTransferResult")
}

func TestBrowserTopicTransferDirectRealIndexedDB(t *testing.T) {
	testOwnDeviceModuleIndexedDB(t, "topictransfer_direct_check.mjs", "topicTransferDirectResult")
}

func TestBrowserTopicTransferAudience(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	if out, err := exec.CommandContext(t.Context(), node, "testdata/topictransfer_audience_check.mjs").CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	} else {
		t.Log(string(out))
	}
}
