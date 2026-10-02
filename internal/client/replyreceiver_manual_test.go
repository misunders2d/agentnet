package client

import (
	"encoding/json"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestReplyReceiverNamedHumanReplyWait(t *testing.T) {
	stub := installAgentStub(t)
	w := newWorld(t, "")
	stop := runAgent(t, w.bob)
	waitNamedAgentCaps(t, w.bob)
	stop()
	selected, e := w.alice.CreateLocalAgent("selected return", Responder{Harness: "agentstub", Dir: stub.dir})
	if e != nil {
		t.Fatal(e)
	}
	named, e := w.bob.CreateLocalAgent("remote named", Responder{Harness: "agentstub", Dir: stub.dir})
	if e != nil {
		t.Fatal(e)
	}
	receiver := ReplyReceiver{Kind: "managed_agent", AgentID: selected.ID, Instructions: "continue authorized local work", Mode: envelope.KindQuestion}
	for _, kind := range []string{envelope.KindQuestion, envelope.KindTask} {
		request, e := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: kind, Body: "original named request", Target: &envelope.Target{Address: w.bob.Address, Fingerprint: w.bob.Self().Fingerprint(), AgentID: named.ID}, ReplyReceiver: &receiver})
		if e != nil {
			t.Fatal(e)
		}
		var raw string
		if e = w.alice.store.db.QueryRow(`SELECT envelope FROM outbox WHERE id=?`, request.ID).Scan(&raw); e != nil {
			t.Fatal(e)
		}
		var original envelope.Envelope
		if e = json.Unmarshal([]byte(raw), &original); e != nil {
			t.Fatal(e)
		}
		if e = w.bob.verifyAndStore(tctx(t), original); e != nil {
			t.Fatal(e)
		}
		reply, e := w.bob.ReplyWait(tctx(t), request.ID, "human takeover reply", 0)
		if e != nil {
			t.Fatal(e)
		}
		if e = w.bob.store.db.QueryRow(`SELECT envelope FROM outbox WHERE id=?`, reply.ID).Scan(&raw); e != nil {
			t.Fatal(e)
		}
		var sealed envelope.Envelope
		if e = json.Unmarshal([]byte(raw), &sealed); e != nil {
			t.Fatal(e)
		}
		in, e := envelope.Open(sealed, w.alice.id, w.alice.Address, w.bob.Self())
		if e != nil {
			t.Fatal(e)
		}
		if in.Kind != replyKind(kind) || in.AgentID != "" {
			t.Fatalf("human producer altered: %+v", in)
		}
		for range 2 {
			if e = w.alice.verifyAndStore(tctx(t), sealed); e != nil {
				t.Fatal(e)
			}
		}
		var count int
		if e = w.alice.store.db.QueryRow(`SELECT count(*) FROM reply_receiver_inputs WHERE inbox_id=?`, reply.ID).Scan(&count); e != nil || count != 1 {
			t.Fatalf("human reply did not bind once: %d %v", count, e)
		}
		for _, bad := range []string{"agent", "ref", "key"} {
			badIn := in
			badIn.ID = protocol.NewID()
			from := w.bob
			switch bad {
			case "agent":
				badIn.AgentID = protocol.NewID()
			case "ref":
				badIn.ReplyTo = protocol.NewID()
			case "key":
				id, e := identity.Generate()
				if e != nil {
					t.Fatal(e)
				}
				from = &Agent{Address: w.bob.Address, id: id}
			}
			if e = w.alice.verifyAndStore(tctx(t), receiverDirect(t, from, w.alice, badIn)); e != nil {
				t.Fatal(e)
			}
			if e = w.alice.store.db.QueryRow(`SELECT count(*) FROM reply_receiver_inputs WHERE inbox_id=?`, badIn.ID).Scan(&count); e != nil || count != 0 {
				t.Fatalf("wrong %s bound: %d %v", bad, count, e)
			}
		}
	}
	if stub.runs() != 0 {
		t.Fatal("fixture invoked model")
	}
}
