package client

import (
	"errors"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func waitNamedAgentCaps(t *testing.T, host *Agent) {
	t.Helper()
	label, name, _ := protocol.SplitAddress(host.Address)
	var profile protocol.Profile
	eventually(t, "published named-agent capability "+host.Address, func() bool {
		err := host.hub.do(tctx(t), "GET", "/v1/agents/"+label+"/"+name+"/profile", nil, &profile)
		return err == nil && profile.Supports(host.Address, host.Self().SignKey, protocol.CapAgentIdentity)
	})
}

func TestNamedAgentEncryptedConversationAndLinkedHistory(t *testing.T) {
	st := installAgentStub(t)
	w, conv, lids, _ := agentWorld(t)
	fakeNotify(w.alice)
	fakeNotify(w.bob)
	waitNamedAgentCaps(t, w.alice)
	waitNamedAgentCaps(t, w.bob)
	record, err := w.bob.CreateLocalAgent("Builder", Responder{Harness: "agentstub", Dir: st.dir})
	if err != nil {
		t.Fatal(err)
	}
	if err = w.bob.PublishAgentCatalog(tctx(t)); err != nil {
		t.Fatal(err)
	}
	foreign, err := w.alice.CreateLocalAgent("Other host", Responder{Harness: "agentstub", Dir: st.dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.alice.InviteNamedAgent(tctx(t), conv, w.bob.Address, foreign.ID, lids[:2], nil, "foreign"); !errors.Is(err, ErrUnknownAgent) {
		t.Fatalf("foreign catalog ID accepted: %v", err)
	}
	invited, err := w.alice.InviteNamedAgent(tctx(t), conv, w.bob.Address, record.ID, lids[:2], nil, "selected granted history")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "named host invitation", func() bool {
		info := stateAt(t, w.bob, invited.PID)
		return info.State == PartInvited && info.AgentID == record.ID
	})
	if _, err = w.bob.AcceptParticipation(tctx(t), invited.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "named participation accepted", func() bool {
		info := stateAt(t, w.alice, invited.PID)
		return info.Claimable() && info.AgentID == record.ID
	})
	question, err := w.alice.AskAgent(tctx(t), invited.PID, envelope.KindQuestion, "what failed for this selected executor?")
	if err != nil {
		t.Fatal(err)
	}
	answer := replyAt(t, w.alice, conv, question.ID)
	requestMessage, count := convMsg(t, w.bob, conv, func(m ConvMessage) bool { return m.ID == question.ID })
	if count != 1 || requestMessage.Target == nil || requestMessage.Target.AgentID != record.ID || requestMessage.Target.Address != w.bob.Address || requestMessage.Target.Fingerprint != w.bob.Self().Fingerprint() {
		t.Fatalf("received selected target DTO %+v", requestMessage)
	}
	hostAnswer := replyAt(t, w.bob, conv, question.ID)
	for _, m := range []ConvMessage{answer, hostAnswer} {
		if m.AgentID != record.ID || m.PID != invited.PID || m.From != w.bob.Address || m.Kind != envelope.KindAnswer || m.Key != w.bob.Self().Fingerprint() {
			t.Fatalf("named answer attribution %+v", m)
		}
	}
	if st.runs() != 1 {
		t.Fatalf("runs %d", st.runs())
	}
	prompt := st.last()
	for _, want := range []string{"deploy failed at step 3", "logs are in the ticket", "what failed for this selected executor?"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("grant missing %q: %s", want, prompt)
		}
	}
	if strings.Contains(prompt, "lunch") {
		t.Fatalf("ungranted history reached harness: %s", prompt)
	}
	if r, err := w.bob.Responder(); err != nil || r != nil {
		t.Fatalf("default changed %+v %v", r, err)
	}

	// Faulty but authenticated devices may not substitute another identity or
	// claim an answer from the non-host. Craft still signs/encrypts normally.
	_, root, _, _ := w.bob.store.conversation(conv)
	for _, in := range []envelope.Inner{
		{Kind: envelope.KindQuestion, Body: "wrong ID", Conv: conv, Root: root, LID: protocol.NewID(), PID: invited.PID, Target: &envelope.Target{Address: w.bob.Address, Fingerprint: w.bob.Self().Fingerprint(), AgentID: foreign.ID}},
		{Kind: envelope.KindAnswer, Body: "wrong host", Conv: conv, Root: root, LID: protocol.NewID(), PID: invited.PID, ReplyTo: question.ID, AgentID: record.ID},
	} {
		forged := craft(t, w.alice, w.bob, in)
		if err = w.bob.accept(tctx(t), forged); err != nil {
			t.Fatal(err)
		}
		var reason string
		if err := w.bob.store.db.QueryRow(`SELECT reason FROM quarantine WHERE id=?`, forged.ID).Scan(&reason); err != nil {
			t.Fatal(err)
		}
		if reason != reasonInvalid {
			t.Fatalf("forged identity reason %q", reason)
		}
		if inboxCount(t, w.bob, `id=?`, forged.ID) != 0 {
			t.Fatal("forgery admitted")
		}
	}
	if st.runs() != 1 {
		t.Fatal("forged identity executed")
	}

	phone, awaited, _ := linkPhone(t, w.alice, "phone")
	fakeNotify(phone)
	request := pendingLink(t, w.alice)
	if err = w.alice.DecideLink(tctx(t), request.ID, true); err != nil {
		t.Fatal(err)
	}
	if out := <-awaited; out.err != nil {
		t.Fatal(out.err)
	}
	runAgent(t, phone)
	waitNamedAgentCaps(t, phone)
	eventually(t, "named answer linked history", func() bool {
		msgs, err := phone.ConversationMessages(conv)
		if err != nil {
			return false
		}
		for _, m := range msgs {
			if m.LID == answer.LID {
				return m.AgentID == record.ID && m.PID == invited.PID && m.History && m.Job == "" && m.SyncedFrom == w.alice.Address
			}
		}
		return false
	})
	if st.runs() != 1 {
		t.Fatal("linked history reran executor")
	}
}
