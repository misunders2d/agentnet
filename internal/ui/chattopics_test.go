package ui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/testhub"
)

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
		return w.api("/api/dm/send", map[string]any{"conv": conv, "body": body, "topic": topic})
	}
	send("Main flow", "")
	seed := send("Promoted main message", "")
	w.api("/api/topic/create", map[string]any{"conv": conv, "id": seed["lid"]})
	send("Optional topic", "new")
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
	out := browserCheck(t, "testdata/chattopics_rendered.cjs")
	t.Log(out)
}
