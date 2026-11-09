package client

import (
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// An agent's needs_human result is its chat message on the request: it is
// unread like any other arrival (badge), travels to the person's devices by
// read sync, and reading it clears it there too (MEL-693). A Stop that won
// raises nothing.
func TestNeedsHumanResultIsUnreadAttention(t *testing.T) {
	w, phone, conv, _ := historyCatchupFixture(t, 0)
	a := w.alice
	_, root := rootOf(t, w.bob, conv)
	ask := func(body string) string {
		env := craft(t, w.bob, a, envelope.Inner{Kind: envelope.KindQuestion, Conv: conv, Root: root, LID: protocol.NewID(), Body: body, Origin: envelope.OriginUI})
		if err := a.verifyAndStore(tctx(t), env); err != nil {
			t.Fatal(err)
		}
		if err := a.MarkRead([]string{env.ID}); err != nil {
			t.Fatal(err)
		}
		if attentionMessage(t, a, conv, env.ID).Unread {
			t.Fatal("fixture: the read request is still unread")
		}
		if _, err := a.store.db.Exec(`UPDATE inbox SET state=?, attempts=1 WHERE id=?`, stateRunning, env.ID); err != nil {
			t.Fatal(err)
		}
		return env.ID
	}
	id := ask("Which carrier?")
	_, mark, err := a.UnreadDirtySince(0)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.endJob(id, stateNeedHuman, "Which carrier should I book?", 1); err != nil {
		t.Fatal(err)
	}
	m := attentionMessage(t, a, conv, id)
	if !m.Unread || m.Attention == nil {
		t.Fatalf("needs_human result is not unread attention: unread=%v attention=%v", m.Unread, m.Attention)
	}
	keys, _, err := a.UnreadDirtySince(mark)
	if err != nil {
		t.Fatal(err)
	}
	dirty := false
	for _, k := range keys {
		dirty = dirty || k == "c:"+conv
	}
	if !dirty {
		t.Fatalf("badge recount not marked for the chat: %v", keys)
	}
	if _, err := a.syncReadMarks(); err != nil {
		t.Fatal(err)
	}
	var carried int
	if err := a.store.db.QueryRow(`SELECT count(*) FROM attention_mark_copies WHERE recipient_fp=? AND conv=? AND lid=? AND revision=?`,
		phone.Self().Fingerprint(), conv, m.LID, m.Attention.Rev).Scan(&carried); err != nil || carried != 1 {
		t.Fatalf("the unread generation is not carried to the person's other device: %d %v", carried, err)
	}
	if err := a.MarkReadWithAttention([]string{id}, map[string]protocol.AttentionGeneration{id: *m.Attention}); err != nil {
		t.Fatal(err)
	}
	if attentionMessage(t, a, conv, id).Unread {
		t.Fatal("reading the needs_human result did not clear it")
	}

	stopped := ask("Book it?")
	if _, err := a.store.db.Exec(`UPDATE inbox SET state=? WHERE id=?`, stateCancelReq, stopped); err != nil {
		t.Fatal(err)
	}
	if err := a.endJob(stopped, stateNeedHuman, "Which date?", 1); err != nil {
		t.Fatal(err)
	}
	if attentionMessage(t, a, conv, stopped).Unread {
		t.Fatal("a Stop that won still raised needs_human attention")
	}
}
