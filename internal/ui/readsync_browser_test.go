package ui

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestBrowserReadSync(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	out, err := exec.Command(node, "testdata/readsync_engine_check.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Log(string(out))
}

func TestBrowserInvitationSync(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	out, err := exec.Command(node, "testdata/invitationsync_engine_check.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Log(string(out))
}

func TestBrowserTopicSync(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	out, err := exec.Command(node, "testdata/topicsync_engine_check.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Log(string(out))
}

func TestBrowserTopicSyncWireMatchesGo(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	base := protocol.TopicSync{V: 1, Person: protocol.NewID(), Roster: strings.Repeat("a", 64), Titles: []protocol.TopicTitle{{Scope: strings.Repeat("b", 64), Topic: protocol.NewID(), Title: "Привіт 🌍", Rev: 1, Writer: "aaaaaaaa-bbbbbbbb-cccccccc-dddddddd"}}}
	var cases []map[string]any
	add := func(raw string) {
		_, err := protocol.ParseTopicSync([]byte(raw))
		cases = append(cases, map[string]any{"raw": raw, "valid": err == nil})
	}
	for _, title := range []string{base.Titles[0].Title, "", strings.Repeat("🌍", 120), strings.Repeat("🌍", 121), "not\ncanonical", "not\u0085canonical", "\ufeffname"} {
		r := base
		r.Titles = append([]protocol.TopicTitle(nil), base.Titles...)
		r.Titles[0].Title = title
		add(marshal(t, r))
	}
	for _, rev := range []int64{0, 2, protocol.MaxTopicTitleRevision, protocol.MaxTopicTitleRevision + 1} {
		r := base
		r.Titles = append([]protocol.TopicTitle(nil), base.Titles...)
		r.Titles[0].Rev = rev
		add(marshal(t, r))
	}
	raw := marshal(t, base)
	for _, value := range []string{
		strings.Replace(raw, `"title":"Привіт 🌍",`, "", 1),
		strings.Replace(raw, `"title":"Привіт 🌍"`, `"title":null`, 1),
		strings.Replace(raw, `"rev":1`, `"rev":1.5`, 1),
		strings.Replace(raw, `"v":1`, `"v":1,"unexpected":true`, 1),
		strings.Replace(raw, base.Titles[0].Scope, "admin/laptop", 1),
		strings.Replace(raw, base.Titles[0].Scope, "wrong scope", 1),
	} {
		add(value)
	}
	base.Titles = append(base.Titles, base.Titles[0])
	add(marshal(t, base))
	base.Titles = base.Titles[:1]
	for len(base.Titles) < 64 {
		entry := base.Titles[0]
		entry.Topic = protocol.NewID()
		base.Titles = append(base.Titles, entry)
	}
	add(marshal(t, base))
	entry := base.Titles[0]
	entry.Topic = protocol.NewID()
	base.Titles = append(base.Titles, entry)
	add(marshal(t, base))
	add(raw + strings.Repeat(" ", 65537-len(raw)))
	cmd := exec.Command(node, "--input-type=module", "-e", `import * as wire from './static/wire.mjs';let input='';for await(const s of process.stdin)input+=s;const cases=JSON.parse(input);for(const [i,c] of cases.entries()){let valid=true;try{wire.parseTopicSync(c.raw)}catch{valid=false}if(valid!==c.valid)throw Error('topic sync Go/browser validation differs at '+i)}console.log('topic sync Go/browser wire checks',cases.length)`)
	data, _ := json.Marshal(cases)
	cmd.Stdin = strings.NewReader(string(data))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Log(string(out))
}
