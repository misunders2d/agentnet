package client

import (
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
)

// referenceTopicHead is chatTopicHead as it was: the whole history decides.
func referenceTopicHead(t *testing.T, a *Agent, conv, topic string) string {
	t.Helper()
	msgs, err := a.ConversationMessages(conv)
	if err != nil {
		t.Fatal(err)
	}
	assigned := ChatTopicAssignments(msgs)
	g := []ConvMessage{}
	for _, m := range msgs {
		if assigned[m.LID] == topic && m.Sub == "" && m.TopicEvent == nil {
			g = append(g, m)
		}
	}
	sortChatTurns(g)
	if len(g) == 0 {
		return ""
	}
	return g[len(g)-1].LID
}

// checkTopicParity compares the bounded lookups with ChatTopicAssignments
// over the original turns, for every turn and every id that names it.
func checkTopicParity(t *testing.T, a *Agent, conv, phase string) {
	t.Helper()
	msgs, err := a.ConversationMessages(conv)
	if err != nil {
		t.Fatal(err)
	}
	originals := []ConvMessage{}
	for _, m := range msgs {
		if !envelope.TopicOrganization(m.TopicEvent) {
			originals = append(originals, m)
		}
	}
	want := ChatTopicAssignments(originals)
	topics := map[string]bool{}
	for _, m := range msgs {
		refs := []string{m.LID, m.ID}
		for _, c := range m.Copies {
			refs = append(refs, c.ID)
		}
		for _, ref := range refs {
			got, err := a.outgoingTopic(conv, "", ref)
			if err != nil {
				t.Fatal(err)
			}
			if exp := want[m.LID]; m.Sub == "" && !envelope.TopicOrganization(m.TopicEvent) && got != exp {
				t.Fatalf("%s: reply to %s (%q) inherits %q, want %q", phase, ref, m.Body, got, exp)
			}
		}
		if want[m.LID] != "" {
			topics[want[m.LID]] = true
		}
	}
	for topic := range topics {
		got, err := a.chatTopicHead(conv, topic)
		if err != nil {
			t.Fatal(err)
		}
		if exp := referenceTopicHead(t, a, conv, topic); got != exp {
			t.Fatalf("%s: head of %s is %s, want %s", phase, topic, got, exp)
		}
	}
}

// S6c: a reply's topic and a topic's latest turn come from bounded lookups
// that agree with the whole-history assignment: replies to own and received
// turns, by logical or copy id, chains through a turn whose sender named no
// topic, and promotion (which, like moves, only the whole history decides).
func TestTopicLookupsMatchWholeHistory(t *testing.T) {
	w, conv, _ := dmFiles(t)
	send := func(a *Agent, out ConvOutgoing) ConvSent {
		t.Helper()
		s, err := a.SendConv(tctx(t), conv, out)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	main := send(w.alice, ConvOutgoing{Body: "main"})
	seed := send(w.alice, ConvOutgoing{Body: "seed", Topic: "new"})
	topic := convMsgByID(t, w.alice, conv, seed.ID).Topic
	inTopic := send(w.alice, ConvOutgoing{Body: "in topic", Topic: topic})
	reply := send(w.alice, ConvOutgoing{Body: "reply by copy id", ReplyTo: inTopic.ID})
	// An older sender named no topic on its reply: the chain decides.
	legacy := send(w.alice, ConvOutgoing{Body: "legacy reply", ReplyTo: reply.LID})
	if _, err := w.alice.store.db.Exec(`UPDATE outbox SET topic=NULL WHERE lid=?`, legacy.LID); err != nil {
		t.Fatal(err)
	}
	send(w.alice, ConvOutgoing{Body: "reply to legacy", ReplyTo: legacy.LID})
	fromBob := send(w.bob, ConvOutgoing{Body: "bob in topic", Topic: topic})
	eventually(t, "bob's turn here", func() bool { return humanBodyCount(t, w.alice, conv, "bob in topic") == 1 })
	send(w.alice, ConvOutgoing{Body: "reply to bob", ReplyTo: fromBob.LID})
	send(w.alice, ConvOutgoing{Body: "main reply", ReplyTo: main.LID})
	checkTopicParity(t, w.alice, conv, "plain")

	// A new message to the topic replies to its latest turn.
	next := send(w.alice, ConvOutgoing{Body: "next in topic", Topic: topic})
	if got := convMsgByID(t, w.alice, conv, next.ID).ReplyTo; got == "" {
		t.Fatal("a topic message without a reply continues no chain")
	}

	// Promotion: the main turn becomes a topic of its own.
	if _, err := w.alice.ChangeChatTopic(tctx(t), conv, main.LID, "create", "", 0); err != nil {
		t.Fatal(err)
	}
	send(w.alice, ConvOutgoing{Body: "after promotion", ReplyTo: main.LID})
	checkTopicParity(t, w.alice, conv, "promoted")
}
