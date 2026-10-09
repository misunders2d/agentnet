package client

import (
	"errors"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParticipationTopicScopeContextAndExactRequest(t *testing.T) {
	w, conv, lids := dmWithHistory(t)
	topic := lids[0]
	if _, err := w.alice.ChangeChatTopic(tctx(t), conv, topic, "create", "", 0); err != nil {
		t.Fatal(err)
	}
	eventually(t, "topic promotion", func() bool { v, e := topicReference(w.bob.store.db, conv, topic, ""); return e == nil && v == topic })
	if _, err := w.alice.InviteAgentInScope(tctx(t), conv, w.bob.Address, "", lids[1:2], nil, "", &topic); !errors.Is(err, errParticipationTopic) {
		t.Fatalf("outside selected context: %v", err)
	}
	p, err := w.alice.InviteAgentInScope(tctx(t), conv, w.bob.Address, "", lids[:1], nil, "", &topic)
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "scoped invitation", func() bool { return stateAt(t, w.bob, p.PID).State == PartInvited })
	if _, err = w.bob.AcceptParticipation(tctx(t), p.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "scoped acceptance", func() bool { return stateAt(t, w.alice, p.PID).Claimable() })
	c, err := w.bob.ParticipationContext(p.PID, 0)
	if err != nil || len(c.Messages) != 1 || c.Messages[0].LID != topic {
		t.Fatalf("scoped context: %+v %v", c, err)
	}
	if _, err = w.alice.AskAgent(tctx(t), p.PID, envelope.KindQuestion, "wrong Main request"); !errors.Is(err, errParticipationTopic) {
		t.Fatalf("outside request: %v", err)
	}
	ask, err := w.alice.AskAgentInTopic(tctx(t), p.PID, envelope.KindQuestion, "this topic only", topic, nil)
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "scoped request received", func() bool { return inboxCount(t, w.bob, `id=? AND state=?`, ask.ID, stateAgentWaiting) == 1 })
	if _, err = w.alice.ChangeChatTopic(tctx(t), conv, topic, "rename", "Renamed", 0); err != nil {
		t.Fatal(err)
	}
	got, err := w.bob.Participation(p.PID)
	if err != nil || got.Topic == nil || *got.Topic != topic {
		t.Fatal("name changed signed identity", err)
	}
	// Old/malformed persisted rows may not gain execution from acceptance.
	_, err = w.bob.store.db.Exec(`UPDATE inbox SET topic=NULL,reply_to=? WHERE id=?`, lids[1], ask.ID)
	if err != nil {
		t.Fatal(err)
	}
	r := agentReq{ID: ask.ID, Sender: w.alice.Address, Key: w.alice.Self().Fingerprint(), Kind: envelope.KindQuestion, Conv: conv, PID: p.PID, State: stateAccepted, Target: &envelope.Target{Address: w.bob.Address, Fingerprint: w.bob.Self().Fingerprint()}}
	verdict, _, err := agentVerdict(w.bob.store.db, r, w.bob.Address, w.bob.Self().Fingerprint(), false, map[string]*partView{})
	if err != nil || verdict != verdictStop {
		t.Fatalf("outside persisted request became runnable: %d %v", verdict, err)
	}
}

func TestParticipationTopicScopeMainAndMissingParent(t *testing.T) {
	w, conv, lids := dmWithHistory(t)
	main := ""
	p, err := w.alice.InviteAgentInScope(tctx(t), conv, w.bob.Address, "", lids[:1], nil, "", &main)
	if err != nil {
		t.Fatal(err)
	}
	if p.Topic == nil || *p.Topic != "" {
		t.Fatal("explicit Main became whole chat")
	}

	tx, err := w.alice.store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	called := false
	b := &replyBinding{receiver: ReplyReceiver{Kind: "live_session", SessionHandle: "synthetic"}, resolve: func(dbq) (*ExecutorStamp, error) { called = true; return nil, errors.New("synthetic receiver resolve") }}
	err = bindReplyReceiver(tx, b, []outCopy{{in: envelope.Inner{ID: protocol.NewID(), LID: protocol.NewID(), Conv: conv, PID: p.PID, Kind: envelope.KindQuestion}}})
	tx.Rollback()
	if called || err == nil || !strings.Contains(err.Error(), "Choose Yourself") {
		t.Fatalf("scoped native receiver was not refused before binding: called=%v err=%v", called, err)
	}
	for _, r := range []*replyBinding{nil, {receiver: ReplyReceiver{Kind: "human"}}} {
		if err = checkTopicReplyReceiver(w.alice.store.db, r, envelope.Inner{Conv: conv, PID: p.PID}); err != nil {
			t.Fatal("ordinary human reply blocked", err)
		}
	}
	if err = checkTopicReplyReceiver(w.alice.store.db, b, envelope.Inner{Conv: conv}); err != nil {
		t.Fatal("whole-chat selected receiver changed", err)
	}
	if err = checkParticipationTopic(w.alice.store.db, &main, envelope.Inner{Conv: conv, ReplyTo: strings.Repeat("f", 32)}); !errors.Is(err, errParticipationTopicPending) {
		t.Fatalf("missing parent inferred Main: %v", err)
	}
	if err = checkParticipationTopic(w.alice.store.db, &main, envelope.Inner{Conv: conv, Topic: protocol.NewID()}); !errors.Is(err, errParticipationTopic) {
		t.Fatal("other topic admitted", err)
	}
	if err = checkParticipationTopic(w.alice.store.db, nil, envelope.Inner{Conv: conv, ReplyTo: strings.Repeat("f", 32)}); err != nil {
		t.Fatal("legacy scope behavior changed", err)
	}
}

func TestParticipationTopicScopeGroupFreshPIDAndFanout(t *testing.T) {
	w, _, packet, _ := groupTurnsFixture(t)
	conv := packet.State.Conv
	broad := p6Member(t, w.alice, w.bob, conv)
	topic := protocol.NewID()
	inside, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "SCOPED_SELECTED", Topic: topic})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "topic source", func() bool { return inboxCount(t, w.bob, `conv=? AND lid=?`, conv, inside.LID) == 1 })
	narrow, err := w.alice.InviteAgentInScope(tctx(t), conv, w.bob.Address, "", []string{inside.LID}, nil, "", &topic)
	if err != nil {
		t.Fatal(err)
	}
	if narrow.PID == broad.PID {
		t.Fatal("broad membership relabeled narrower")
	}
	eventually(t, "scoped group invitation", func() bool { return stateAt(t, w.bob, narrow.PID).State == PartInvited })
	if _, err = w.bob.AcceptParticipation(tctx(t), narrow.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "scoped group acceptance", func() bool { return stateAt(t, w.alice, narrow.PID).Claimable() })
	if _, err = w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "OTHER_TOPIC_PRIVATE"}); err != nil {
		t.Fatal(err)
	}
	same, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "SCOPED_LIVE", Topic: topic})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		rows, _ := w.alice.store.db.Query(`SELECT recipient,state,coalesce(error,'') FROM outbox WHERE lid=?`, same.LID)
		if rows != nil {
			defer rows.Close()
			for rows.Next() {
				var to, state, why string
				rows.Scan(&to, &state, &why)
				t.Log("source", to, state, why)
			}
		}
		rows, _ = w.bob.store.db.Query(`SELECT reason,coalesce(detail_code,'') FROM quarantine`)
		if rows != nil {
			defer rows.Close()
			for rows.Next() {
				var reason, detail string
				rows.Scan(&reason, &detail)
				t.Log("held", reason, detail)
			}
		}
	})
	eventually(t, "scoped live turn", func() bool { return inboxCount(t, w.bob, `conv=? AND lid=?`, conv, same.LID) == 1 })
	c, err := w.bob.ParticipationContext(narrow.PID, 0)
	if err != nil {
		t.Fatal(err)
	}
	selected, live := false, false
	for _, m := range c.Messages {
		if m.Body == "OTHER_TOPIC_PRIVATE" {
			t.Fatal("other topic leaked into scoped context")
		}
		selected = selected || m.Body == "SCOPED_SELECTED"
		live = live || m.Body == "SCOPED_LIVE"
	}
	if !selected || !live {
		t.Fatalf("selected=%v live=%v", selected, live)
	}
	if !stateAt(t, w.alice, broad.PID).Claimable() {
		t.Fatal("new narrow scope changed existing broad access")
	}
}

func TestParticipationTopicScopeHumanGuestAndOldPeer(t *testing.T) {
	w, guest, conv, _, _ := humanWorld(t)
	topic := protocol.NewID()
	file := filepath.Join(t.TempDir(), "scoped.txt")
	if err := os.WriteFile(file, []byte("SCOPED_FILE_BYTES"), 0600); err != nil {
		t.Fatal(err)
	}
	selected, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "ONLY_SELECTED_TOPIC", Topic: topic, Files: []OutgoingFile{{Path: file}}})
	if err != nil {
		t.Fatal(err)
	}
	signCapsAfter(t, guest, without(ownCaps, protocol.CapTopicParticipation))
	if _, err = w.alice.InviteHumanInScope(tctx(t), conv, guest.Address, []string{selected.LID}, "", &topic); err == nil {
		t.Fatal("old reader received scoped invitation")
	}
	signCapsAfter(t, guest, ownCaps)
	p, err := w.alice.InviteHumanInScope(tctx(t), conv, guest.Address, []string{selected.LID}, "", &topic)
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "topic guest invitation", func() bool { return stateAt(t, guest, p.PID).State == PartInvited })
	if _, err = guest.AcceptParticipation(tctx(t), p.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "guest accepted scope", func() bool { return stateAt(t, w.alice, p.PID).HumanActive() })
	eventually(t, "only selected excerpt", func() bool { return humanBodyCount(t, guest, conv, "ONLY_SELECTED_TOPIC") == 1 })
	msgs, e := guest.ConversationMessages(conv)
	if e != nil {
		t.Fatal(e)
	}
	opened := false
	for _, msg := range msgs {
		if msg.ExcerptPID == p.PID && msg.Body == "ONLY_SELECTED_TOPIC" {
			r, _, e := guest.OpenFileFrom(tctx(t), "in", msg.ID, 0)
			if e != nil {
				t.Fatal(e)
			}
			data, e := io.ReadAll(r)
			r.Close()
			if e != nil || string(data) != "SCOPED_FILE_BYTES" {
				t.Fatalf("selected exact bytes: %q %v", data, e)
			}
			opened = true
		}
	}
	if !opened {
		t.Fatal("scoped selected file not available")
	}
	outside, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "OTHER_TOPIC_NOT_DISCLOSED", Files: []OutgoingFile{{Path: file}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range outside.Copies {
		if c.To == guest.Address {
			t.Fatal("scoped guest got other-topic encrypted carrier")
		}
	}
	inside, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "LIVE_SAME_TOPIC", Topic: topic})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "same topic live guest copy", func() bool { return humanBodyCount(t, guest, conv, "LIVE_SAME_TOPIC") == 1 })
	if _, err = guest.SendConv(tctx(t), conv, ConvOutgoing{Body: "guest right topic", Topic: topic, ReplyTo: inside.LID}); err != nil {
		t.Fatal(err)
	}
	if _, err = guest.SendConv(tctx(t), conv, ConvOutgoing{Body: "guest wrong Main", PID: p.PID}); err == nil {
		t.Fatal("topic guest authored outside scope")
	}
	if _, err = w.alice.DismissParticipation(tctx(t), p.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "guest scope ended", func() bool { return stateAt(t, guest, p.PID).State == PartDismissed })
	if _, err = guest.SendConv(tctx(t), conv, ConvOutgoing{Body: "after removal", Topic: topic, PID: p.PID}); err == nil {
		t.Fatal("removed topic guest sent")
	}
}
