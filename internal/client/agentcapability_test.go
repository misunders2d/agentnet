package client

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestNamedAgentOfflineQueueRechecksCapability(t *testing.T) {
	for _, capable := range []bool{false, true} {
		name := "older-peer-refused"
		if capable {
			name = "capable-peer-delivered"
		}
		t.Run(name, func(t *testing.T) {
			w := newWorld(t, "")
			runAgent(t, w.bob)
			label, device, _ := protocol.SplitAddress(w.bob.Address)
			var prof protocol.Profile
			eventually(t, "bob session", func() bool {
				return w.alice.hub.do(tctx(t), "GET", "/v1/agents/"+label+"/"+device+"/profile", nil, &prof) == nil && len(prof.Sessions) == 1
			})
			// Explicitly model either generation; the current program reads agi1.
			caps := slices.Clone(ownCaps)
			if !capable {
				caps = without(caps, protocol.CapAgentIdentity)
			}
			record := protocol.CapsRecord{Address: w.bob.Address, Session: prof.Sessions[0], Caps: caps, TS: time.Now().Unix() + 100}
			record.Sign(w.bob.id.Sign)
			if err := w.bob.hub.do(tctx(t), "PUT", "/v1/caps", record, nil); err != nil {
				t.Fatal(err)
			}

			// Lose both preflight and delivery profile reads, retaining the exact
			// encrypted request durably without pretending capability is known.
			fault := injectFaults(w.alice)
			fault.add("GET", "/profile", 2, false)
			res, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindQuestion, Body: "queued named request", Target: &envelope.Target{Address: w.bob.Address, Fingerprint: w.bob.Self().Fingerprint(), AgentID: protocol.NewID()}})
			if err != nil || res.State != stateQueued {
				t.Fatalf("offline queue: %+v %v", res, err)
			}
			var requirement string
			if err = w.alice.store.db.QueryRow("SELECT required_cap FROM outbox WHERE id=?", res.ID).Scan(&requirement); err != nil || requirement != protocol.CapAgentIdentity {
				t.Fatalf("stored requirement: %q %v", requirement, err)
			}
			if inboxCount(t, w.bob, "1=1") != 0 {
				t.Fatal("unverified capability delivered")
			}
			if err = w.alice.FlushOutbox(tctx(t)); err != nil {
				t.Fatal(err)
			}
			if capable {
				eventually(t, "named request delivered once", func() bool { return inboxCount(t, w.bob, "1=1") == 1 })
			} else {
				s, _, _, err := w.alice.store.outboxState(res.ID)
				if err != nil || s != stateFailed || inboxCount(t, w.bob, "1=1") != 0 {
					t.Fatalf("older peer downgrade: %s %v", s, err)
				}
			}
		})
	}
}

func TestNamedAgentHistoryRequirementPersists(t *testing.T) {
	s, err := openStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	identity := protocol.NewID()
	original := envelope.Inner{ID: protocol.NewID(), LID: protocol.NewID(), From: "host/agent", Kind: envelope.KindAnswer, ReplyTo: protocol.NewID(), AgentID: identity, PID: protocol.NewID()}
	h := itemOf(original, "claimed-key", time.Now().UnixMilli())
	if h.inner("conv").AgentID != identity {
		t.Fatal("history lost executor identity")
	}
	body, _ := json.Marshal(h)
	copy := outCopy{env: envelope.Envelope{ID: protocol.NewID(), To: "person/phone"}, in: envelope.Inner{Conv: "conv", LID: protocol.NewID(), Kind: envelope.KindMessage, Sub: envelope.SubHistory, Body: string(body)}, state: stateQueued}
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = insertCopies(tx, []outCopy{copy}); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var stored, requirement string
	if err = s.db.QueryRow("SELECT body,required_cap FROM outbox WHERE id=?", copy.env.ID).Scan(&stored, &requirement); err != nil || stored != "" || requirement != protocol.CapAgentIdentity {
		t.Fatalf("encrypted-only history gate: body=%q requirement=%q err=%v", stored, requirement, err)
	}
	invite, _ := json.Marshal(protocol.ParticipationEvent{Host: &protocol.ParticipationHost{AgentID: identity}})
	if agentRequirement(envelope.Inner{Sub: envelope.SubEvent, Body: string(invite)}) != protocol.CapAgentIdentity {
		t.Fatal("named invite lacks compatibility gate")
	}
	if agentRequirement(envelope.Inner{Kind: envelope.KindMessage, Body: "ordinary"}) != "" {
		t.Fatal("legacy turn gained requirement")
	}
}

func TestNamedAgentStatusUsesExactHostAndRequester(t *testing.T) {
	w := newWorld(t, "")
	conv, request := "synthetic-conversation", protocol.NewID()
	target := &envelope.Target{Address: w.bob.Address, Fingerprint: w.bob.Self().Fingerprint(), AgentID: protocol.NewID()}
	in := envelope.Inner{ID: request, From: w.alice.Address, To: w.bob.Address, Kind: envelope.KindQuestion, Target: target}
	if err := w.alice.store.addOutbox(envelope.Envelope{ID: request, From: in.From, To: in.To}, in, "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := w.alice.store.db.Exec("UPDATE outbox SET conv=?,lid=?,kind=? WHERE id=?", conv, request, in.Kind, request); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(envelope.Status{State: "running"})
	status := envelope.Inner{Conv: conv, Body: string(body), Ref: &envelope.Ref{ID: request, Fingerprint: w.alice.Self().Fingerprint()}}
	if ok, why := w.alice.statusAllowed(status, w.bob.Address, w.bob.Self().Fingerprint()); !ok {
		t.Fatal(why)
	}
	if ok, _ := w.alice.statusAllowed(status, w.bob.Address, w.alice.Self().Fingerprint()); ok {
		t.Fatal("changed host key accepted")
	}
	status.Ref.Fingerprint = w.bob.Self().Fingerprint()
	if ok, _ := w.alice.statusAllowed(status, w.bob.Address, w.bob.Self().Fingerprint()); ok {
		t.Fatal("wrong requester key accepted")
	}
}

func TestNamedConversationWaitsForRequiredCapability(t *testing.T) {
	stub := installAgentStub(t)
	w := newWorld(t, "")
	stopAlice := runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	conv := newDM(t, w.alice, w.bob)
	waitNamedAgentCaps(t, w.bob)
	agent, err := w.bob.CreateLocalAgent("Capability fixture", Responder{Harness: "agentstub", Dir: stub.dir})
	if err != nil {
		t.Fatal(err)
	}
	if err = w.bob.PublishAgentCatalog(tctx(t)); err != nil {
		t.Fatal(err)
	}
	part, err := w.alice.InviteNamedAgent(tctx(t), conv, w.bob.Address, agent.ID, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "named capability fixture invitation", func() bool { return stateAt(t, w.bob, part.PID).State == PartInvited })
	if _, err = w.bob.AcceptParticipation(tctx(t), part.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "named capability fixture accepted", func() bool { return stateAt(t, w.alice, part.PID).Claimable() })
	var profile protocol.Profile
	label, device, _ := protocol.SplitAddress(w.bob.Address)
	if err := w.alice.hub.do(tctx(t), "GET", "/v1/agents/"+label+"/"+device+"/profile", nil, &profile); err != nil || len(profile.Sessions) != 1 {
		t.Fatalf("synthetic session: %+v %v", profile.Sessions, err)
	}
	oldCaps := without(ownCaps, protocol.CapAgentIdentity)
	oldRecord := protocol.CapsRecord{Address: w.bob.Address, Session: profile.Sessions[0], Caps: oldCaps, TS: time.Now().Unix() + 100}
	oldRecord.Sign(w.bob.id.Sign)
	if err := w.bob.hub.do(tctx(t), "PUT", "/v1/caps", oldRecord, nil); err != nil {
		t.Fatal(err)
	}
	stopAlice() // deterministic inspection of the durable outgoing copy
	m := ConvOutgoing{Kind: envelope.KindQuestion, Body: "arrives after participation", PID: part.PID, Target: &envelope.Target{Address: w.bob.Address, Fingerprint: w.bob.Self().Fingerprint(), AgentID: agent.ID}}
	sent, err := w.alice.SendConv(tctx(t), conv, m)
	if err != nil || sent.State != stateConvWaiting {
		t.Fatalf("old-cap conversation: %+v %v", sent, err)
	}
	features, err := w.alice.relayFeatures(tctx(t))
	if err != nil {
		t.Fatal(err)
	}
	w.alice.releaseConv(tctx(t), features)
	if state, _, _, err := w.alice.store.outboxState(sent.ID); err != nil || state != stateConvWaiting {
		t.Fatalf("released without named support: %s %v", state, err)
	}
	record := protocol.CapsRecord{Address: w.bob.Address, Session: profile.Sessions[0], Caps: slices.Clone(ownCaps), TS: oldRecord.TS + 1}
	record.Sign(w.bob.id.Sign)
	if err = w.bob.hub.do(tctx(t), "PUT", "/v1/caps", record, nil); err != nil {
		t.Fatal(err)
	}
	w.alice.releaseConv(tctx(t), features)
	if state, _, _, err := w.alice.store.outboxState(sent.ID); err != nil || state != stateQueued {
		t.Fatalf("not released after signed capability: %s %v", state, err)
	}
}
