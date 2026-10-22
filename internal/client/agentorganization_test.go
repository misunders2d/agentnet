package client

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestAgentTopicOrganizationAdmittedRun(t *testing.T) {
	for _, local := range []bool{false, true} {
		name := "foreign-original"
		if local {
			name = "local-original"
		}
		t.Run(name, func(t *testing.T) {
			stub := installAgentStub(t)
			w, conv, lids, stopBob := agentWorld(t)
			if _, err := w.alice.ChangeChatTopic(tctx(t), conv, lids[1], "create", "Earlier named topic", 0); err != nil {
				t.Fatal(err)
			}
			existingTopic := protocol.NewID()
			if _, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "Existing automatic destination", Topic: existingTopic}); err != nil {
				t.Fatal(err)
			}
			privateSource, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "Independent ordinary privacy regression source"})
			if err != nil {
				t.Fatal(err)
			}
			eventually(t, "host sees source topic", func() bool {
				messages, e := w.bob.ConversationMessages(conv)
				hasDestination, hasPrivateSource := false, false
				for _, m := range messages {
					hasDestination = hasDestination || m.Topic == existingTopic
					hasPrivateSource = hasPrivateSource || m.LID == privateSource.LID
				}
				return e == nil && hasDestination && hasPrivateSource && ChatTopicAssignments(messages)[lids[1]] == lids[1]
			})
			// Retained private names predate shared rename. They remain local
			// and must never be copied into an agent's new move notice.
			tx, err := w.bob.store.db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			if err = w.bob.recordTopicTitle(tx, conv, existingTopic, "HOST PRIVATE LABEL"); err != nil {
				tx.Rollback()
				t.Fatal(err)
			}
			if err = tx.Commit(); err != nil {
				t.Fatal(err)
			}
			// The privacy case has its own exact grant. Bob is stopped before
			// Alice's inverse events, so reuse of those moved originals would
			// intentionally carry a stale source-topic CAS on the requester.
			pid := participate(t, w, conv, []string{lids[0], lids[1], privateSource.LID}, nil)
			for _, a := range []*Agent{w.alice, w.bob} {
				addCapSuccessor(t, a, protocol.CapAgentOrganization)
			}
			asker := w.alice
			if local {
				asker = w.bob
			}
			if err := w.bob.Approve(w.alice.Address); err != nil {
				t.Fatal(err)
			}
			q, err := asker.AskAgent(tctx(t), pid, envelope.KindQuestion, "PRIVATE_REQUEST move the related selected discussion into a topic")
			if err != nil {
				t.Fatal(err)
			}
			waitState(t, w.bob, q.ID, stateAgentWaiting)
			j := claimAt(t, w.bob, q.ID)
			stopBob()
			setResponder(t, w.bob, "agentstub", stub.dir, time.Minute)
			p := stateAt(t, w.bob, pid)
			c, err := w.bob.agentContext(p, j.ID, agentContextBytes)
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
			r, err := w.bob.organizationRun(w.bob.store.db, j.ID, attempt)
			if err != nil {
				t.Fatal(err)
			}
			proof, err := w.bob.organizationRequestProof(w.bob.store.db, r)
			if err != nil {
				t.Fatal(err)
			}
			if (proof.Proof != nil) != local {
				t.Fatal("foreign original was re-signed or local fallback absent")
			}
			raw, _ := json.Marshal(proof)
			if strings.Contains(string(raw), "PRIVATE_REQUEST") {
				t.Fatal("private request body leaked in compact proof")
			}
			for _, bad := range []int{0, attempt + 1} {
				if _, err = w.bob.organizationRun(w.bob.store.db, j.ID, bad); err == nil {
					t.Fatal("another attempt obtained current authority")
				}
			}
			if _, err = w.bob.OrganizeAgentTopic(tctx(t), j.ID, attempt, TopicOrganizationRequest{IDs: []string{lids[2]}, New: true}); err == nil {
				t.Fatal("unselected ambient history became organization scope")
			}
			sent, err := w.bob.OrganizeAgentTopic(tctx(t), j.ID, attempt, TopicOrganizationRequest{IDs: lids[:2], New: true, Title: "Related discussion"})
			if err != nil {
				t.Fatal(err)
			}
			again, err := w.bob.OrganizeAgentTopic(tctx(t), j.ID, attempt, TopicOrganizationRequest{IDs: lids[:2], New: true, Title: "Related discussion"})
			if err != nil || again.LID != sent.LID {
				t.Fatalf("retry duplicated action: %v", err)
			}
			row, organizationCount := convMsg(t, w.bob, conv, func(m ConvMessage) bool { return m.LID == sent.LID })
			if organizationCount != 1 {
				t.Fatalf("organization rows=%d", organizationCount)
			}
			if row.Body != "Moved selected messages to new topic “Related discussion”." {
				t.Fatalf("agent notice names wrong destination: %q", row.Body)
			}
			if row.TopicEvent == nil || row.TopicEvent.Request == nil || row.TopicEvent.Request.LID != q.LID {
				t.Fatal("signed action lost exact request")
			}
			realm, err := w.bob.RealmID()
			if err != nil {
				t.Fatal(err)
			}
			wantLink, err := protocol.ConversationLink(realm, conv, row.Topic)
			if err != nil {
				t.Fatal(err)
			}
			if link, err := w.bob.OrganizationLink(sent); err != nil || link != wantLink {
				t.Fatalf("stored organization destination: %q, %v; want %q", link, err, wantLink)
			}
			if _, err := w.bob.OrganizationLink(ConvSent{ID: sent.ID, LID: q.LID}); err == nil {
				t.Fatal("another result obtained the destination")
			}
			in := envelope.Inner{V: envelope.Version2, Conv: conv, Kind: envelope.KindMessage, Topic: row.Topic, TopicEvent: row.TopicEvent, PID: pid, AgentID: p.AgentID, Origin: envelope.OriginAgentOrganization}
			if reason, e := w.alice.topicOrganizationAdmission(w.alice.store.db, in, w.bob.Address, w.bob.Self().Fingerprint(), false); e != nil {
				t.Fatalf("other reader rejected actual authority (%s): %v", reason, e)
			}
			eventually(t, "actual other reader projects native move", func() bool {
				messages, e := w.alice.ConversationMessages(conv)
				return e == nil && ChatTopicAssignments(messages)[lids[0]] == row.Topic
			})
			original, rowsCount := convMsg(t, w.bob, conv, func(m ConvMessage) bool { return m.LID == lids[0] })
			if rowsCount != 1 {
				t.Fatalf("original rows=%d", rowsCount)
			}
			if original.Topic != "" || original.Body != "deploy failed at step 3" {
				t.Fatal("original body/topic changed")
			}
			if stateAt(t, w.bob, pid).Topic != nil {
				t.Fatal("display move expanded future topic admission")
			}
			if _, err := w.alice.PreviewTopicOrganization(TopicOrganizationRequest{Conv: conv, IDs: []string{lids[0]}}); err == nil {
				t.Fatal("missing destination became Main")
			}
			for _, candidate := range func() []ConvMessage { ms, _ := w.alice.ConversationMessages(conv); return ms }() {
				if candidate.LID == sent.LID {
					t.Logf("undo original: key=%s claimed=%s verifiedAgent=%v pid=%s action=%s", candidate.Key, candidate.Claimed, candidate.VerifiedAgent, candidate.PID, candidate.TopicEvent.Action)
				}
			}
			undo := TopicOrganizationUndo{Conv: conv, LID: sent.LID, Author: w.bob.Self().Fingerprint(), ID: protocol.NewID()}
			restored, err := w.alice.UndoTopicOrganization(tctx(t), undo)
			if err != nil || len(restored) != 2 {
				t.Fatalf("grouped Main/named undo: %v (%d)", err, len(restored))
			}
			messages, err := w.alice.ConversationMessages(conv)
			if err != nil {
				t.Fatal(err)
			}
			assignments := ChatTopicAssignments(messages)
			if assignments[lids[0]] != "" || assignments[lids[1]] != lids[1] {
				t.Fatalf("inverse assignments: %v", assignments)
			}
			retry, err := w.alice.UndoTopicOrganization(tctx(t), undo)
			if err != nil || len(retry) != 2 || retry[0].LID != restored[0].LID || retry[1].LID != restored[1].LID {
				t.Fatalf("undo retry: %v", err)
			}
			undo.ID = protocol.NewID()
			if _, err := w.alice.UndoTopicOrganization(tctx(t), undo); err == nil {
				t.Fatal("intervening inverse did not invalidate a new undo")
			}
			second, err := w.bob.OrganizeAgentTopic(tctx(t), j.ID, attempt, TopicOrganizationRequest{IDs: lids[:2], New: true, Title: "Second move"})
			if err != nil {
				t.Fatal(err)
			}
			eventually(t, "second agent operation", func() bool {
				_, n := convMsg(t, w.alice, conv, func(m ConvMessage) bool { return m.LID == second.LID })
				return n == 1
			})
			source, _ := convMsg(t, w.alice, conv, func(m ConvMessage) bool { return m.LID == lids[1] && m.Dir == "out" })
			ref, err := w.alice.RefOf(conv, source.ID, "out")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = w.alice.Revise(tctx(t), ref, "Changed source version after the move"); err != nil {
				t.Fatal(err)
			}
			before := count(t, w.alice, "outbox")
			if _, err = w.alice.UndoTopicOrganization(tctx(t), TopicOrganizationUndo{Conv: conv, LID: second.LID, Author: w.bob.Self().Fingerprint(), ID: protocol.NewID()}); err == nil {
				t.Fatal("changed source version permitted grouped undo")
			}
			if count(t, w.alice, "outbox") != before {
				t.Fatal("failed later source group stored a partial inverse")
			}
			privateMove, e := w.bob.OrganizeAgentTopic(tctx(t), j.ID, attempt, TopicOrganizationRequest{IDs: []string{privateSource.LID}, Topic: existingTopic})
			if e != nil {
				t.Fatal(e)
			}
			privateRow, _ := convMsg(t, w.bob, conv, func(m ConvMessage) bool { return m.LID == privateMove.LID })
			if privateRow.Body != TopicOrganizationNotice(existingTopic, "") || strings.Contains(privateRow.Body, "HOST PRIVATE LABEL") {
				t.Fatalf("existing agent move leaked a private name: %q", privateRow.Body)
			}
			eventually(t, "requester sees existing destination without host private name", func() bool {
				messages, err := w.alice.ConversationMessages(conv)
				if err != nil || ChatTopicAssignments(messages)[privateSource.LID] != existingTopic {
					return false
				}
				topics, err := w.alice.ChatTopicsForMessages(conv, messages)
				if err != nil {
					return false
				}
				for _, topic := range topics {
					if topic.ID == existingTopic {
						return topic.Title != "HOST PRIVATE LABEL" && !topic.Renamed
					}
				}
				return false
			})
			mainMove, e := w.bob.OrganizeAgentTopic(tctx(t), j.ID, attempt, TopicOrganizationRequest{IDs: []string{lids[0]}, Main: true})
			if e != nil {
				t.Fatal(e)
			}
			mainRow, _ := convMsg(t, w.bob, conv, func(m ConvMessage) bool { return m.LID == mainMove.LID })
			if mainRow.Topic != "" || mainRow.Body != "Moved selected messages to Main flow." {
				t.Fatalf("agent Main destination notice=%q topic=%q", mainRow.Body, mainRow.Topic)
			}
			if _, err = w.bob.store.db.Exec(`UPDATE inbox SET state=? WHERE id=?`, stateCancelled, j.ID); err != nil {
				t.Fatal(err)
			}
			if _, err = w.bob.OrganizeAgentTopic(tctx(t), j.ID, attempt, TopicOrganizationRequest{IDs: []string{lids[0]}, New: true}); err == nil {
				t.Fatal("cancelled run initiated another move")
			}
		})
	}
}

func TestAgentOrganizationWireVectors(t *testing.T) {
	id, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	pub := id.Public("owner/host")
	request := envelope.Inner{V: envelope.Version2, Conv: strings.Repeat("a", 64), LID: protocol.NewID(), PID: protocol.NewID(), Kind: envelope.KindTask, Body: "SYNTHETIC_PRIVATE_REQUEST", Origin: envelope.OriginUI, Target: &envelope.Target{Address: pub.Address, Fingerprint: pub.Fingerprint(), AgentID: protocol.NewID()}}
	proof := envelope.TopicRequestProof{V: 1, Conv: request.Conv, LID: request.LID, PID: request.PID, Author: protocol.EventAuthor{Person: protocol.NewID(), Roster: strings.Repeat("b", 64), Address: pub.Address, Fingerprint: pub.Fingerprint()}, Target: *request.Target, Hash: contentHash(request), Kind: request.Kind}
	proof.Sign(id.Sign)
	event := envelope.TopicEvent{Action: "move", Moves: []envelope.TopicMove{{LID: protocol.NewID(), Author: pub.Fingerprint(), Hash: strings.Repeat("c", 64)}}, Request: &envelope.TopicRequest{LID: request.LID, Author: pub.Fingerprint(), Hash: proof.Hash, Proof: &proof}}
	raw, _ := json.Marshal(event)
	vector, _ := json.Marshal(map[string]any{"public": pub, "proof": proof, "canonical": string(proof.Canonical()), "request": request, "hash": proof.Hash, "event": event, "event_json": string(raw)})
	fmt.Println("AGENT_ORGANIZATION_VECTOR_JSON " + string(vector))
}

func TestAgentTopicOrganizationGroupAndOutsideSnapshot(t *testing.T) {
	for _, group := range []bool{false, true} {
		name := "outside-snapshot"
		if group {
			name = "group"
		}
		t.Run(name, func(t *testing.T) {
			var w *world
			var host *Agent
			var conv, pid, selected string
			var stub *agentStub
			if group {
				var packet GroupContext
				w, _, packet, _ = groupTurnsFixture(t)
				conv = packet.State.Conv
				host = w.bob
				stub = installAgentStub(t)
				stub.mode("sleep")
				setResponder(t, host, "agentstub", stub.dir, time.Minute)
				p := p6Member(t, w.alice, host, conv)
				pid = p.PID
				sent, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "Related ordinary [@Agent](agentnet:agent/" + pid + ") discussion"})
				if err != nil {
					t.Fatal(err)
				}
				selected = sent.LID
				if err = host.Approve(w.alice.Address); err != nil {
					t.Fatal(err)
				}
			} else {
				var lids []string
				var records []protocol.AgentRecord
				w, host, conv, lids, stub, records, _ = externalAgentWorld(t)
				stub.mode("sleep")
				selected = lids[0]
				p, err := w.alice.InviteNamedAgent(tctx(t), conv, host.Address, records[0].ID, []string{selected}, nil, "")
				if err != nil {
					t.Fatal(err)
				}
				pid = p.PID
				eventually(t, "outside invite", func() bool { return stateAt(t, host, pid).Invite != "" })
				if _, err = host.AcceptParticipation(tctx(t), pid); err != nil {
					t.Fatal(err)
				}
			}
			for _, a := range []*Agent{w.alice, w.bob, host} {
				addCapSuccessor(t, a, protocol.CapAgentOrganization)
				addCapSuccessor(t, a, protocol.CapMentionReaction)
			}
			eventually(t, "requester current scope", func() bool { return stateAt(t, w.alice, pid).Claimable() })
			q, err := w.alice.AskAgent(tctx(t), pid, envelope.KindQuestion, "Organize the related admitted discussion")
			if err != nil {
				t.Fatal(err)
			}
			waitState(t, host, q.LID, stateRunning)
			eventually(t, "native prompt supplied with exact captured context", func() bool {
				var context string
				return host.store.db.QueryRow(`SELECT coalesce(agent_context,'') FROM inbox WHERE id=? AND state=?`, q.LID, stateRunning).Scan(&context) == nil && context != "" && stub.runs() > 0
			})
			var attempt int
			if err = host.store.db.QueryRow(`SELECT attempts FROM inbox WHERE id=?`, q.LID).Scan(&attempt); err != nil {
				t.Fatal(err)
			}
			for _, invalid := range []string{`{"action":"organize","ids":["` + selected + `"]}`, `{"action":"organize","pid":"` + protocol.NewID() + `","new":true,"ids":["` + selected + `"]}`} {
				if _, err := host.codexRoomTool(tctx(t), q.LID, json.RawMessage(invalid)); err == nil {
					t.Fatal("native tool accepted missing destination or a caller-supplied agent PID")
				}
			}
			tool, err := host.codexRoomTool(tctx(t), q.LID, json.RawMessage(`{"action":"organize","new":true,"title":"Related","ids":["`+selected+`"]}`))
			if err != nil || !strings.Contains(tool, "stored") {
				t.Fatalf("native organization tool: %s %v", tool, err)
			}
			sent, err := host.OrganizeAgentTopic(tctx(t), q.LID, attempt, TopicOrganizationRequest{IDs: []string{selected}, New: true, Title: "Related"})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if t.Failed() {
					t.Logf("sent result: %+v", sent)
					rows, e := host.store.db.Query(`SELECT recipient,state,coalesce(error,'') FROM outbox WHERE lid=?`, sent.LID)
					if e != nil {
						t.Log("copy lookup", e)
					}
					if rows != nil {
						defer rows.Close()
						for rows.Next() {
							var recipient, state, detail string
							rows.Scan(&recipient, &state, &detail)
							t.Log("organization copy", recipient, state, detail)
						}
					}
					messages, _ := w.alice.ConversationMessages(conv)
					t.Log("selected display", ChatTopicAssignments(messages)[selected])
				}
			})
			eventually(t, "current full reader gets native move", func() bool {
				messages, e := w.alice.ConversationMessages(conv)
				if e != nil {
					return false
				}
				for _, m := range messages {
					if m.LID == sent.LID {
						return ChatTopicAssignments(messages)[selected] == m.Topic
					}
				}
				return false
			})
			if group {
				before := count(t, host, "inbox WHERE kind IN ('question','task')")
				if _, err = host.codexRoomTool(tctx(t), q.LID, json.RawMessage(`{"action":"react","id":"`+selected+`","author":"`+w.alice.Self().Fingerprint()+`","emoji":"👍"}`)); err != nil {
					t.Fatal(err)
				}
				if _, err = host.codexRoomTool(tctx(t), q.LID, json.RawMessage(`{"action":"send","text":"Discretionary agent update"}`)); err != nil {
					t.Fatal(err)
				}
				if count(t, host, "inbox WHERE kind IN ('question','task')") != before {
					t.Fatal("reaction fabricated work")
				}
			}
			if err = host.Cancel(q.LID); err != nil {
				t.Fatal(err)
			}
			if _, err := host.codexRoomTool(tctx(t), q.LID, json.RawMessage(`{"action":"send","text":"after cancellation"}`)); err == nil {
				t.Fatal("cancelled native run used its action tool")
			}
			if _, err = host.OrganizeAgentTopic(tctx(t), q.LID, attempt, TopicOrganizationRequest{IDs: []string{selected}, New: true}); err == nil {
				t.Fatal("cancelled current run organized again")
			}
		})
	}
}
