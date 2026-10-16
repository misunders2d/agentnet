package client

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestBodyPreviewParity(t *testing.T) {
	data, err := os.ReadFile("testdata/body_preview_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct{ Name, Body, Want string }
	if err = json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			if got := BodyPreview(c.Body); got != c.Want {
				t.Fatalf("got %q, want %q", got, c.Want)
			}
		})
	}
	for _, count := range []int{1000, 4096, 4097} {
		refs := make([]string, count)
		for i := range refs {
			refs[i] = fmt.Sprintf("p.%064x", i)
		}
		body := "[@Amazon_team](agentnet:collective/v1/" + strings.Join(refs, ",") + ") Check inventory"
		want := "@Amazon_team Check inventory"
		if count > 4096 {
			want = firstLine(body)
		}
		if got := BodyPreview(body); got != want {
			t.Fatalf("%d refs: preview %q, want %q", count, got, want)
		}
	}
	oversize := "[@team](agentnet:collective/v1/" + strings.Repeat("a", 280001) + ")"
	if BodyPreview(oversize) != firstLine(oversize) {
		t.Fatal("oversize payload must remain inert")
	}
	literal := "[@filename](agentnet:person/a)"
	if firstLine(literal) != literal {
		t.Fatal("literal clipping changed")
	}
}

func TestDeviceCollectivePreview(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	refs := make([]string, 1000)
	for i := range refs {
		refs[i] = fmt.Sprintf("a.%032x", i)
	}
	body := "[@Amazon_team](agentnet:collective/v1/" + strings.Join(refs, ",") + ") Check inventory"
	sent, err := w.alice.Send(tctx(t), w.bob.Address, body, "")
	if err != nil {
		t.Fatal(err)
	}
	topic := topicOf(t, w.alice, sent.ID)
	if topic.Title != "@Amazon_team Check inventory" || topic.Last != topic.Title {
		t.Fatalf("device preview title=%q last=%q", topic.Title, topic.Last)
	}
	ref, err := w.alice.RefOf("", sent.ID, "out")
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(body, "Amazon_team", "Edited_team", 1)
	if _, err = w.alice.Revise(tctx(t), ref, edited); err != nil {
		t.Fatal(err)
	}
	check := func() {
		t.Helper()
		row := legacyView(t, w.alice, sent.ID)
		if row.Body != body || !row.Edited || row.Text != edited {
			t.Fatal("resolved edit or original body changed")
		}
		topic = topicOf(t, w.alice, sent.ID)
		if topic.Last != "@Edited_team Check inventory" {
			t.Fatalf("resolved topic preview %q", topic.Last)
		}
		ts, _, err := w.alice.TopicOverview(true)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range ts {
			if s.ID == sent.ID && s.Last != topic.Last {
				t.Fatalf("overview=%q topic=%q", s.Last, topic.Last)
			}
		}
	}
	check()
	// Stored controls are still checked when displayed. A different author's
	// key, a changed key and the right author in another peer scope cannot win.
	for _, c := range []struct{ peer, key, ref string }{
		{w.bob.Address, w.bob.Self().Fingerprint(), sent.ID},
		{w.bob.Address, "aaaaaaaa-bbbbbbbb-cccccccc-dddddddd", sent.ID},
		{"other/desk", w.alice.Self().Fingerprint(), sent.ID},
		{w.bob.Address, w.alice.Self().Fingerprint(), protocol.NewID()},
	} {
		in := envelope.Inner{ID: protocol.NewID(), From: c.peer, Kind: envelope.KindMessage, Sub: envelope.SubRevision,
			Body: `{"rev":99,"text":"wrong scoped edit"}`, Ref: &envelope.Ref{ID: c.ref, Fingerprint: ref.Fingerprint}}
		if err = w.alice.store.addControlInbox(in, c.key, false); err != nil {
			t.Fatal(err)
		}
		check()
	}
	literal := "[@literal](agentnet:person/name)"
	if err = w.alice.RenameTopic(w.bob.Address, sent.ID, literal); err != nil {
		t.Fatal(err)
	}
	topic = topicOf(t, w.alice, sent.ID)
	if topic.Title != literal || topic.AutoTitle != "@Edited_team Check inventory" {
		t.Fatalf("literal title=%q auto=%q", topic.Title, topic.AutoTitle)
	}
	row := legacyView(t, w.alice, sent.ID)
	if row.Body != body {
		t.Fatal("preview changed signed direct body")
	}
	if _, err = w.alice.Retract(tctx(t), ref, ""); err != nil {
		t.Fatal(err)
	}
	if got := topicOf(t, w.alice, sent.ID); got.Last != "Message deleted" || got.Title != literal {
		t.Fatalf("retracted preview title=%q last=%q", got.Title, got.Last)
	}
}
