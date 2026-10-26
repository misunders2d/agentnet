package ui

import (
	"context"
	"encoding/base64"
	"fmt"
	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/envelope"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestBrowserUnreadProvider(t *testing.T) { runNodeCheck(t, "testdata/unread_provider_check.mjs") }

func TestLiveUnreadProvider(t *testing.T) {
	alice, bob, _, conv, eventually, stops := liveGuestWorld(t)
	group := liveTwoMemberGroup(t, alice, bob)
	for _, stop := range stops {
		stop()
	}
	live := NewLive(alice)
	a, b := protocol.NewID(), protocol.NewID()
	ids := map[string][]string{}
	for _, topic := range []string{"", "", a, b, b, b} {
		sent, err := live.SendDM(DMDraft{Conv: conv, Topic: topic, Body: "Review native message"})
		if err != nil {
			t.Fatal(err)
		}
		ids[topic] = append(ids[topic], sent.ID)
	}
	for _, set := range ids {
		if err := alice.MarkUnread(set[0], set); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := live.ChangeTopic(TopicMark, TopicChange{Conv: conv, ID: b, Count: 3}); err != nil {
		t.Fatal(err)
	}
	if _, err := live.ChangeTopic("archive", TopicChange{Conv: conv, ID: b, Count: 3}); err != nil {
		t.Fatal(err)
	}
	var muted []string
	for i := 0; i < 4; i++ {
		sent, err := live.SendDM(DMDraft{Conv: group, Body: "Muted deliberate sent"})
		if err != nil {
			t.Fatal(err)
		}
		muted = append(muted, sent.ID)
	}
	if err := alice.MarkUnread(muted[0], muted); err != nil {
		t.Fatal(err)
	}
	prefs, err := alice.AlertPrefs()
	if err != nil {
		t.Fatal(err)
	}
	prefs.Enabled = false
	prefs.Mutes = []string{group}
	if err := alice.SetAlertPrefs(prefs); err != nil {
		t.Fatal(err)
	}
	check := func(want, alert int) {
		t.Helper()
		for _, archived := range []bool{true, false} {
			o, err := live.overview(archived)
			if err != nil {
				t.Fatal(err)
			}
			if o.UnreadTotal == nil || o.AlertTotal == nil || *o.UnreadTotal != want || *o.AlertTotal != alert {
				t.Fatalf("totals archived=%v: unread=%d alert=%d want %d/%d", archived, *o.UnreadTotal, *o.AlertTotal, want, alert)
			}
		}
	}
	if _, err := live.ChangeTopic("rename", TopicChange{Conv: conv, ID: b, Title: "Shared unread @literal"}); err != nil {
		t.Fatal(err)
	}
	check(10, 6)
	labels, err := live.UnreadPage(UnreadQuery{Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range labels.Rows {
		if row.Conv == conv && row.Topic == b && row.TopicTitle != "Shared unread @literal" {
			t.Fatalf("never-opened archived topic label: %+v", row)
		}
		if row.Topic == "" && row.TopicTitle != "" {
			t.Fatalf("Main flow label: %+v", row)
		}
	}
	var index *unreadIndex
	eventually("same-revision shared unread index", func() bool {
		before, _ := alice.ViewChanges()
		if _, err := live.UnreadPage(UnreadQuery{Limit: 200}); err != nil {
			t.Fatal(err)
		}
		candidate := live.unread
		p, err := live.UnreadPage(UnreadQuery{Limit: 2})
		if err != nil {
			t.Fatal(err)
		}
		after, _ := alice.ViewChanges()
		if before.Content != after.Content {
			t.Logf("content changed during cache check: %d -> %d", before.Content, after.Content)
			return false
		}
		if p.Total != 10 || len(p.Rows) != 2 || p.Next == "" {
			t.Fatalf("page: %+v", p)
		}
		if live.unread != candidate {
			t.Fatalf("unchanged page rebuilt shared compact index at content %d", before.Content)
		}
		index = candidate
		return true
	})
	prefs.Mutes = nil
	if err := alice.SetAlertPrefs(prefs); err != nil {
		t.Fatal(err)
	}
	check(10, 10)
	if live.unread == index {
		t.Fatal("local mute did not invalidate")
	}
	var extra []string
	for i := 0; i < 201; i++ {
		sent, err := live.SendDM(DMDraft{Conv: conv, Body: fmt.Sprintf("Additional %d", i)})
		if err != nil {
			t.Fatal(err)
		}
		extra = append(extra, sent.ID)
	}
	if err := alice.MarkUnread(extra[0], extra); err != nil {
		t.Fatal(err)
	}
	check(211, 211)
	first, err := live.UnreadPage(UnreadQuery{Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Rows) != 200 || first.Total != 211 {
		t.Fatalf("large first page: %d/%d", len(first.Rows), first.Total)
	}
	tail, err := live.UnreadPage(UnreadQuery{Limit: 200, After: first.Next})
	if err != nil {
		t.Fatal(err)
	}
	if len(tail.Rows) != 11 {
		t.Fatalf("tail=%d", len(tail.Rows))
	}
	attention := map[string]protocol.AttentionGeneration{}
	for _, scope := range []string{conv, group} {
		view, err := live.DM(scope)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range view.Messages {
			if m.Attention != nil {
				attention[m.ID] = *m.Attention
			}
		}
	}
	var selected []string
	selectedAttention := map[string]protocol.AttentionGeneration{}
	for _, r := range first.Rows {
		selected = append(selected, r.ID)
		selectedAttention[r.ID] = attention[r.ID]
		if r.Author == "" || r.LID == "" || r.Key == "" || r.Dir != "out" {
			t.Fatalf("incomplete sent identity: %+v", r)
		}
	}
	if err := alice.MarkReadWithAttention(selected, selectedAttention); err != nil {
		t.Fatal(err)
	}
	remaining, err := live.UnreadPage(UnreadQuery{Limit: 200, After: first.Next})
	if err != nil {
		t.Fatal(err)
	}
	if remaining.Total != 11 || len(remaining.Rows) != len(tail.Rows) {
		t.Fatalf("read first page lost remainder: %+v", remaining)
	}
	for i, r := range remaining.Rows {
		if r.Key != tail.Rows[i].Key {
			t.Fatalf("remaining key changed %s != %s", r.Key, tail.Rows[i].Key)
		}
	}
	for _, q := range []UnreadQuery{{Limit: -1}, {Limit: 201}, {After: "bad"}, {After: base64.RawURLEncoding.EncodeToString([]byte(`{"v":1,"key":"row"}`))}} {
		if _, err := live.UnreadPage(q); err == nil {
			t.Fatalf("accepted %+v", q)
		}
	}
	// Device refs are collected in the same pass and only their requested page hydrates text.
	direct, err := alice.SendMessage(context.Background(), client.Outgoing{To: bob.Address, Kind: envelope.KindMessage, Body: "[@Device](agentnet:person/team) body"})
	if err != nil {
		t.Fatal(err)
	}
	if err := alice.MarkUnread(direct.ID, []string{direct.ID}); err != nil {
		t.Fatal(err)
	}
	if err := alice.ArchiveTopic(bob.Address, direct.ID); err != nil {
		t.Fatal(err)
	}
	if err := alice.RenameTopic(bob.Address, direct.ID, "Private device title"); err != nil {
		t.Fatal(err)
	}
	check(12, 12)
	devicePage, err := live.UnreadPage(UnreadQuery{Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range devicePage.Rows {
		if r.ID == direct.ID && r.Kind == "device" {
			found = true
			if r.Preview != "@Device body" || r.Thread != direct.ID || r.Author != alice.Self().Fingerprint() || r.TopicTitle != "Private device title" {
				t.Fatalf("device preview/identity %+v", r)
			}
		}
	}
	if !found {
		t.Fatal("archived device unread absent")
	}
	before, _ := alice.ViewChanges()
	if _, err := live.UnreadPage(UnreadQuery{}); err != nil {
		t.Fatal(err)
	}
	after, _ := alice.ViewChanges()
	if before.Content != after.Content {
		t.Fatal("projection changed read state")
	}

	// Exercise the actual optional authenticated HTTP route, including parse errors.
	var server *Server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { server.Handler().ServeHTTP(w, r) }))
	t.Cleanup(ts.Close)
	server = New(live, strings.TrimPrefix(ts.URL, "http://"), testToken)
	for path, want := range map[string]int{"/api/unread?limit=2": 200, "/api/unread?limit=abc": 409, "/api/unread?limit=%2B1": 409, "/api/unread?limit=201": 409, "/api/unread?after=bad": 409} {
		r := do(t, ts, "GET", path, "", authed(ts, nil))
		r.Body.Close()
		if r.StatusCode != want {
			t.Fatalf("%s HTTP %d want%d", path, r.StatusCode, want)
		}
	}
}

func TestLiveUnreadProviderGuest(t *testing.T) {
	alice, _, guest, conv, eventually, _ := liveGuestWorld(t)
	inviteGuest(t, alice, guest, conv, eventually)
	if _, err := NewLive(alice).SendDM(DMDraft{Conv: conv, Body: "Only current invited scope"}); err != nil {
		t.Fatal(err)
	}
	live := NewLive(guest)
	eventually("guest presented unread", func() bool {
		d, err := live.DM(conv)
		if err != nil {
			return false
		}
		for _, m := range d.Messages {
			if m.Body == "Only current invited scope" && m.Unread {
				return true
			}
		}
		return false
	})
	dm, err := live.DM(conv)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{}
	for _, m := range dm.Messages {
		if m.Unread {
			want[m.ID] = true
		}
	}
	p, err := live.UnreadPage(UnreadQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Total != len(want) {
		t.Fatalf("guest page %d != shown %d", p.Total, len(want))
	}
	for _, r := range p.Rows {
		if r.Conv != conv || !want[r.ID] {
			t.Fatalf("unpresented guest row: %+v", r)
		}
	}
}

func TestUnreadKeyUsesLogicalAuthor(t *testing.T) {
	original := UnreadRow{Kind: "chat", Conv: "scope", Author: "author", LID: "logical", ID: "physical-one"}
	selected := original
	selected.ID = "physical-two"
	if unreadKey(original) != unreadKey(selected) {
		t.Fatal("a new selected carrier changed logical identity")
	}
	selected.Author = "other-author"
	if unreadKey(original) == unreadKey(selected) {
		t.Fatal("same LID merged different authors")
	}
}

// Same-second arrivals keep their actual subsecond order across page boundaries.
func TestUnreadSubsecondOrder(t *testing.T) {
	alice, _, _, _, _, stops := liveGuestWorld(t)
	for _, stop := range stops {
		stop()
	}
	revision, _ := alice.ViewChanges()
	live := NewLive(alice)
	stamp := time.Unix(1792700000, 0)
	live.unread = &unreadIndex{content: revision.Content, seq: revision.Seq, rows: []UnreadRow{
		{Key: "a", At: stamp.Add(10 * time.Millisecond)},
		{Key: "b", At: stamp.Add(90 * time.Millisecond)},
		{Key: "c", At: stamp.Add(50 * time.Millisecond)},
	}}
	first, err := live.UnreadPage(UnreadQuery{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Rows) != 2 || first.Rows[0].Key != "b" || first.Rows[1].Key != "c" {
		t.Fatalf("same-second arrival order: %+v", first.Rows)
	}
	next, err := live.UnreadPage(UnreadQuery{Limit: 2, After: first.Next})
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Rows) != 1 || next.Rows[0].Key != "a" {
		t.Fatalf("subsecond cursor lost or repeated rows: %+v", next.Rows)
	}
}
