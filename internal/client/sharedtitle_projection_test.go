package client

import (
	"strings"
	"testing"
)

func TestSharedTitleProjectionPresenceAndPublicRename(t *testing.T) {
	w, conv, _ := dmFiles(t)
	a := w.alice
	ctx := tctx(t)
	sent, e := a.SendConv(ctx, conv, ConvOutgoing{Body: "Automatic original", Topic: "new"})
	if e != nil {
		t.Fatal(e)
	}
	topic := convMsgByID(t, a, conv, sent.ID).Topic
	if e = a.setTopicTitle(conv, topic, "private first"); e != nil {
		t.Fatal(e)
	}
	if _, ok, e := a.SharedTopicTitle(conv, topic); e != nil || ok {
		t.Fatal("private label seeded shared state")
	}
	ts, e := a.ChatTopics(conv)
	if e != nil || len(ts) != 1 || ts[0].Title != "private first" {
		t.Fatalf("private projection %v %v", ts, e)
	}
	if ok, e := a.ChangeChatTopic(ctx, conv, topic, "rename", "  Shared\u202e @agent  ", 0); e != nil || !ok {
		t.Fatal(ok, e)
	}
	for _, agent := range []*Agent{a, w.bob} {
		eventually(t, "shared title projection", func() bool {
			ts, e := agent.ChatTopics(conv)
			return e == nil && len(ts) == 1 && ts[0].Title == "Shared @agent" && ts[0].Renamed && ts[0].AutoTitle == "Automatic original" && ts[0].Count == 1
		})
	}
	found, e := a.Topics(TopicQuery{Conv: conv, Query: "shared @agent"})
	if e != nil || found.Matched != 1 {
		t.Fatalf("shared title search %v %v", found, e)
	}
	if ok, e := a.ChangeChatTopic(ctx, conv, topic, "rename", "", 0); e != nil || !ok {
		t.Fatal(ok, e)
	}
	if e = a.setTopicTitle(conv, topic, "private later"); e != nil {
		t.Fatal(e)
	}
	ts, e = a.ChatTopics(conv)
	if e != nil || len(ts) != 1 || ts[0].Title != "Automatic original" || ts[0].Renamed || ts[0].AutoTitle != "" {
		t.Fatalf("reset presence lost %v %v", ts, e)
	}
	page, e := a.Topics(TopicQuery{Conv: conv, Query: "private later"})
	if e != nil || page.Matched != 0 {
		t.Fatalf("private name leaked into search %v %v", page, e)
	}
	main, e := a.SendConv(ctx, conv, ConvOutgoing{Body: "private main"})
	if e != nil {
		t.Fatal(e)
	}
	_ = main
	if ok, e := a.ChangeChatMainTopic(ctx, conv, "rename", "Main private", 0); e != nil || !ok {
		t.Fatal(ok, e)
	}
	if _, ok, e := a.SharedTopicTitle(conv, strings.Repeat("0", 32)); e != nil || ok {
		t.Fatal("Main seeded a shared topic")
	}
}
