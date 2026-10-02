package client

import (
	"encoding/json"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Executing a human request cannot turn its local executor stamp into a
// claimed wire author, either in the current view or exported history.
func TestNamedAgentExecutedRequestAuthorship(t *testing.T) {
	for _, external := range []bool{false, true} {
		name := "member linked history"
		if external {
			name = "external visitor"
		}
		t.Run(name, func(t *testing.T) {
			var w *world
			var host *Agent
			var conv string
			var stub *agentStub
			var record protocol.AgentRecord
			if external {
				var records []protocol.AgentRecord
				w, host, conv, _, stub, records, _ = externalAgentWorld(t)
				record = records[0]
			} else {
				stub = installAgentStub(t)
				w, conv, _, _ = agentWorld(t)
				host = w.bob
				for _, a := range []*Agent{w.alice, host} {
					fakeNotify(a)
					waitNamedAgentCaps(t, a)
				}
				var err error
				record, err = host.CreateLocalAgent("Builder", Responder{Harness: "agentstub", Dir: stub.dir})
				if err != nil {
					t.Fatal(err)
				}
				if err = host.PublishAgentCatalog(tctx(t)); err != nil {
					t.Fatal(err)
				}
			}
			p, err := w.alice.InviteNamedAgent(tctx(t), conv, host.Address, record.ID, nil, nil, "")
			if err != nil {
				t.Fatal(err)
			}
			eventually(t, "named invitation", func() bool { return stateAt(t, host, p.PID).State == PartInvited })
			if _, err = host.AcceptParticipation(tctx(t), p.PID); err != nil {
				t.Fatal(err)
			}
			eventually(t, "named acceptance", func() bool { return stateAt(t, w.alice, p.PID).Claimable() })
			requests, replies, stamps := map[string]ConvSent{}, map[string]ConvMessage{}, map[string]string{}
			for _, kind := range []string{envelope.KindQuestion, envelope.KindTask} {
				request, err := w.alice.AskAgent(tctx(t), p.PID, kind, "human authored "+kind)
				if err != nil {
					t.Fatal(err)
				}
				if kind == envelope.KindTask {
					waitState(t, host, request.ID, stateAwaiting)
					if stub.runs() != 1 {
						t.Fatal("task bypassed explicit acceptance")
					}
					if err = host.Accept(request.ID); err != nil {
						t.Fatal(err)
					}
				}
				reply := replyAt(t, w.alice, conv, request.ID)
				var stampID, before, after string
				if err = host.store.db.QueryRow(`SELECT agent_id,executor FROM inbox WHERE id=?`, request.ID).Scan(&stampID, &before); err != nil {
					t.Fatal(err)
				}
				var stamp ExecutorStamp
				if json.Unmarshal([]byte(before), &stamp) != nil || stamp.AgentID != record.ID || stampID != record.ID {
					t.Fatalf("missing immutable executor stamp %q", before)
				}
				m, n := convMsg(t, host, conv, func(m ConvMessage) bool { return m.ID == request.ID })
				if n != 1 || m.AgentID != "" || m.From != w.alice.Address || m.Kind != kind || m.PID != p.PID || m.Target == nil || m.Target.AgentID != record.ID || m.Target.Address != host.Address || m.Target.Fingerprint != host.Self().Fingerprint() {
					t.Fatalf("executed human request misattributed %+v", m)
				}
				if err = host.store.db.QueryRow(`SELECT executor FROM inbox WHERE id=?`, request.ID).Scan(&after); err != nil || after != before {
					t.Fatalf("projection changed immutable executor %v", err)
				}
				if reply.AgentID != record.ID || reply.From != host.Address || reply.PID != p.PID {
					t.Fatalf("reply author lost %+v", reply)
				}
				requests[kind], replies[kind], stamps[request.ID] = request, reply, before
			}
			if external {
				return // outside host still has no sibling room-history authority
			}
			phone, awaited, _ := linkPhone(t, host, "executed-history-phone")
			fakeNotify(phone)
			link := pendingLink(t, host)
			if err = host.DecideLink(tctx(t), link.ID, true); err != nil {
				t.Fatal(err)
			}
			if out := <-awaited; out.err != nil {
				t.Fatal(out.err)
			}
			runAgent(t, phone)
			waitNamedAgentCaps(t, phone)
			eventually(t, "executed requests and replies in linked history", func() bool {
				msgs, err := phone.ConversationMessages(conv)
				if err != nil {
					return false
				}
				found := 0
				for _, m := range msgs {
					for kind, request := range requests {
						if m.LID == request.LID {
							if !m.History || m.Job != "" || m.SyncedFrom != host.Address || m.AgentID != "" || m.From != w.alice.Address || m.Kind != kind || m.PID != p.PID || m.Target == nil || m.Target.AgentID != record.ID || m.Target.Address != host.Address || m.Target.Fingerprint != host.Self().Fingerprint() {
								t.Fatalf("exported human request misattributed %+v", m)
							}
							found++
						}
						if m.LID == replies[kind].LID {
							if !m.History || m.Job != "" || m.SyncedFrom != host.Address || m.AgentID != record.ID || m.From != host.Address || m.PID != p.PID || m.Kind != replies[kind].Kind {
								t.Fatalf("exported reply author lost %+v", m)
							}
							found++
						}
					}
				}
				return found == 4
			})
			for id, before := range stamps {
				var after, stampID string
				if err = host.store.db.QueryRow(`SELECT executor,agent_id FROM inbox WHERE id=?`, id).Scan(&after, &stampID); err != nil || after != before || stampID != record.ID {
					t.Fatalf("history export changed executor %v", err)
				}
			}
			if stub.runs() != 2 {
				t.Fatal("linked history reran executor")
			}
		})
	}
}
