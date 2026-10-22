package ui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/testhub"
)

func TestBrowserNativeTopicDestinationTitle(t *testing.T) {
	t.Setenv("AGENTNET_NOTIFY", "off")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	dir := filepath.Join(t.TempDir(), "hub")
	hub := testhub.Start(t, dir, "127.0.0.1:0", "")
	a, err := client.Join(ctx, filepath.Join(t.TempDir(), "alice"), testhub.BootstrapCode(t, dir), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	runDaemon(t, a)
	if _, err = a.CreatePerson(ctx, "Alice"); err != nil {
		t.Fatal(err)
	}
	w := startEngineNode(t, dir)
	w.ok(map[string]any{"op": "init", "base": "https://" + hub.Addr})
	code, err := a.Invite(ctx, "bob", time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	w.ok(map[string]any{"op": "join", "code": browserCode(t, code), "name": "phone"})
	w.api("/api/person", map[string]any{"label": "Bob"})
	w.ok(map[string]any{"op": "start"})
	conv := w.api("/api/dm/new", map[string]any{"address": a.Address})["id"].(string)
	wait := func(what string, pred func() bool) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			if pred() {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal(what)
	}
	for _, browser := range []bool{true, false} {
		from, to := protocol.NewID(), protocol.NewID()
		// At equal signed seconds these IDs still put the source first.
		sourceID, destinationID := strings.Repeat("1", 31)+"1", strings.Repeat("f", 31)+"1"
		if !browser {
			sourceID, destinationID = strings.Repeat("1", 31)+"2", strings.Repeat("f", 31)+"2"
		}
		send := func(id, body, topic string) {
			t.Helper()
			if browser {
				w.api("/api/dm/send", map[string]any{"conv": conv, "id": id, "body": body, "topic": topic})
			} else if _, err := a.SendConv(client.WithQueuedSend(ctx, id), conv, client.ConvOutgoing{Body: body, Topic: topic}); err != nil {
				t.Fatal(err)
			}
		}
		send(sourceID, "Group note to edit", from)
		send(destinationID, "Stock count topic start", to)
		checkTitle := func(title string, count int) bool {
			ts, err := a.ChatTopics(conv)
			if err != nil {
				t.Fatal(err)
			}
			native := false
			for _, topic := range ts {
				if topic.ID == to {
					native = topic.Title == title && topic.Count == count
				}
			}
			page := w.api("/api/topics?conv="+conv, nil)
			for _, raw := range page["topics"].([]any) {
				topic := raw.(map[string]any)
				if topic["id"] == to {
					return native && topic["title"] == title && topic["count"] == float64(count)
				}
			}
			return false
		}
		wait("both engines hold the original destination", func() bool { return checkTitle("Stock count topic start", 1) })
		if browser {
			// Review/apply is deliberately offline so unrelated receipt writes
			// cannot invalidate the exact reviewed browser storage snapshot.
			w.ok(map[string]any{"op": "stopStream"})
			w.api("/api/topic/rename", map[string]any{"conv": conv, "id": from, "title": "Freight quote"})
			review := w.api("/api/topic/organization/preview", map[string]any{"conv": conv, "ids": []string{sourceID}, "topic": to, "merge": from})
			moved := w.api("/api/topic/organization/apply", review)
			if again := w.api("/api/topic/organization/apply", review); again["lid"] != moved["lid"] {
				t.Fatal("exact browser replay changed operation")
			}
			w.ok(map[string]any{"op": "start"})
		} else {
			review, err := a.PreviewTopicOrganization(client.TopicOrganizationRequest{Conv: conv, IDs: []string{sourceID}, Topic: to})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = a.ApplyTopicOrganization(ctx, review); err != nil {
				t.Fatal(err)
			}
		}
		wait("native/browser destination retains its exact automatic title", func() bool { return checkTitle("Stock count topic start", 2) })
		w.ok(map[string]any{"op": "reload", "base": "https://" + hub.Addr})
		w.until("browser reconnects after reload", func() bool { return w.ok(map[string]any{"op": "status"})["connected"] == true })
		wait("browser reload retains destination title", func() bool { return checkTitle("Stock count topic start", 2) })
		if !browser {
			back, err := a.PreviewTopicOrganization(client.TopicOrganizationRequest{Conv: conv, IDs: []string{sourceID}, Topic: from})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = a.ApplyTopicOrganization(ctx, back); err != nil {
				t.Fatal(err)
			}
			wait("inverse move restores placement without title drift", func() bool { return checkTitle("Stock count topic start", 1) })
		}
		w.api("/api/topic/rename", map[string]any{"conv": conv, "id": to, "title": "Named destination"})
		count := 2
		if !browser {
			count = 1
		}
		wait("explicit shared destination name still wins", func() bool { return checkTitle("Named destination", count) })
		msgs, err := a.ConversationMessages(conv)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, m := range msgs {
			if m.LID == sourceID {
				found = m.Topic == from && m.Body == "Group note to edit" && m.Key != ""
			}
		}
		if !found {
			t.Fatal("display organization changed the signed original")
		}
	}
}

// Real signed/E2E turns between the browser engine and native installation,
// including persisted shared marks, pagination, bulk operations and local erase.
func TestBrowserChatTopicJourney(t *testing.T) {
	t.Setenv("AGENTNET_NOTIFY", "off")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	dir := filepath.Join(t.TempDir(), "hub")
	hub := testhub.Start(t, dir, "127.0.0.1:0", "")
	base := "https://" + hub.Addr
	alice, err := client.Join(ctx, filepath.Join(t.TempDir(), "alice"), testhub.BootstrapCode(t, dir), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { alice.Close() })
	runDaemon(t, alice)
	if _, err = alice.CreatePerson(ctx, "Alice"); err != nil {
		t.Fatal(err)
	}
	w := startEngineNode(t, dir)
	w.ok(map[string]any{"op": "init", "base": base})
	code, err := alice.Invite(ctx, "dana", time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	w.ok(map[string]any{"op": "join", "code": browserCode(t, code), "name": "phone"})
	w.api("/api/person", map[string]any{"label": "Dana"})
	w.ok(map[string]any{"op": "start"})
	conv := w.api("/api/dm/new", map[string]any{"address": alice.Address})["id"].(string)
	send := func(body, topic string) map[string]any {
		id := protocol.NewID()
		r := w.api("/api/dm/send", map[string]any{"id": id, "conv": conv, "body": body, "topic": topic})
		if r["lid"] != id {
			t.Fatalf("queued browser topic correlation: %v want %s", r, id)
		}
		return r
	}
	send("Main flow", "")
	seed := send("Promoted main message", "")
	w.api("/api/topic/create", map[string]any{"conv": conv, "id": seed["lid"]})
	topicID := protocol.NewID() // the UI captures this before clearing the new-topic draft
	send("Optional topic", topicID)
	native := func() []client.ThreadSummary {
		ts, e := alice.ChatTopics(conv)
		if e != nil {
			t.Fatal(e)
		}
		return ts
	}
	wait := func(what string, pred func() bool) {
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			if pred() {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal(what)
	}
	wait("native holds two topics", func() bool { return len(native()) == 2 })
	found := false
	for _, topic := range native() {
		if topic.ID == topicID {
			found = true
		}
	}
	if !found {
		t.Fatal("native lost the browser's captured topic reference")
	}
	w.api("/api/topic/rename", map[string]any{"conv": conv, "id": topicID, "title": "Shared HTTP title"})
	wait("browser public rename reaches native header/list", func() bool {
		for _, topic := range native() {
			if topic.ID == topicID {
				return topic.Title == "Shared HTTP title" && topic.Renamed && topic.AutoTitle == "Optional topic"
			}
		}
		return false
	})
	sharedPage, e := alice.Topics(client.TopicQuery{Conv: conv, Query: "Shared HTTP title"})
	if e != nil || sharedPage.Matched != 1 {
		t.Fatalf("native shared title search: %+v %v", sharedPage, e)
	}
	if _, e := NewLive(alice).ChangeTopic("rename", TopicChange{Conv: conv, ID: topicID, Title: "Native shared title"}); e != nil {
		t.Fatal(e)
	}
	wait("native public rename reaches browser header", func() bool {
		page := w.api("/api/topics?conv="+conv+"&q=Native%20shared%20title", nil)
		return page["matched"] == float64(1)
	})
	first := w.api("/api/topics?conv="+conv+"&limit=1", nil)
	if first["matched"] != float64(2) || first["next"] == nil {
		t.Fatalf("topic page: %v", first)
	}
	second := w.api("/api/topics?conv="+conv+"&limit=1&before="+first["next"].(string), nil)
	if second["next"] != nil || len(second["topics"].([]any)) != 1 {
		t.Fatalf("next page: %v", second)
	}
	ids := []string{}
	for _, x := range native() {
		ids = append(ids, x.ID)
	}

	send("Arrived while bulk action waited", ids[0])
	wait("new message held before bulk Done", func() bool {
		for _, x := range native() {
			if x.ID == ids[0] {
				return x.Count == 2
			}
		}
		return false
	})
	stale := w.api("/api/topic/done", map[string]any{"conv": conv, "id": "", "ids": []string{ids[0]}, "counts": map[string]int{ids[0]: 1}})
	if !strings.Contains(stale["note"].(string), "kept active") {
		t.Fatalf("stale bulk counts: %v", stale)
	}
	if _, e := NewLive(alice).ChangeTopic("done", TopicChange{Conv: conv, IDs: []string{ids[0]}, Counts: map[string]int{ids[0]: 1}}); e != nil {
		t.Fatal(e)
	}
	for _, x := range native() {
		if x.State != client.TopicActive {
			t.Fatal("stale bulk closed unseen messages")
		}
	}
	w.api("/api/topic/done", map[string]any{"conv": conv, "id": "", "ids": ids})
	wait("shared bulk done", func() bool {
		ts := native()
		return len(ts) == 2 && ts[0].State == client.TopicDone && ts[1].State == client.TopicDone
	})
	live := NewLive(alice)
	if _, e := live.ChangeTopic("archive", TopicChange{Conv: conv, IDs: ids}); e != nil {
		t.Fatal(e)
	}
	for _, x := range native() {
		if x.State != client.TopicArchived {
			t.Fatalf("local archive: %v", native())
		}
	}
	w.api("/api/topic/reopen", map[string]any{"conv": conv, "id": ids[0]})
	wait("shared reopen clears local archive", func() bool {
		for _, x := range native() {
			if x.ID == ids[0] {
				return x.State == client.TopicActive
			}
		}
		return false
	})
	follow := send("New topic message reopens", ids[1])
	if follow["lid"] == nil {
		t.Fatal("logical id missing")
	}
	wait("new message invalidates closure", func() bool {
		for _, x := range native() {
			if x.ID == ids[1] {
				return x.Count == 2 && x.State == client.TopicActive
			}
		}
		return false
	})
	w.ok(map[string]any{"op": "reload", "base": base})
	reloaded := w.api("/api/topics?conv="+conv, nil)
	if reloaded["matched"] != float64(2) {
		t.Fatalf("reload: %v", reloaded)
	}
	w.api("/api/topic/delete", map[string]any{"conv": conv, "id": "", "ids": ids})
	if got := w.api("/api/topics?conv="+conv, nil); got["matched"] != float64(0) {
		t.Fatalf("local delete: %v", got)
	}
	if len(native()) != 2 {
		t.Fatal("delete removed another person's copy")
	}
	dm := w.api("/api/dm?id="+conv, nil)
	if got := dmBodies(dm); got != "out:Main flow" {
		t.Fatalf("main flow after topic erase: %s", got)
	}
}

func TestChatTopicsAllSkinsRendered(t *testing.T) {
	privateScreenshots(t)
	out := browserCheck(t, "testdata/chattopics_rendered.cjs")
	t.Log(out)
}

func TestTopicPendingRequestsComicRendered(t *testing.T) {
	privateScreenshots(t)
	t.Setenv("P11_SKINS", "comic")
	t.Setenv("P11_PENDING_ONLY", "1")
	t.Setenv("AGENTNET_SCREENSHOTS", t.TempDir())
	t.Log(browserCheck(t, "testdata/chattopics_rendered.cjs"))
}

// A typed topic name is literal, while automatic names remain message previews.
func TestRenamedTopicTitlesComicRendered(t *testing.T) {
	privateScreenshots(t)
	t.Setenv("P11_SKINS", "comic")
	t.Setenv("P28_RENAMED_ONLY", "1")
	t.Log(browserCheck(t, "testdata/chattopics_rendered.cjs"))
}
