package client

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Each recipient has its own physical request ID. Only its shared logical
// identity can correlate the answer on a sibling that never held the host copy.
func TestDMAgentReplyUsesLogicalRequest(t *testing.T) {
	st := installAgentStub(t)
	w, conv, lids, _ := agentWorld(t)
	setResponder(t, w.bob, "agentstub", st.dir, time.Minute)
	pid := participate(t, w, conv, lids[:1], nil)
	q, err := w.alice.AskAgent(WithQueuedSend(tctx(t), protocol.NewID()), pid, envelope.KindQuestion, "synthetic exact reply")
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, q.ID, stateAnswered)
	var answer ConvMessage
	eventually(t, "agent answer stored", func() bool {
		answer, _ = convMsg(t, w.alice, conv, func(m ConvMessage) bool { return m.Kind == envelope.KindAnswer && m.PID == pid })
		return answer.ID != ""
	})
	if q.ID == q.LID {
		t.Fatal("fixture needs distinct physical and logical identities")
	}
	if answer.ReplyTo != q.LID {
		t.Fatalf("answer names host-only copy %s; want logical request %s", answer.ReplyTo, q.LID)
	}
	if st.runs() != 1 {
		t.Fatalf("request ran %d times", st.runs())
	}
}

func TestDMAgentOldAnsweredStatusRecovery(t *testing.T) {
	w, conv, lids, stop := agentWorld(t)
	phone := linkedVia(t, w.bob, "phone", func(ctx context.Context, id string) error { return w.bob.DecideLink(ctx, id, true) })
	runAgent(t, phone)
	pid := participate(t, w, conv, lids[:1], nil)
	eventually(t, "sibling participation", func() bool { return stateAt(t, phone, pid).Claimable() })
	stop() // construct an old successful job; no responder is launched
	ask := func() ConvSent {
		t.Helper()
		q, e := w.bob.AskAgent(WithQueuedSend(tctx(t), protocol.NewID()), pid, envelope.KindQuestion, "retained exact request")
		if e != nil {
			t.Fatal(e)
		}
		return q
	}
	q, pending := ask(), ask()
	observer := phone
	eventually(t, "sibling request", func() bool {
		_, n := convMsg(t, phone, conv, func(m ConvMessage) bool { return m.LID == q.LID })
		return n == 1
	})
	held, _ := convMsg(t, phone, conv, func(m ConvMessage) bool { return m.LID == q.LID })
	if held.ID == q.ID {
		observer = w.alice
	}
	eventually(t, "distinct recipient request", func() bool {
		m, n := convMsg(t, observer, conv, func(m ConvMessage) bool { return m.LID == q.LID })
		return n == 1 && m.ID != q.ID
	})
	_, raw, _, e := w.bob.store.conversation(conv)
	if e != nil {
		t.Fatal(e)
	}
	// The old host signed a correct output bound to its physical request.
	// Another copy's reader must not guess that association from text/order.
	in := envelope.Inner{V: envelope.Version2, ID: protocol.NewID(), LID: protocol.NewID(), From: w.bob.Address, To: observer.Address, TS: time.Now().Unix(), Kind: envelope.KindAnswer, Body: "retained successful reply", ReplyTo: q.ID, Status: envelope.StatusDone, Conv: conv, Root: raw, PID: pid, Origin: "agent:fixture", Emotion: "neutral"}
	recipient, _ := observer.Self().Recipient()
	env, e := envelope.Seal(in, w.bob.id.Sign, recipient)
	if e != nil {
		t.Fatal(e)
	}
	if e = w.bob.store.addConvOutbox([]outCopy{{env: env, in: in, state: stateQueued}}, envelope.Inner{}, nil, ""); e != nil {
		t.Fatal(e)
	}
	if _, e = w.bob.store.db.Exec(`UPDATE inbox SET state=?,result_id=?,attempts=1,status_due=0 WHERE id=?`, stateAnswered, in.ID, q.ID); e != nil {
		t.Fatal(e)
	}
	if e = w.bob.FlushOutbox(tctx(t)); e != nil {
		t.Fatal(e)
	}
	eventually(t, "old answer on sibling", func() bool {
		_, n := convMsg(t, observer, conv, func(m ConvMessage) bool { return m.LID == in.LID })
		return n == 1
	})
	before, e := observer.ConversationMessages(conv)
	if e != nil {
		t.Fatal(e)
	}
	view := summarizeChatTopicView(conv, before, nil, time.Now().Unix(), true)
	if len(view) != 1 || len(view[0].PendingIDs) != 2 {
		t.Fatalf("fixture did not retain the old unresolved reference: %+v", view)
	}
	if _, e = w.bob.store.db.Exec(answeredConversationStatusSchema); e != nil {
		t.Fatal(e)
	}
	var due int
	if e = w.bob.store.db.QueryRow(`SELECT status_due FROM inbox WHERE id=?`, q.ID).Scan(&due); e != nil || due != 1 {
		t.Fatalf("old exact success not queued: %d %v", due, e)
	}
	if !w.bob.tellStatus(tctx(t), q.ID) {
		t.Fatal("terminal status was not sent")
	}
	if e = w.bob.FlushOutbox(tctx(t)); e != nil {
		t.Fatal(e)
	}
	eventually(t, "exact old completion visible", func() bool {
		m, n := convMsg(t, observer, conv, func(m ConvMessage) bool { return m.LID == q.LID })
		return n == 1 && m.Exec != nil && m.Exec.State == "answered"
	})
	msgs, e := observer.ConversationMessages(conv)
	if e != nil {
		t.Fatal(e)
	}
	view = summarizeChatTopicView(conv, msgs, nil, time.Now().Unix(), true)
	pendingCopy, _ := convMsg(t, observer, conv, func(m ConvMessage) bool { return m.LID == pending.LID })
	if len(view) != 1 || !reflect.DeepEqual(view[0].PendingIDs, []string{pendingCopy.ID}) {
		t.Fatalf("exact pending after recovery: %+v", view)
	}
	var attempts int
	var state string
	if e = w.bob.store.db.QueryRow(`SELECT state,attempts,status_due FROM inbox WHERE id=?`, q.ID).Scan(&state, &attempts, &due); e != nil || state != stateAnswered || attempts != 1 || due != 0 {
		t.Fatalf("recovery changed execution or remained due: %s %d %d %v", state, attempts, due, e)
	}
	if !w.bob.tellStatus(tctx(t), q.ID) {
		t.Fatal("already told status failed")
	}
	var count int
	if e = w.bob.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE sub='status' AND ref_id=? AND recipient=?`, q.LID, observer.Address).Scan(&count); e != nil || count != 1 {
		t.Fatalf("repeat duplicated terminal status: %d %v", count, e)
	}
}

func TestDMAgentAnsweredRecoveryExcludesUnproven(t *testing.T) {
	p := filepath.Join(t.TempDir(), "agent.db")
	s, e := openStore(p)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { s.db.Close() }()
	var success string
	for _, change := range []string{"pending", "no-result", "wrong-parent", "wrong-conv", "wrong-pid", "wrong-target", "replica", "exact"} {
		id, lid, result := protocol.NewID(), protocol.NewID(), protocol.NewID()
		state, parent, conv, pid, target, replica := stateAnswered, id, "chat", "participant", "own/host", 0
		if change == "pending" {
			state = stateRunning
		}
		if change == "no-result" {
			result = ""
		}
		if change == "wrong-parent" {
			parent = protocol.NewID()
		}
		if change == "wrong-conv" {
			conv = "other"
		}
		if change == "wrong-pid" {
			pid = "other"
		}
		if change == "wrong-target" {
			target = "other/host"
		}
		if change == "replica" {
			replica = 1
		}
		targetJSON, _ := json.Marshal(envelope.Target{Address: target, Fingerprint: "synthetic"})
		_, e = s.db.Exec(`INSERT INTO inbox(id,lid,sender,ts,kind,body,received_at,state,verified_by,conv,pid,target,result_id,replica,attempts) VALUES(?,?,'own/host',1,'question','retained',1,?,'synthetic','chat','participant',?,?,?,1)`, id, lid, state, string(targetJSON), result, replica)
		if e != nil {
			t.Fatal(e)
		}
		_, e = s.db.Exec(`INSERT INTO outbox(id,recipient,body,envelope,state,created_at,conv,lid,pid,reply_to,kind,status,origin) VALUES(?,'peer/host','old answer','{"from":"own/host"}','delivered',1,?,?,?,?,'answer','done','agent:fixture')`, result, conv, protocol.NewID(), pid, parent)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = s.db.Exec(answeredConversationStatusSchema); e != nil {
			t.Fatal(e)
		}
		var due int
		if e = s.db.QueryRow(`SELECT status_due FROM inbox WHERE id=?`, id).Scan(&due); e != nil || due != map[bool]int{true: 1, false: 0}[change == "exact"] {
			t.Fatalf("%s due=%d %v", change, due, e)
		}
		if change == "exact" {
			success = id
			continue
		}
		if _, e = s.db.Exec(`DELETE FROM inbox`); e != nil {
			t.Fatal(e)
		}
		if _, e = s.db.Exec(`DELETE FROM outbox`); e != nil {
			t.Fatal(e)
		}
	}
	if e = s.db.Close(); e != nil {
		t.Fatal(e)
	}
	s, e = openStore(p)
	if e != nil {
		t.Fatal(e)
	} // the appended step is already recorded
	var due int
	if e = s.db.QueryRow(`SELECT status_due FROM inbox WHERE id=?`, success).Scan(&due); e != nil || due != 1 {
		t.Fatalf("restart repeated migration: %d %v", due, e)
	}
}
