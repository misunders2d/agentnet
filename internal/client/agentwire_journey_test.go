package client

import (
	"os"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// This journey uses real signed/encrypted SendMessage and daemon receive;
// no inbox insertion or direct claim substitutes for admission.
func TestNamedAgentEncryptedWireJourneyABA(t *testing.T) {
	st := sessionStub(t)
	w := newWorld(t, "")
	fakeNotify(w.alice)
	fakeNotify(w.bob)
	if err := w.bob.Approve(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	a, err := w.bob.CreateLocalAgent("A", Responder{Harness: "cstyle", Dir: st.dir})
	if err != nil {
		t.Fatal(err)
	}
	b, err := w.bob.CreateLocalAgent("B", Responder{Harness: "cstyle2", Dir: st.dir})
	if err != nil {
		t.Fatal(err)
	}
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	// Use normal daemon publication; no test-only capability records.
	waitNamedAgentCaps(t, w.alice)
	waitNamedAgentCaps(t, w.bob)
	requestAuthor := func(id, agentID string) {
		t.Helper()
		var before, after, stampID string
		if err := w.bob.store.db.QueryRow(`SELECT agent_id,executor FROM inbox WHERE id=?`, id).Scan(&stampID, &before); err != nil || stampID != agentID {
			t.Fatalf("request executor stamp %q %v", stampID, err)
		}
		thread, err := w.bob.Conversation(id, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		requestFound, replyFound := false, false
		for _, m := range thread.Messages {
			if m.ID == id {
				requestFound = true
				if m.AgentID != "" || m.From != w.alice.Address || m.Target == nil || m.Target.AgentID != agentID || m.Target.Address != w.bob.Address || m.Target.Fingerprint != w.bob.Self().Fingerprint() {
					t.Fatalf("human direct request misattributed %+v", m)
				}
			}
			if m.ReplyTo == id {
				replyFound = true
				if m.AgentID != agentID || m.From != w.bob.Address {
					t.Fatalf("direct agent reply lost author %+v", m)
				}
			}
		}
		if !requestFound || !replyFound {
			t.Fatal("direct request/reply missing")
		}
		if err := w.bob.store.db.QueryRow(`SELECT executor FROM inbox WHERE id=?`, id).Scan(&after); err != nil || before != after {
			t.Fatalf("projection changed executor %v", err)
		}
	}

	var previous string
	for i, record := range []protocol.AgentRecord{a, b, a} {
		sent, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindQuestion, Body: "question for " + record.Label, ReplyTo: previous,
			Target: &envelope.Target{Address: w.bob.Address, Fingerprint: w.bob.Self().Fingerprint(), AgentID: record.ID}})
		if err != nil {
			t.Fatal(err)
		}
		var answer Message
		eventually(t, "encrypted named answer", func() bool { var ok bool; answer, ok = findReply(w.alice, sent.ID); return ok })
		if answer.Kind != envelope.KindAnswer || answer.Status != envelope.StatusDone {
			t.Fatalf("answer %d: %+v", i, answer)
		}
		var target, author, executor string
		if err = w.bob.store.db.QueryRow(`SELECT target,executor FROM inbox WHERE id=?`, sent.ID).Scan(&target, &executor); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(target, record.ID) || !strings.Contains(executor, record.ID) {
			t.Fatalf("wrong admitted target/stamp: %s %s", target, executor)
		}
		if err = w.alice.store.db.QueryRow(`SELECT agent_id FROM inbox WHERE id=?`, answer.ID).Scan(&author); err != nil || author != record.ID {
			t.Fatalf("wrong author %q %v", author, err)
		}
		requestAuthor(sent.ID, record.ID)
		if sessionOf(t, w.bob, sent.ID) != nil {
			t.Fatal("native session retained")
		}
		previous = answer.ID
	}
	if st.count() != 3 {
		t.Fatalf("A/B/A runs=%d", st.count())
	}
	log, _ := os.ReadFile(st.log)
	if strings.Count(string(log), "args=--question-mode --no-session-persistence") != 2 || strings.Count(string(log), "args=--other-flags --no-session-persistence") != 1 {
		t.Fatalf("executor sequence: %s", log)
	}
	if r, err := w.bob.Responder(); err != nil || r != nil {
		t.Fatalf("manual device default changed: %+v %v", r, err)
	}

	task, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindTask, Body: "explicitly accepted task", ReplyTo: previous,
		Target: &envelope.Target{Address: w.bob.Address, Fingerprint: w.bob.Self().Fingerprint(), AgentID: b.ID}})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, task.ID, stateAwaiting)
	if st.count() != 3 {
		t.Fatal("named task bypassed approval")
	}
	if err = w.bob.Accept(task.ID); err != nil {
		t.Fatal(err)
	}
	var result Message
	eventually(t, "accepted encrypted named result", func() bool { var ok bool; result, ok = findReply(w.alice, task.ID); return ok })
	var author string
	if result.Kind != envelope.KindResult || result.Status != envelope.StatusDone {
		t.Fatalf("task result %+v", result)
	}
	if err = w.alice.store.db.QueryRow(`SELECT agent_id FROM inbox WHERE id=?`, result.ID).Scan(&author); err != nil || author != b.ID {
		t.Fatalf("task author %q %v", author, err)
	}
	requestAuthor(task.ID, b.ID)
	if st.count() != 4 {
		t.Fatalf("exactly-once runs=%d", st.count())
	}
}
