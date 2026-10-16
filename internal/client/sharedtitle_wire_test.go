package client

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
)

func TestSharedTopicWireParity(t *testing.T) {
	node, e := exec.LookPath("node")
	if e != nil {
		t.Skip("node unavailable")
	}
	module, e := filepath.Abs("../ui/static/wire.mjs")
	if e != nil {
		t.Fatal(e)
	}
	inputs := []string{"", "  a\t\u202eb\u2028c  ", "é🐱\u2066literal\u2069", "a\u00a0b\u0085c", "\ufeffBOM preserved", " title\n@agent ", string(make([]rune, 0))}
	long := ""
	for range 121 {
		long += "🐱"
	}
	inputs = append(inputs, long)
	events := []json.RawMessage{json.RawMessage(`{"action":"create"}`), json.RawMessage(`{"action":"done","seen":["aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"]}`), json.RawMessage(`{"action":"rename","title":"","rev":1}`), json.RawMessage(`{"action":"rename","title":"literal @agent 🐱","rev":9007199254740991}`)}
	malformed := []json.RawMessage{json.RawMessage(`{"action":"create","rev":0}`), json.RawMessage(`{"action":"done","title":null}`), json.RawMessage(`{"action":"rename","title":null,"rev":1}`), json.RawMessage(`{"action":"rename","title":"x","rev":0}`), json.RawMessage(`{"action":"rename","title":"x","rev":9007199254740992}`), json.RawMessage(`{"action":"rename","title":"x","rev":1,"moves":[{}]}`)}
	data, _ := json.Marshal(map[string]any{"module": "file://" + module, "inputs": inputs, "events": events, "malformed": malformed})
	script := `const d=JSON.parse(process.argv[1]),w=await import(d.module);let norm=d.inputs.map(w.normalizeSharedTopicTitle),canonical=d.events.map(e=>w.topicEventJSON(w.parseTopicEvent(e)));for(const e of d.malformed){let rejected=false;try{w.parseTopicEvent(e)}catch{rejected=true}if(!rejected)throw Error("accepted malformed "+JSON.stringify(e));}console.log(JSON.stringify({norm,canonical}));`
	out, e := exec.Command(node, "--input-type=module", "-e", script, string(data)).CombinedOutput()
	if e != nil {
		t.Fatalf("wire fixture: %v %s", e, out)
	}
	var got struct{ Norm, Canonical []string }
	if e = json.Unmarshal(out, &got); e != nil {
		t.Fatal(e)
	}
	for i, s := range inputs {
		if got.Norm[i] != envelope.NormalizeSharedTopicTitle(s) {
			t.Fatalf("normalization %q: wire %q Go %q", s, got.Norm[i], envelope.NormalizeSharedTopicTitle(s))
		}
	}
	for i, raw := range events {
		var e envelope.TopicEvent
		if err := json.Unmarshal(raw, &e); err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(e)
		if string(b) != got.Canonical[i] {
			t.Fatalf("canonical %s != %s", b, got.Canonical[i])
		}
	}
	for _, raw := range malformed {
		var e envelope.TopicEvent
		if err := json.Unmarshal(raw, &e); err != nil {
			continue
		}
		if envelope.CheckTopic(envelope.Inner{V: 2, Kind: envelope.KindMessage, Topic: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", TopicEvent: &e}) == nil {
			t.Fatalf("Go accepted %s", raw)
		}
	}
}
