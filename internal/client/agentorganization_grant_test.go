package client

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// All identities, signed events, original envelopes and file data belong to
// this local fixture. No model or installed client state participates.
func TestAgentOrganizationScopeOnlySelectedGrant(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		name := "new"
		if legacy {
			name = "retained-seven"
		}
		t.Run(name, func(t *testing.T) { testAgentOrganizationScopeOnlySelectedGrant(t, legacy) })
	}
}
func testAgentOrganizationScopeOnlySelectedGrant(t *testing.T, legacy bool) {
	stub := installAgentStub(t)
	w, reader, packet, stops := groupTurnsFixture(t)
	conv := packet.State.Conv
	for _, a := range []*Agent{w.alice, w.bob, reader} {
		addCapSuccessor(t, a, protocol.CapAgentOrganization)
	}
	file := filepath.Join(t.TempDir(), "original.txt")
	if err := os.WriteFile(file, []byte("SELECTED_ORIGINAL_BYTES"), 0600); err != nil {
		t.Fatal(err)
	}
	selected, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "Before the invitation", Files: []OutgoingFile{{Path: file}}})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "host and reader retain exact earlier original", func() bool {
		for _, a := range []*Agent{w.bob, reader} {
			if _, n := convMsg(t, a, conv, func(m ConvMessage) bool { return m.LID == selected.LID }); n != 1 {
				return false
			}
		}
		return true
	})
	unselected, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "UNSELECTED_HISTORY"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "reader retains unselected original", func() bool {
		_, n := convMsg(t, reader, conv, func(m ConvMessage) bool { return m.LID == unselected.LID })
		return n == 1
	})
	p, err := w.alice.InviteAgent(tctx(t), conv, w.bob.Address, []string{selected.LID}, nil, "PRIVATE_INVITATION_NOTE")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "signed invitation at host", func() bool { return stateAt(t, w.bob, p.PID).State == PartInvited })
	if _, err = w.bob.AcceptParticipation(tctx(t), p.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "scope and exact acceptance at inviter and reader", func() bool {
		return stateAt(t, w.alice, p.PID).Claimable() && stateAt(t, reader, p.PID).Claimable()
	})
	// Model the existing public scope+accept carrier at a late reader. The
	// original signed invitation remains at its author and host; this reader
	// holds only the already signed public projection and host acceptance.
	if _, err = reader.store.db.Exec(`DELETE FROM participation_events WHERE conv=? AND pid=? AND type=?`, conv, p.PID, protocol.EventInvite); err != nil {
		t.Fatal(err)
	}
	hostPart, readerPart := stateAt(t, w.bob, p.PID), stateAt(t, reader, p.PID)
	if len(hostPart.Grant) != 1 || hostPart.Note != "PRIVATE_INVITATION_NOTE" {
		t.Fatalf("host lost complete signed invitation: %+v", hostPart)
	}
	if readerPart.Scope == "" || len(readerPart.Grant) != 0 || readerPart.Note != "" || len(readerPart.TaskKeys) != 0 {
		t.Fatalf("fixture is not a scope-only reader: %+v", readerPart)
	}
	if err = w.bob.Approve(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	request, err := w.alice.AskAgent(tctx(t), p.PID, envelope.KindQuestion, "Move selected discussion and close the destination")
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, request.ID, stateAgentWaiting)
	j := claimAt(t, w.bob, request.ID)
	setResponder(t, w.bob, "agentstub", stub.dir, time.Minute)
	c, err := w.bob.agentContext(hostPart, j.ID, agentContextBytes)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.bob.captureAgentContext(j, c); err != nil {
		t.Fatal(err)
	}
	var attempt int
	if err = w.bob.store.db.QueryRow(`SELECT attempts FROM inbox WHERE id=?`, j.ID).Scan(&attempt); err != nil {
		t.Fatal(err)
	}
	if legacy {
		stops[w.alice]()
		stops[reader]()
	}
	sent, err := w.bob.OrganizeAgentTopic(tctx(t), j.ID, attempt, TopicOrganizationRequest{IDs: []string{selected.LID, request.LID}, New: true, Title: "Selected discussion"})
	if err != nil {
		t.Fatal(err)
	}
	row, n := convMsg(t, w.bob, conv, func(m ConvMessage) bool { return m.LID == sent.LID })
	if n != 1 {
		t.Fatal("host move missing")
	}
	in := envelope.Inner{V: envelope.Version2, Conv: conv, Kind: envelope.KindMessage, Topic: row.Topic, TopicEvent: row.TopicEvent, PID: p.PID, AgentID: hostPart.AgentID, Origin: envelope.OriginAgentOrganization}
	// A host that did not author the invitation cannot mint inviter disclosure.
	beforeHost := count(t, w.bob, "participation_events WHERE type='share'")
	w.bob.discloseOrganizationGrants(tctx(t))
	if count(t, w.bob, "participation_events WHERE type='share'") != beforeHost {
		t.Fatal("host minted inviter evidence")
	}
	ref := protocol.GrantRef{LID: selected.LID, Fingerprint: w.alice.Self().Fingerprint()}
	if err := agentOrganizationRefScope(reader.store.db, conv, ref, readerPart, reader.Self().Fingerprint(), row.TopicEvent.Request); !errors.Is(err, errOrganizationGrantProof) {
		t.Fatalf("missing grant did not wait for signer proof: %v", err)
	}
	var held []envelope.Envelope
	var forged, unselectedEnvelope envelope.Envelope
	if legacy {
		var raw []byte
		if err = w.bob.store.db.QueryRow(`SELECT envelope FROM outbox WHERE conv=? AND lid=? AND recipient=? LIMIT 1`, conv, sent.LID, reader.Address).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var originalEnvelope envelope.Envelope
		if err = json.Unmarshal(raw, &originalEnvelope); err != nil {
			t.Fatal(err)
		}
		opened, err := envelope.Open(originalEnvelope, reader.id, reader.Address, w.bob.Self())
		if err != nil {
			t.Fatal(err)
		}
		recipient, err := reader.Self().Recipient()
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 7; i++ {
			copy := opened
			copy.ID = protocol.NewID()
			env, err := envelope.Seal(copy, w.bob.id.Sign, recipient)
			if err != nil {
				t.Fatal(err)
			}
			held = append(held, env)
			if err = reader.store.holdAsDiagnostic(env, reasonInvalid, "selected original was not admitted to this agent's scope"); err != nil {
				t.Fatal(err)
			}
		}
		// A genuinely signed host assertion about unselected history is
		// still invalid, even after the legitimate selected share arrives.
		raw, _ = json.Marshal(opened)
		var unselectedCopy envelope.Inner
		if err = json.Unmarshal(raw, &unselectedCopy); err != nil {
			t.Fatal(err)
		}
		unselectedCopy.ID = protocol.NewID()
		unselectedCopy.TopicEvent.Moves = append(unselectedCopy.TopicEvent.Moves, envelope.TopicMove{LID: unselected.LID, Author: w.alice.Self().Fingerprint(), Hash: strings.Repeat("e", 64)})
		unselectedEnvelope, err = envelope.Seal(unselectedCopy, w.bob.id.Sign, recipient)
		if err != nil {
			t.Fatal(err)
		}
		if err = reader.store.holdAsDiagnostic(unselectedEnvelope, reasonInvalid, "selected original was not admitted to this agent's scope"); err != nil {
			t.Fatal(err)
		}
		forged = held[0]
		forged.ID = protocol.NewID()
		if err = reader.store.holdAsDiagnostic(forged, reasonInvalid, "envelope verification failed"); err != nil {
			t.Fatal(err)
		}
		if _, err = reader.store.db.Exec(`DELETE FROM config WHERE k='organization-selected-grant-proof-v1'`); err != nil {
			t.Fatal(err)
		}
		// A valid legacy copy arriving before current context must remain
		// eligible; only normal verified context arrival wakes its recovery.
		if _, err = reader.store.db.Exec(`DELETE FROM group_context WHERE conv=?`, conv); err != nil {
			t.Fatal(err)
		}
		if err = reader.recoverOrganizationGrantProof(); err != nil {
			t.Fatal(err)
		}
		var complete bool
		if err = reader.store.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM config WHERE k='organization-selected-grant-proof-v1')`).Scan(&complete); err != nil || complete {
			t.Fatalf("missing context consumed recovery: %t %v", complete, err)
		}
		for _, env := range held {
			if heldReason(t, reader, env.ID) != reasonInvalid {
				t.Fatal("copy replayed without context")
			}
		}
		if err = reader.AcceptGroupContext(tctx(t), packet); err != nil {
			t.Fatal(err)
		}
		if err = reader.recoverOrganizationGrantProof(); err != nil {
			t.Fatal(err)
		}
		for _, env := range held {
			if heldReason(t, reader, env.ID) != reasonInvalid {
				t.Fatal("retained organization replayed before its signed grant")
			}
		}
		if heldReason(t, reader, forged.ID) != reasonInvalid {
			t.Fatal("forged invalid envelope was replayed")
		}
		runAgent(t, reader)
		runAgent(t, w.alice)
	}
	eventually(t, "original inviter automatically discloses exact selected grant", func() bool { return len(stateAt(t, reader, p.PID).Grant) == 1 })
	if reason, err := reader.topicOrganizationAdmission(reader.store.db, in, w.bob.Address, w.bob.Self().Fingerprint(), false); err != nil {
		t.Fatalf("scope-only reader rejected existing selected grant (%s): %v", reason, err)
	}
	events, err := reader.store.participationEvents(conv, p.PID)
	if err != nil {
		t.Fatal(err)
	}
	var disclosure protocol.ParticipationEvent
	for _, event := range events {
		if event.Type == protocol.EventShare {
			disclosure = event
		}
	}
	if disclosure.Type == "" || disclosure.Author.Address != w.alice.Address || disclosure.Author.Fingerprint != w.alice.Self().Fingerprint() || disclosure.Prev != p.Invite || len(disclosure.Grant) != 1 || disclosure.Grant[0] != ref || disclosure.Note != "" || len(disclosure.TaskKeys) != 0 || disclosure.Verify(w.alice.Self().SignKey) != nil {
		t.Fatal("disclosure lost exact signer/subset/privacy")
	}
	originalEvents, err := w.alice.store.participationEvents(conv, p.PID)
	if err != nil {
		t.Fatal(err)
	}
	var originalInvite protocol.ParticipationEvent
	for _, event := range originalEvents {
		if event.Hash() == p.Invite {
			originalInvite = event
		}
	}
	scope := protocol.ScopeOf(originalInvite, disclosure.TS)
	raw, _ := json.Marshal(scope)
	if strings.Contains(string(raw), "PRIVATE_INVITATION_NOTE") || scope.Grant != nil || scope.TaskKeys != nil {
		t.Fatal("public scope privacy widened")
	}
	validRaw, _ := json.Marshal(disclosure)
	validInner := envelope.Inner{Kind: envelope.KindMessage, From: w.alice.Address, Conv: conv, PID: p.PID, Body: string(validRaw), Sub: envelope.SubEvent}
	if _, err := checkParticipationEvent(validInner, w.alice.Self().Fingerprint(), w.alice.Self().SignKey); err != nil {
		t.Fatalf("real signed disclosure rejected: %v", err)
	}
	forgedShare := disclosure
	forgedShare.Sign(w.bob.id.Sign)
	raw, _ = json.Marshal(forgedShare)
	validInner.Body = string(raw)
	if _, err := checkParticipationEvent(validInner, w.alice.Self().Fingerprint(), w.alice.Self().SignKey); err == nil {
		t.Fatal("forged inviter share accepted")
	}
	current := stateAt(t, reader, p.PID)
	if err := agentOrganizationRefScope(reader.store.db, conv, protocol.GrantRef{LID: unselected.LID, Fingerprint: w.alice.Self().Fingerprint()}, current, reader.Self().Fingerprint(), row.TopicEvent.Request); err == nil {
		t.Fatal("unselected earlier original admitted")
	}
	stale := disclosure
	stale.Author.GroupAdmission = strings.Repeat("f", 64)
	stale.Sign(w.alice.id.Sign)
	members, err := membersIn(reader.store.db, conv)
	if err != nil {
		t.Fatal(err)
	}
	if valid, err := members.verifyInviteEpoch(reader.store.db, stale); err != nil || valid {
		t.Fatalf("stale inviter admission counted: %t %v", valid, err)
	}
	eventually(t, "receiver converges exact move", func() bool {
		ms, e := reader.ConversationMessages(conv)
		return e == nil && ChatTopicAssignments(ms)[selected.LID] == row.Topic
	})
	if legacy {
		eventually(t, "all exact retained copies converge without duplicate record", func() bool {
			for _, env := range held {
				if heldReason(t, reader, env.ID) != "" {
					return false
				}
			}
			return true
		})
		_, copies := convMsg(t, reader, conv, func(m ConvMessage) bool { return m.LID == sent.LID })
		if copies != 1 {
			t.Fatalf("logical move duplicated %d times", copies)
		}
		if heldReason(t, reader, forged.ID) != reasonInvalid {
			t.Fatal("forged envelope left quarantine")
		}
		if heldReason(t, reader, unselectedEnvelope.ID) != reasonInvalid {
			t.Fatal("signed unselected organization left invalid quarantine")
		}

	}
	// Unrelated signed invitations cannot reconstruct participation authority
	// after this move's exact subset already has a counted share.
	for i := 0; i < 48; i++ {
		extra := originalInvite
		extra.PID = protocol.NewID()
		extra.Sign(w.alice.id.Sign)
		raw, _ := json.Marshal(extra)
		if err := extra.Verify(w.alice.Self().SignKey); err != nil {
			t.Fatal(err)
		}
		if err := w.alice.store.addParticipationEvent(extra, raw); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		if checks := w.alice.discloseOrganizationGrants(tctx(t)); checks != 0 {
			t.Fatalf("no-op wake reconstructed %d participations", checks)
		}
	}
	t.Log("cost check: 49 authored invitations, one already-disclosed move, 3 unrelated wakes, 0 participation reconstructions")
	// The existing explicit topic action is bound to the requested destination.
	// Automatic reply placement deliberately preserves the original signed topic.
	if _, err = w.bob.ChangeChatTopic(tctx(t), conv, row.Topic, "done", "", 0); err != nil {
		t.Fatal(err)
	}
	eventually(t, "requested destination Done converges", func() bool {
		topics, e := reader.ChatTopics(conv)
		if e != nil {
			return false
		}
		for _, topic := range topics {
			if topic.ID == row.Topic {
				return topic.State == TopicDone
			}
		}
		return false
	})
	w.bob.finishAgent(tctx(t), j, &Responder{Harness: "agentstub", Dir: stub.dir}, envelope.StatusDone, "Done at the requested destination\nemotion: calm")
	original, n := convMsg(t, reader, conv, func(m ConvMessage) bool { return m.LID == selected.LID })
	if n != 1 || original.Body != "Before the invitation" || original.Topic != "" || len(original.Attachments) != 1 {
		raw, _ := json.Marshal(original)
		t.Fatalf("immutable original changed: %s", raw)
	}
	if stub.runs() != 0 {
		t.Fatal("fixture unexpectedly launched a responder")
	}
}
