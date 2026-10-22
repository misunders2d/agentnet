package client

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// The person's synchronized read state is authoritative for every native
// session. Session cursors do not create independent unread inboxes.
func TestAttentionOwnReadSyncDoesNotReplay(t *testing.T) {
	w := newWorld(t, "")
	stop := runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	phone := linked(t, w.alice)
	conv := newDM(t, w.bob, w.alice)
	for _, harness := range []string{"codex", "claude", "pi"} {
		for _, session := range []string{"existing", "resumed"} {
			at, err := w.alice.Attention(HookEvent{Harness: harness, Session: session, Event: "SessionStart"})
			if err != nil {
				t.Fatal(err)
			}
			if err := at.Commit(); err != nil {
				t.Fatal(err)
			}
		}
	}
	for i := range attentionItems + 3 {
		if _, err := w.bob.SendConv(tctx(t), conv, ConvOutgoing{Body: fmt.Sprintf("read elsewhere %02d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, "ordinary turns on both own devices", func() bool {
		return len(convBodies(t, w.alice, conv)) == attentionItems+3 && len(convBodies(t, phone, conv)) == attentionItems+3
	})
	var phoneIDs, readIDs []string
	for i := range attentionItems + 2 {
		body := fmt.Sprintf("read elsewhere %02d", i)
		id, _ := readState(t, phone, conv, body)
		phoneIDs = append(phoneIDs, id)
		id, _ = readState(t, w.alice, conv, body)
		readIDs = append(readIDs, id)
	}
	if err := phone.MarkRead(phoneIDs); err != nil {
		t.Fatal(err)
	}
	eventually(t, "phone reads synchronize without reading newer turn", func() bool {
		for i := range attentionItems + 2 {
			if _, read := readState(t, w.alice, conv, fmt.Sprintf("read elsewhere %02d", i)); !read {
				return false
			}
		}
		return true
	})
	unseen, read := readState(t, w.alice, conv, fmt.Sprintf("read elsewhere %02d", attentionItems+2))
	if read {
		t.Fatal("synchronized read cleared a newer unseen turn")
	}
	stop()
	if err := w.alice.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := Open(w.alice.home)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	for _, harness := range []string{"codex", "claude", "pi"} {
		for _, session := range []string{"existing", "new", "resumed"} {
			t.Run(harness+"/"+session, func(t *testing.T) {
				event := "SessionStart"
				if session == "existing" {
					event = "UserPromptSubmit"
				}
				at, err := again.Attention(HookEvent{Harness: harness, Session: session, Event: event})
				if err != nil {
					t.Fatal(err)
				}
				var replayed []string
				for _, id := range readIDs {
					if strings.Contains(at.Text, id) {
						replayed = append(replayed, id)
					}
				}
				if len(replayed) > 0 {
					t.Errorf("synchronized read turns replayed: %v", replayed)
				}
				if !strings.Contains(at.Text, unseen) {
					t.Errorf("newer unread turn %s was starved: %s", unseen, at.Text)
				}
			})
		}
	}
}

func TestAttentionFilteredReadCursorProgress(t *testing.T) {
	w := newWorld(t, "")
	shown(t, w.alice, "filtered", "SessionStart")
	for i := range attentionItems + 4 {
		id := fmt.Sprintf("already-read-%02d", i)
		if err := w.alice.store.addInbox(envelope.Inner{ID: id, From: w.bob.Address, To: w.alice.Address,
			TS: time.Now().Unix(), Kind: envelope.KindMessage, Body: id}, ""); err != nil {
			t.Fatal(err)
		}
		if err := w.alice.MarkRead([]string{id}); err != nil {
			t.Fatal(err)
		}
	}
	at := attention(t, w.alice, "filtered", "PostToolUse")
	if at.Text != "" {
		t.Errorf("read-only backlog produced attention: %s", at.Text)
	}
	if err := at.Commit(); err != nil {
		t.Fatal(err)
	}
	top, err := w.alice.store.arrivalTop()
	if err != nil {
		t.Fatal(err)
	}
	pos, known, err := w.alice.store.cursor("claude", "filtered")
	if err != nil || !known || pos != top {
		t.Errorf("filtered cursor = %d, known=%v, err=%v; want scanned top %d", pos, known, err, top)
	}
	for i := range attentionItems + 2 {
		id := fmt.Sprintf("later-unread-%02d", i)
		if err := w.alice.store.addInbox(envelope.Inner{ID: id, From: w.bob.Address, To: w.alice.Address,
			TS: time.Now().Unix(), Kind: envelope.KindMessage, Body: id}, ""); err != nil {
			t.Fatal(err)
		}
		readID := fmt.Sprintf("interleaved-read-%02d", i)
		if err := w.alice.store.addInbox(envelope.Inner{ID: readID, From: w.bob.Address, To: w.alice.Address,
			TS: time.Now().Unix(), Kind: envelope.KindMessage, Body: readID}, ""); err != nil {
			t.Fatal(err)
		}
		if err := w.alice.MarkRead([]string{readID}); err != nil {
			t.Fatal(err)
		}
	}
	first := shown(t, w.alice, "filtered", "PostToolUse")
	second := shown(t, w.alice, "filtered", "PostToolUse")
	for i := range attentionItems + 2 {
		id := fmt.Sprintf("later-unread-%02d", i)
		if strings.Count(first+second, id) != 1 {
			t.Errorf("later eligible arrival %s lost or duplicated: %s\n%s", id, first, second)
		}
	}
	if got := shown(t, w.alice, "filtered", "PostToolUse"); got != "" {
		t.Errorf("drained arrivals replayed: %s", got)
	}
	top, err = w.alice.store.arrivalTop()
	if err != nil {
		t.Fatal(err)
	}
	pos, _, err = w.alice.store.cursor("claude", "filtered")
	if err != nil || pos != top {
		t.Errorf("trailing filtered rows stalled cursor at %d, want %d: %v", pos, top, err)
	}
}

func TestAttentionUsesSynchronizedGeneration(t *testing.T) {
	w, phone, conv, ids := historyCatchupFixture(t, 2)
	a := w.alice
	if err := a.MarkRead([]string{ids[0]}); err != nil {
		t.Fatal(err)
	}
	me, _, err := a.store.selfPerson(a.Address)
	if err != nil {
		t.Fatal(err)
	}
	marks := make([]protocol.AttentionMark, 0, 2)
	for i, id := range ids {
		m := attentionMessage(t, a, conv, id)
		marks = append(marks, protocol.AttentionMark{ReadRef: convAttentionRef(conv, m),
			AttentionGeneration: protocol.AttentionGeneration{Rev: 1, Writer: phone.Self().Fingerprint()},
			State:               []string{"unread", "read"}[i]})
	}
	raw, err := json.Marshal(protocol.ReadSync{V: 2, Person: me.info.Person, Roster: me.info.Roster, Marks: marks})
	if err != nil {
		t.Fatal(err)
	}
	carrier := craft(t, phone, a, envelope.Inner{Kind: envelope.KindMessage, Sub: envelope.SubReadSync, Replica: true, Body: string(raw)})
	if err := a.verifyAndStore(tctx(t), carrier); err != nil {
		t.Fatal(err)
	}
	if !attentionMessage(t, a, conv, ids[0]).Unread || attentionMessage(t, a, conv, ids[1]).Unread {
		t.Fatal("fixture's signed generations did not override opposite legacy read values")
	}
	at := attention(t, a, "generation", "SessionStart")
	if !strings.Contains(at.Text, "1 unread") || !strings.Contains(at.Text, ids[0]) || strings.Contains(at.Text, ids[1]) {
		t.Fatalf("hook disagrees with effective synchronized attention: %s", at.Text)
	}
}

func TestAttentionReadUnresolvedDecisionRemainsVisible(t *testing.T) {
	w := newWorld(t, "")
	shown(t, w.alice, "decision", "SessionStart")
	const id = "unresolved-question"
	if err := w.alice.store.addInbox(envelope.Inner{ID: id, From: w.bob.Address, To: w.alice.Address,
		TS: time.Now().Unix(), Kind: envelope.KindQuestion, Body: "needs consent"}, ""); err != nil {
		t.Fatal(err)
	}
	if err := w.alice.MarkRead([]string{id}); err != nil {
		t.Fatal(err)
	}
	if got := attention(t, w.alice, "decision", "Stop"); got.Text != "" {
		t.Fatalf("generic decision blocked Stop: %s", got.Text)
	}
	if got := shown(t, w.alice, "decision", "PostToolUse"); !strings.Contains(got, id) {
		t.Fatalf("reading hid an unresolved decision: %s", got)
	}
	msg, err := w.alice.store.inboxMessage(id)
	if err != nil || msg == nil || msg.State != stateHeld {
		t.Fatalf("reading changed execution authority: %+v %v", msg, err)
	}
	var attempts int
	if err := w.alice.store.db.QueryRow(`SELECT attempts FROM inbox WHERE id=?`, id).Scan(&attempts); err != nil || attempts != 0 {
		t.Fatalf("reading started work: attempts=%d err=%v", attempts, err)
	}
}

func TestAttentionNewStopDoesNotConsumeUnshownArrivals(t *testing.T) {
	w := newWorld(t, "")
	const id = "chat-before-first-stop"
	if err := w.alice.store.addInbox(envelope.Inner{ID: id, From: w.bob.Address, To: w.alice.Address,
		TS: time.Now().Unix(), Kind: envelope.KindMessage, Body: id}, ""); err != nil {
		t.Fatal(err)
	}
	at := attention(t, w.alice, "first-stop", "Stop")
	if at.Text != "" {
		t.Fatalf("ordinary chat blocked first Stop: %s", at.Text)
	}
	if err := at.Commit(); err != nil {
		t.Fatal(err)
	}
	if pos, known, err := w.alice.store.cursor("claude", "first-stop"); err != nil || known {
		t.Fatalf("first Stop consumed unshown arrivals: pos=%d known=%v err=%v", pos, known, err)
	}
	if next := shown(t, w.alice, "first-stop", "UserPromptSubmit"); !strings.Contains(next, id) {
		t.Fatalf("next normal boundary lost first Stop's unshown chat: %s", next)
	}
}

func TestAttentionReadReplyIsNotANewSessionInbox(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	phone := linked(t, w.alice)
	for _, harness := range []string{"codex", "claude", "pi"} {
		for _, session := range []string{"existing-read-reply", "resumed-read-reply"} {
			at, err := w.alice.Attention(HookEvent{Harness: harness, Session: session, Event: "SessionStart"})
			if err != nil {
				t.Fatal(err)
			}
			if err := at.Commit(); err != nil {
				t.Fatal(err)
			}
		}
	}
	q, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindQuestion, Body: "status?"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "question reaches peer", func() bool { return hasInbox(w.bob, "status?") })
	reply, err := w.bob.Reply(tctx(t), q.ID, "already read result")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "answer reaches owner", func() bool { return hasInbox(w.alice, "already read result") })
	eventually(t, "answer reaches linked phone", func() bool { return hasInbox(phone, "already read result") })
	if err := phone.MarkRead([]string{reply.ID}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "phone read reaches original device", func() bool {
		m, err := w.alice.store.inboxMessage(reply.ID)
		return err == nil && m != nil && m.Read
	})
	for _, harness := range []string{"codex", "claude", "pi"} {
		for _, c := range []struct{ session, event string }{{"new-read-reply", "SessionStart"}, {"existing-read-reply", "PostToolUse"}, {"resumed-read-reply", "SessionStart"}, {"existing-read-reply", "Stop"}} {
			at, err := w.alice.Attention(HookEvent{Harness: harness, Session: c.session, Event: c.event})
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(at.Text, reply.ID) {
				t.Errorf("phone-read unbound reply entered %s/%s/%s: %s", harness, c.session, c.event, at.Text)
			}
		}
	}
}

func TestAttentionReadSelectedReplyKeepsExactReceiver(t *testing.T) {
	w := newWorld(t, "")
	owner, call := nativeReceiverFixture(t, w.alice, "pi")
	_, id := nativeInput(t, w.alice, w.bob, owner.Handle)
	if err := w.alice.MarkRead([]string{id}); err != nil {
		t.Fatal(err)
	}
	if got := shown(t, w.alice, "unrelated-session", "SessionStart"); strings.Contains(got, id) {
		t.Fatalf("selected reply leaked into generic overview: %s", got)
	}
	delivery, err := w.alice.TakeReplyReceiverInput(call)
	if err != nil || delivery == nil || delivery.InputID != id {
		t.Fatalf("reading hid exact selected reply: %+v %v", delivery, err)
	}
}
