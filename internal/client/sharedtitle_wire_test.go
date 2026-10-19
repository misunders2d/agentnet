package client

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
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

func TestResourceLinkWireParity(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	module, err := filepath.Abs("../ui/static/wire.mjs")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, url string
		valid     bool
	}{
		{"Notes", "https://example.com/a?x=1&y=2#part", true},
		{"🐱 <notes>", "HTTP://example.com:65535/%ff?q=%zz", true},
		{strings.Repeat("<", 10900), "https://example.com", true},
		{strings.Repeat("<", 11000), "https://example.com", false},
		{"", "https://example.com", false},
		{" Notes", "https://example.com", false},
		{"Notes\n", "https://example.com", false},
		{"Notes", "https://user:secret@example.com", false},
		{"Notes", "https://@example.com", false},
		{"Notes", "file:///tmp/notes", false},
		{"Notes", "https://example.com:65536", false},
		{"Notes", "https://[fe80::1%25eth0]/", false},
		{"Notes", "https://example.com/%zz", false},
		{"Notes", "https://example.com/#%zz", false},
	}
	lists := make([][]protocol.ResourceLink, len(cases))
	for i, c := range cases {
		lists[i] = []protocol.ResourceLink{{Name: c.name, URL: c.url}}
		if err := protocol.ValidateResourceLinks(lists[i]); (err == nil) != c.valid {
			t.Fatalf("Go resource case %d valid=%v: %v", i, c.valid, err)
		}
	}
	input, _ := json.Marshal(map[string]any{"module": "file://" + module, "lists": lists})
	cmd := exec.Command(node, "--input-type=module", "-e", `const d=JSON.parse((await import('node:fs')).readFileSync(0,'utf8')),w=await import(d.module);console.log(JSON.stringify(d.lists.map(links=>{try{return w.resourceLinksJSON(w.parseResourceLinks(links));}catch{return null;}})));`)
	cmd.Stdin = bytes.NewReader(input)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("browser resource parser: %v %s", err, out)
	}
	var parsed []*string
	if err = json.Unmarshal(out, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed) != len(cases) {
		t.Fatal("browser parser did not classify all inputs")
	}
	for i, c := range cases {
		if (parsed[i] != nil) != c.valid {
			t.Fatalf("browser resource case %d valid=%v", i, c.valid)
		}
		if c.valid {
			canonical, _ := json.Marshal(lists[i])
			if *parsed[i] != string(canonical) {
				t.Fatalf("resource case %d has incompatible signed JSON", i)
			}
		}
	}
}
