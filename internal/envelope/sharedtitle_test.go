package envelope

import (
	"encoding/json"
	"github.com/misunders2d/agentnet/internal/protocol"
	"strings"
	"testing"
)

func TestSharedTopicRenameGrammar(t *testing.T) {
	valid := func() Inner {
		title := ""
		return Inner{V: Version2, Kind: KindMessage, Topic: strings.Repeat("a", 32), TopicEvent: &TopicEvent{Action: "rename", Title: &title, Rev: 1}}
	}
	if err := CheckTopic(valid()); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*Inner){
		"missing": func(n *Inner) { n.TopicEvent.Title = nil }, "zero": func(n *Inner) { n.TopicEvent.Rev = 0 }, "overflow": func(n *Inner) { n.TopicEvent.Rev = protocol.MaxTopicTitleRevision + 1 }, "body": func(n *Inner) { n.Body = "private" }, "main": func(n *Inner) { n.Topic = "" }, "bare PID": func(n *Inner) { n.PID = strings.Repeat("b", 32) },
		"agent": func(n *Inner) { n.Origin = "agent:codex" }, "identity": func(n *Inner) { n.AgentID = strings.Repeat("b", 32) }, "seen": func(n *Inner) { n.TopicEvent.Seen = []string{strings.Repeat("b", 32)} }, "target": func(n *Inner) { n.Target = &Target{} }, "status": func(n *Inner) { n.Status = "done" }, "file": func(n *Inner) { n.Attachments = []Attachment{{}} }, "control": func(n *Inner) { v := "a\u202eb"; n.TopicEvent.Title = &v }, "long": func(n *Inner) { v := strings.Repeat("🐱", 121); n.TopicEvent.Title = &v },
	} {
		t.Run(name, func(t *testing.T) {
			n := valid()
			change(&n)
			if CheckTopic(n) == nil {
				t.Fatal("accepted malformed rename")
			}
		})
	}
	for _, raw := range []string{`{"action":"create","rev":0}`, `{"action":"done","title":null}`, `{"action":"rename","title":null,"rev":1}`} {
		var e TopicEvent
		if err := json.Unmarshal([]byte(raw), &e); err != nil {
			t.Fatal(err)
		}
		n := valid()
		n.TopicEvent = &e
		if CheckTopic(n) == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	n := valid()
	b, _ := json.Marshal(n.TopicEvent)
	if string(b) != `{"action":"rename","title":"","rev":1}` {
		t.Fatal(string(b))
	}
	b, _ = json.Marshal(&TopicEvent{Action: "done", Seen: []string{strings.Repeat("b", 32)}})
	if string(b) != `{"action":"done","seen":["`+strings.Repeat("b", 32)+`"]}` {
		t.Fatal(string(b))
	}
	if NormalizeSharedTopicTitle("  a\t\u202eb\u2028c  ") != "ab c" {
		t.Fatal("normalization")
	}
}
