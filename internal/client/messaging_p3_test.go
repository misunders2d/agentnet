package client

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestSplitTrailers(t *testing.T) {
	for _, s := range []string{"finished\ntopic: done\nreaction: 👍", "finished\nreaction: 👍\ntopic: done", "finished\nTOPIC: DONE"} {
		body, reaction, done := splitTrailers(s, envelope.StatusDone)
		if body != "finished" || !done || strings.Contains(s, "reaction:") && reaction == nil {
			t.Fatalf("%q -> %q %v %v", s, body, reaction, done)
		}
	}
	for _, s := range []string{"topic: done", "still working", "finished\ntopic: maybe", "finished\nreaction: nope"} {
		_, _, done := splitTrailers(s, envelope.StatusDone)
		if done {
			t.Fatal(s)
		}
	}
	_, _, done := splitTrailers("failed\ntopic: done", envelope.StatusFailed)
	if done {
		t.Fatal("failed reply closed topic")
	}
}
func TestApplyReceiptNeverDowngrades(t *testing.T) {
	w := newWorld(t, "")
	sent, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "stored is not read"})
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"delivered", "failed", "not_delivered", "waiting"} {
		w.alice.store.db.Exec(`UPDATE outbox SET state=? WHERE id=?`, state, sent.ID)
		if err = w.alice.store.applyReceipt(protocol.ReceiptEvent{ID: sent.ID, State: "expired", Seq: 10}); err != nil {
			t.Fatal(err)
		}
		var got string
		w.alice.store.db.QueryRow(`SELECT state FROM outbox WHERE id=?`, sent.ID).Scan(&got)
		if got != state {
			t.Fatalf("%s -> %s", state, got)
		}
	}
	w.alice.store.db.Exec(`UPDATE outbox SET state='quarantined' WHERE id=?`, sent.ID)
	if err = w.alice.store.applyReceipt(protocol.ReceiptEvent{ID: sent.ID, State: "delivered", Seq: 11}); err != nil {
		t.Fatal(err)
	}
	if err = w.alice.store.applyReceipt(protocol.ReceiptEvent{ID: protocol.NewID(), State: "delivered", Seq: 8}); err != nil {
		t.Fatal(err)
	}
	cursor, _ := w.alice.store.config("receipt_cursor")
	if cursor != "8" {
		t.Fatal(cursor)
	}
}

type receiptCountTransport struct {
	http.RoundTripper
	n atomic.Int64
}

func (t *receiptCountTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/v1/messages/") {
		t.n.Add(1)
	}
	return t.RoundTripper.RoundTrip(r)
}
func TestPushedReceiptReachesConversation(t *testing.T) {
	w := newWorld(t, "")
	tr := &receiptCountTransport{RoundTripper: w.alice.hub.http.Transport}
	w.alice.hub.http.Transport = tr
	runAgent(t, w.alice)
	sent, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "receipt comes later"})
	if err != nil {
		t.Fatal(err)
	}
	runAgent(t, w.bob)
	eventually(t, "pushed receipt", func() bool {
		var state string
		w.alice.store.db.QueryRow(`SELECT state FROM outbox WHERE id=?`, sent.ID).Scan(&state)
		return state == "delivered"
	})
	if tr.n.Load() != 0 {
		t.Fatalf("receipt used %d status reads", tr.n.Load())
	}
}
func TestQuoteMarkAndQuotedMessageInPrompt(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.bob)
	runAgent(t, w.alice)
	old, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "OLD QUOTED CONTEXT"})
	if err != nil {
		t.Fatal(err)
	}
	last := old.ID
	for i := 0; i < 6; i++ {
		sent, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "later turn", ReplyTo: last})
		if err != nil {
			t.Fatal(err)
		}
		last = sent.ID
	}
	sent, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: "question", Body: "explain earlier", ReplyTo: last, Quote: old.ID})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "quote received", func() bool {
		var q string
		w.bob.store.db.QueryRow(`SELECT coalesce(quote,'') FROM inbox WHERE id=?`, sent.ID).Scan(&q)
		return q == old.ID
	})
	prompt, err := w.bob.prompt(job{ID: sent.ID, From: w.alice.Address, Kind: "question", Body: "explain earlier", ReplyTo: last, Quote: old.ID}, &Responder{Harness: "agentstub"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, "OLD QUOTED CONTEXT") {
		t.Fatal(prompt)
	}
	prompt, err = w.bob.prompt(job{ID: sent.ID, From: "other/device", Kind: "question", Body: "other", Quote: old.ID}, &Responder{Harness: "agentstub"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(prompt, "OLD QUOTED CONTEXT") {
		t.Fatal("cross-peer quote disclosure")
	}
}

func TestReceiptCursorPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.applyReceipt(protocol.ReceiptEvent{ID: protocol.NewID(), State: "delivered", Seq: 21}); err != nil {
		t.Fatal(err)
	}
	s.db.Close()
	s, err = openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	if got, _ := s.config("receipt_cursor"); got != "21" {
		t.Fatal(got)
	}
}
func TestReceiptWinsLateCustody(t *testing.T) {
	w := newWorld(t, "")
	sent, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if err = w.alice.store.applyReceipt(protocol.ReceiptEvent{ID: sent.ID, State: "delivered", Seq: 1}); err != nil {
		t.Fatal(err)
	}
	if err = w.alice.store.setOutboxState(sent.ID, "custody", "", "hub"); err != nil {
		t.Fatal(err)
	}
	got, _, _, err := w.alice.store.outboxState(sent.ID)
	if err != nil || got != "delivered" {
		t.Fatalf("%s %v", got, err)
	}
}

// Signed capability changes stand in for an older app, without a wall-clock wait.
func p3Caps(t *testing.T, a *Agent, caps []string) {
	t.Helper()
	label, device, _ := protocol.SplitAddress(a.Address)
	var p protocol.Profile
	if err := a.hub.do(tctx(t), "GET", "/v1/agents/"+label+"/"+device+"/profile", nil, &p); err != nil {
		t.Fatal(err)
	}
	ts := time.Now().Unix()
	for _, raw := range p.Caps {
		r, _ := protocol.ParseCapsRecord(raw)
		if r.TS >= ts {
			ts = r.TS + 1
		}
	}
	caps = slices.Clone(caps)
	slices.Sort(caps)
	for _, session := range p.Sessions {
		r := protocol.CapsRecord{Address: a.Address, Session: session, TS: ts, Caps: caps}
		r.Sign(a.id.Sign)
		if err := a.hub.do(tctx(t), "PUT", "/v1/caps", r, nil); err != nil {
			t.Fatal(err)
		}
	}
}
func TestHumanInviteWaitsForUpdate(t *testing.T) {
	w, carol, conv, _, _ := humanWorld(t)
	p3Caps(t, carol, without(ownCaps, protocol.CapHumanParticipation))
	support, err := w.alice.HumanInviteSupport(tctx(t), conv, carol.Address)
	if err != nil {
		t.Fatal(err)
	}
	updated := false
	for _, p := range support {
		updated = updated || p.State == "update"
	}
	if !updated {
		t.Fatalf("%+v", support)
	}
	inv, err := w.alice.InviteHuman(tctx(t), conv, carol.Address, nil, "join later")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "invitation waits for update", func() bool { p, e := w.alice.Participation(inv.PID); return e == nil && len(p.NeedsUpdate) > 0 })
	var waiting int
	if err = w.alice.store.db.QueryRow("SELECT count(*) FROM outbox WHERE pid=? AND state='waiting' AND required_cap=?", inv.PID, protocol.CapHumanParticipation).Scan(&waiting); err != nil || waiting == 0 {
		t.Fatalf("waiting %d %v", waiting, err)
	}
	if _, err := w.alice.store.db.Exec("UPDATE outbox SET error=? WHERE pid=? AND state='waiting'", WaitServerUnavailable+"cannot reach the Hub", inv.PID); err != nil {
		t.Fatal(err)
	}
	if p, e := w.alice.Participation(inv.PID); e != nil || len(p.NeedsUpdate) != 0 {
		t.Fatalf("server wait incorrectly blames a guest update: %+v %v", p, e)
	}
	p3Caps(t, carol, ownCaps)
	eventually(t, "updated guest sees its invitation", func() bool {
		p, e := carol.Participation(inv.PID)
		return e == nil && p.State == PartInvited && p.HostHere
	})
	eventually(t, "waiting copy released", func() bool { p, e := w.alice.Participation(inv.PID); return e == nil && len(p.NeedsUpdate) == 0 })
}
func TestNeedsUpdateError(t *testing.T) {
	e := &NeedsUpdateError{Address: "peer/laptop", Cap: protocol.CapHumanParticipation}
	if !errors.Is(e, errAgentIdentityUnsupported) || !strings.Contains(e.Error(), "cannot read human participation yet") {
		t.Fatal(e)
	}
}
func TestQuoteKeepsSession(t *testing.T) {
	st := sessionStub(t)
	w := newWorld(t, "")
	setResponder(t, w.bob, "cstyle", st.dir, time.Minute)
	w.bob.Approve(w.alice.Address)
	runWith(t, w, w.bob, RunOptions{})
	runWith(t, w, w.alice, RunOptions{})
	q1, a1 := ask(t, w, "")
	ref := sessionOf(t, w.bob, q1)
	q2, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: "question", Body: "explain earlier", ReplyTo: a1, Quote: q1})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "quoted follow-up answered", func() bool { _, ok := findReply(w.alice, q2.ID); return ok })
	if r := sessionOf(t, w.bob, q2.ID); r == nil || ref == nil || r.ID != ref.ID {
		t.Fatalf("session changed: %+v %+v", ref, r)
	}
	runs := st.runs()
	if len(runs) != 2 || !strings.Contains(runs[1], "--resume "+ref.ID) {
		t.Fatal(runs)
	}
}
func TestTopicCloseSignal(t *testing.T) {
	st := installStub(t, "answer")
	h := Harnesses["stub"]
	script := strings.Replace(stubScript, "*) echo \"stub answer\" ;;", "*) printf 'finished\\ntopic: done\\nreaction: 👍\\n' ;;", 1)
	if err := os.WriteFile(h.bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	w := newWorld(t, "")
	setResponder(t, w.bob, "stub", st.dir, time.Minute)
	w.bob.Approve(w.alice.Address)
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	q, answer := ask(t, w, "")
	prompt, promptErr := os.ReadFile(st.log + ".stdin")
	if promptErr != nil || !strings.Contains(string(prompt), topicClosurePromptText) {
		t.Fatal("direct worker prompt lacks explicit-request-only topic closure instruction")
	}
	eventually(t, "explicit close received", func() bool { return topicOf(t, w.alice, q).State == TopicDone })
	topic := topicOf(t, w.alice, q)
	if topic.DoneBy != DoneByAgent || topic.Conclusion != "finished" {
		data, _ := json.Marshal(topic)
		t.Fatal(string(data))
	}
	_, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "one more thing", ReplyTo: answer})
	if err != nil {
		t.Fatal(err)
	}
	if topicOf(t, w.alice, q).State != TopicActive {
		t.Fatal("new message did not reopen topic")
	}
}

func TestGuestSeesQuote(t *testing.T) {
	w, carol, conv, lids, _ := humanWorld(t)
	inv, err := w.alice.InviteHuman(tctx(t), conv, carol.Address, []string{lids[0]}, "")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "guest invitation", func() bool { p, e := carol.Participation(inv.PID); return e == nil && p.State == PartInvited })
	if _, err = carol.AcceptParticipation(tctx(t), inv.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "guest active", func() bool { p, e := w.alice.Participation(inv.PID); return e == nil && p.HumanActive() })
	sent, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "quoted old history", Quote: lids[0]})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "guest sees quote", func() bool {
		rows, e := carol.ConversationMessages(conv)
		if e != nil {
			return false
		}
		for _, m := range rows {
			if m.LID == sent.LID {
				return m.Quote == lids[0]
			}
		}
		return false
	})
	rows, err := carol.ConversationMessages(conv)
	if err != nil {
		t.Fatal(err)
	}
	var selected string
	for _, m := range rows {
		if m.LID == sent.LID {
			selected = m.ID
		}
	}
	reply, err := carol.SendConv(tctx(t), conv, ConvOutgoing{Body: "guest quoted reply", ReplyTo: selected, Quote: selected})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "original sees normalized guest quote", func() bool {
		rows, e := w.bob.ConversationMessages(conv)
		if e != nil {
			return false
		}
		for _, m := range rows {
			if m.LID == reply.LID {
				return m.Quote == sent.LID
			}
		}
		return false
	})
}

func TestHumanInviteSupport(t *testing.T) {
	key, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	address := "carol/box"
	caps := protocol.CapsRecord{Address: address, Session: protocol.NewID(), TS: time.Now().Unix(), Caps: []string{protocol.CapHumanParticipation}}
	caps.Sign(key.Sign)
	raw, _ := json.Marshal(caps)
	p := protocol.Profile{Sessions: []string{caps.Session}, Caps: []json.RawMessage{raw}, Live: true}
	if got := humanSupportState(p, address, key.Public(address).SignKey); got != "ok" {
		t.Fatal(got)
	}
	p.Live = false
	if got := humanSupportState(p, address, key.Public(address).SignKey); got != "offline" {
		t.Fatal(got)
	}
	p.Caps = nil
	if got := humanSupportState(p, address, key.Public(address).SignKey); got != "update" {
		t.Fatal(got)
	}
	p.Sessions = nil
	if got := humanSupportState(p, address, key.Public(address).SignKey); got != "not set up" {
		t.Fatal(got)
	}
	p.Sessions = []string{caps.Session}
	p.Caps = []json.RawMessage{raw}
	changed, _ := identity.Generate()
	if got := humanSupportState(p, address, changed.Public(address).SignKey); got != "update" {
		t.Fatalf("changed key accepted: %s", got)
	}
}
