package client

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// An invited helper sees new conversation turns, including another helper's
// feedback, without receiving any unselected pre-invitation history.
func TestDMLiveAgentContext(t *testing.T) {
	for _, tc := range []struct {
		name     string
		external bool
		topic    bool
	}{{"member", false, false}, {"member_topic", false, true}, {"external", true, false}, {"external_topic", true, true}} {
		t.Run(tc.name, func(t *testing.T) {
			external := tc.external
			var w *world
			var host *Agent
			var conv string
			var lids []string
			var stub *agentStub
			var records []protocol.AgentRecord
			if external {
				w, host, conv, lids, stub, records, _ = externalAgentWorld(t)
			} else {
				stub = installAgentStub(t)
				w, conv, lids, _ = agentWorld(t)
				host = w.bob
				setResponder(t, host, "agentstub", stub.dir, time.Minute)
			}
			var p ParticipationInfo
			var err error
			var scope *string
			if tc.topic {
				main := ""
				scope = &main
			}
			if scope != nil {
				agentID := ""
				if external {
					agentID = records[0].ID
				}
				p, err = w.alice.InviteAgentInScope(tctx(t), conv, host.Address, agentID, lids[:1], nil, "read new feedback", scope)
			} else if external {
				p, err = w.alice.InviteNamedAgent(tctx(t), conv, host.Address, records[0].ID, lids[:1], nil, "read new feedback")
			} else {
				p, err = w.alice.InviteAgent(tctx(t), conv, host.Address, lids[:1], nil, "read new feedback")
			}
			if err != nil {
				t.Fatal(err)
			}
			eventually(t, "helper invitation", func() bool { return pollState(t, host, p.PID).State == PartInvited })
			if _, err = host.AcceptParticipation(tctx(t), p.PID); err != nil {
				t.Fatal(err)
			}
			eventually(t, "helper accepted", func() bool { return stateAt(t, w.alice, p.PID).Claimable() })
			setResponder(t, w.alice, "agentstub", stub.dir, time.Minute)
			peer, err := w.alice.InviteAgent(tctx(t), conv, w.alice.Address, nil, nil, "peer reviewer")
			if err != nil {
				t.Fatal(err)
			}
			eventually(t, "peer accepted", func() bool { return stateAt(t, w.alice, peer.PID).Claimable() })
			feedback, err := w.alice.SendAgentMessage(tctx(t), peer.PID, "", "DAVID_AGENT_FEEDBACK: simplify the opening")
			if err != nil {
				t.Fatal(err)
			}
			human, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "NEW_HUMAN_FEEDBACK: keep the example"})
			if err != nil {
				t.Fatal(err)
			}
			_, err = w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "ANOTHER_TOPIC_FEEDBACK", Topic: protocol.NewID()})
			if err != nil {
				t.Fatal(err)
			}
			eventually(t, "live turns arrive at helper host", func() bool {
				msgs, _ := host.ConversationMessages(conv)
				seen := 0
				for _, m := range msgs {
					if m.LID == feedback.LID || m.LID == human.LID {
						seen++
					}
				}
				return seen == 2
			})
			peerAsk, err := w.alice.AskAgent(tctx(t), peer.PID, envelope.KindQuestion, "PEER_REVIEW_REQUEST")
			if err != nil {
				t.Fatal(err)
			}
			peerAnswer := replyAt(t, w.alice, conv, peerAsk.ID)
			eventually(t, "peer answer arrives at helper host", func() bool {
				msgs, _ := host.ConversationMessages(conv)
				for _, m := range msgs {
					if m.LID == peerAnswer.LID {
						return true
					}
				}
				return false
			})
			var nativeRequest ConvSent
			var nativeReply ConvMessage
			check := func(label string, retained bool) {
				t.Helper()
				c, err := host.ParticipationContext(p.PID, 0)
				if err != nil {
					t.Fatal(err)
				}
				text := strings.Join(c.lines, "\n")
				wants := []string{"deploy failed at step 3", "DAVID_AGENT_FEEDBACK", "NEW_HUMAN_FEEDBACK"}
				answer, request := peerAnswer, peerAsk
				if retained && !external {
					wants = []string{"deploy failed at step 3", "PROVEN_LOCAL_FEEDBACK"}
					answer, request = nativeReply, nativeRequest
					if strings.Contains(text, "DAVID_AGENT_FEEDBACK") || strings.Contains(text, "NEW_HUMAN_FEEDBACK") || strings.Contains(text, peerAnswer.LID) {
						t.Fatalf("%s inferred unbound incoming order across stores:\n%s", label, text)
					}
				}
				for _, want := range wants {
					if !strings.Contains(text, want) {
						t.Fatalf("%s context lacks %q:\n%s", label, want, text)
					}
				}
				if strings.Contains(text, "logs are in the ticket") || strings.Contains(text, "lunch") {
					t.Fatalf("%s exposed unselected earlier history:\n%s", label, text)
				}
				if tc.topic && strings.Contains(text, "ANOTHER_TOPIC_FEEDBACK") {
					t.Fatalf("%s exposed another topic:\n%s", label, text)
				}
				foundAnswer, foundRequest := false, false
				for _, m := range c.Messages {
					foundAnswer = foundAnswer || m.LID == answer.LID && m.VerifiedAgent && m.PID == peer.PID
					foundRequest = foundRequest || m.LID == request.LID && m.PID == peer.PID && m.Target != nil
				}
				if !foundAnswer {
					t.Fatalf("%s lacks the verified peer's actual answer:\n%s", label, text)
				}
				if !foundRequest {
					t.Fatalf("%s lacks the peer's new request:\n%s", label, text)
				}
			}
			check("new", false)
			// All four newly indexed cases reach a genuine native harness.
			q, err := w.alice.AskAgent(tctx(t), p.PID, envelope.KindQuestion, "use the indexed peer feedback")
			if err != nil {
				t.Fatal(err)
			}
			replyAt(t, w.alice, conv, q.ID)
			if prompt := stub.last(); !strings.Contains(prompt, "DAVID_AGENT_FEEDBACK") || !strings.Contains(prompt, "NEW_HUMAN_FEEDBACK") {
				t.Fatalf("new native harness did not receive live feedback:\n%s", prompt)
			}
			if !external {
				if err = w.alice.Approve(host.Address); err != nil {
					t.Fatal(err)
				}
				nativeRequest, err = host.AskAgent(tctx(t), peer.PID, envelope.KindQuestion, "PROVEN_NATIVE_PEER_REQUEST")
				if err != nil {
					t.Fatal(err)
				}
				nativeReply = replyAt(t, host, conv, nativeRequest.ID)
				if _, err = host.SendConv(tctx(t), conv, ConvOutgoing{Body: "PROVEN_LOCAL_FEEDBACK"}); err != nil {
					t.Fatal(err)
				}
			}
			// Existing invitations recover from the exact native
			// acceptance record, even if the old client never indexed live refs.
			if _, err = host.store.db.Exec(`DELETE FROM room_context WHERE conv=? AND pid=?`, conv, p.PID); err != nil {
				t.Fatal(err)
			}
			check("retained invitation", true)
			q, err = w.alice.AskAgent(tctx(t), p.PID, envelope.KindQuestion, "use the peer feedback")
			if err != nil {
				t.Fatal(err)
			}
			replyAt(t, w.alice, conv, q.ID)
			prompt := stub.last()
			if external && (!strings.Contains(prompt, "DAVID_AGENT_FEEDBACK") || !strings.Contains(prompt, "NEW_HUMAN_FEEDBACK")) || !external && (!strings.Contains(prompt, "PROVEN_LOCAL_FEEDBACK") || !strings.Contains(prompt, nativeReply.LID)) {
				t.Fatalf("retained native harness did not receive proven feedback:\n%s", prompt)
			}
			if _, err = w.alice.DismissParticipation(tctx(t), p.PID); err != nil {
				t.Fatal(err)
			}
			eventually(t, "helper dismissed", func() bool { return pollState(t, host, p.PID).State == PartDismissed })
			if _, err = host.ParticipationContext(p.PID, 0); err == nil {
				t.Fatal("dismissed helper still exposes context")
			}
			if _, err = w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "AFTER_DISMISSAL_UNSHARED"}); err != nil {
				t.Fatal(err)
			}
			if !external {
				next, err := w.alice.InviteAgentInScope(tctx(t), conv, host.Address, "", nil, nil, "new invitation", scope)
				if err != nil {
					t.Fatal(err)
				}
				eventually(t, "new invitation", func() bool { return pollState(t, host, next.PID).State == PartInvited })
				if _, err = host.AcceptParticipation(tctx(t), next.PID); err != nil {
					t.Fatal(err)
				}
				c, err := host.ParticipationContext(next.PID, 0)
				if err != nil || strings.Contains(strings.Join(c.lines, "\n"), "FEEDBACK") || strings.Contains(strings.Join(c.lines, "\n"), "AFTER_DISMISSAL") {
					t.Fatalf("new invitation exposed previous live history: %+v %v", c, err)
				}
			}
		})
	}
}

func TestGroupLiveAgentContext(t *testing.T) {
	stub := installAgentStub(t)
	w, carol, packet, _ := groupTurnsFixture(t)
	conv := packet.State.Conv
	setResponder(t, w.bob, "agentstub", stub.dir, time.Minute)
	setResponder(t, w.alice, "agentstub", stub.dir, time.Minute)
	if _, err := carol.SendConv(tctx(t), conv, ConvOutgoing{Body: "EARLIER_UNSELECTED_GROUP_HISTORY"}); err != nil {
		t.Fatal(err)
	}
	reader := p6Member(t, w.alice, w.bob, conv)
	peer := p6Member(t, w.alice, w.alice, conv)
	eventually(t, "third member knows helper", func() bool { return stateAt(t, carol, reader.PID).Claimable() })
	feedback, err := w.alice.SendAgentMessage(tctx(t), peer.PID, "", "GROUP_PEER_FEEDBACK: simplify the opening")
	if err != nil {
		t.Fatal(err)
	}
	human, err := carol.SendConv(tctx(t), conv, ConvOutgoing{Body: "GROUP_HUMAN_FEEDBACK: keep the example"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "group live context captured", func() bool {
		c, err := w.bob.ParticipationContext(reader.PID, 0)
		if err != nil {
			return false
		}
		seen := 0
		for _, m := range c.Messages {
			if m.LID == feedback.LID || m.LID == human.LID {
				seen++
			}
		}
		return seen == 2
	})
	if err := w.bob.Approve(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	peerRequest, err := w.alice.AskAgent(tctx(t), peer.PID, envelope.KindQuestion, "GROUP_NATIVE_PEER_REVIEW")
	if err != nil {
		t.Fatal(err)
	}
	peerAnswer := replyAt(t, w.alice, conv, peerRequest.ID)
	eventually(t, "group sees native peer answer", func() bool {
		c, err := w.bob.ParticipationContext(reader.PID, 0)
		if err != nil {
			return false
		}
		for _, m := range c.Messages {
			if m.LID == peerAnswer.LID && m.VerifiedAgent && m.PID == peer.PID {
				return true
			}
		}
		return false
	})
	q, err := w.alice.AskAgent(tctx(t), reader.PID, envelope.KindQuestion, "use the group's feedback")
	if err != nil {
		t.Fatal(err)
	}
	replyAt(t, w.alice, conv, q.ID)
	prompt := stub.last()
	if !strings.Contains(prompt, "GROUP_PEER_FEEDBACK") || !strings.Contains(prompt, "GROUP_HUMAN_FEEDBACK") || strings.Contains(prompt, "EARLIER_UNSELECTED_GROUP_HISTORY") {
		t.Fatalf("group native context:\n%s", prompt)
	}
}

func TestDMLiveExternalPeerContext(t *testing.T) {
	w, readerHost, conv, _, stub, records, _ := externalAgentWorld(t)
	peerHost := mustJoin(t, filepath.Join(t.TempDir(), "dave"), w.aliceInvites("dave"), "host")
	runAgent(t, peerHost)
	persons(t, peerHost)
	fakeNotify(peerHost)
	waitNamedAgentCaps(t, peerHost)
	record, err := peerHost.CreateLocalAgent("Peer", Responder{Harness: "agentstub", Dir: stub.dir})
	if err != nil {
		t.Fatal(err)
	}
	if err := peerHost.PublishAgentCatalog(tctx(t)); err != nil {
		t.Fatal(err)
	}
	invite := func(host *Agent, id string) ParticipationInfo {
		t.Helper()
		p, err := w.alice.InviteNamedAgent(tctx(t), conv, host.Address, id, nil, nil, "review together")
		if err != nil {
			t.Fatal(err)
		}
		eventually(t, "outside invitation", func() bool { return pollState(t, host, p.PID).State == PartInvited })
		if _, err := host.AcceptParticipation(tctx(t), p.PID); err != nil {
			t.Fatal(err)
		}
		eventually(t, "outside helper accepted", func() bool { return stateAt(t, w.alice, p.PID).Claimable() })
		return p
	}
	reader := invite(readerHost, records[0].ID)
	peer := invite(peerHost, record.ID)
	q, err := w.alice.AskAgent(tctx(t), peer.PID, envelope.KindQuestion, "EXTERNAL_PEER_REVIEW")
	if err != nil {
		t.Fatal(err)
	}
	answer := replyAt(t, w.alice, conv, q.ID)
	eventually(t, "other outside helper sees exact peer answer", func() bool {
		c, err := readerHost.ParticipationContext(reader.PID, 0)
		if err != nil {
			return false
		}
		for _, m := range c.Messages {
			if m.LID == answer.LID && m.VerifiedAgent && m.PID == peer.PID {
				return true
			}
		}
		return false
	})
	// An already queued captured-audience reply still rechecks its parent.
	faults := injectFaults(peerHost)
	faults.add("POST", "/v1/messages", 3, false)
	queued, err := peerHost.SendConv(tctx(t), conv, ConvOutgoing{Kind: envelope.KindAnswer, PID: peer.PID, AgentID: record.ID, ReplyTo: q.ID, Body: "QUEUED_PEER_ANSWER", Origin: envelope.OriginAgentPrefix + "stub", Emotion: "neutral"})
	if err != nil || queued.State != stateQueued {
		t.Fatalf("queued reply: %+v %v", queued, err)
	}
	if _, err := peerHost.store.db.Exec(`UPDATE outbox SET reply_to=? WHERE lid=?`, protocol.NewID(), queued.LID); err != nil {
		t.Fatal(err)
	}
	if err := peerHost.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	var held, wrapped int
	if err := peerHost.store.db.QueryRow(`SELECT count(*), count(human) FROM outbox WHERE lid=? AND state=?`, queued.LID, stateNotDelivered).Scan(&held, &wrapped); err != nil || held != 3 || wrapped != 3 {
		t.Fatalf("captured queued replies lost request validation: held=%d wrapped=%d %v", held, wrapped, err)
	}
}

// Owner, 2026-11-11: a message held here as a copy (an own agent's reply,
// an own other device's message) that its owner chose to share when
// bringing in an own agent is that agent's context, not a left-out copy.
func TestGrantedCopyIsOwnHelperContext(t *testing.T) {
	w, conv, lids, _ := agentWorld(t)
	if _, err := w.bob.store.db.Exec(`UPDATE inbox SET replica=1 WHERE conv=? AND lid=?`, conv, lids[0]); err != nil {
		t.Fatal(err)
	}
	p, err := w.bob.InviteAgent(tctx(t), conv, w.bob.Address, []string{lids[0]}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	c, err := w.bob.ParticipationContext(p.PID, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range c.Messages {
		found = found || m.LID == lids[0]
	}
	if !found || c.Missing != 0 || c.Replicas != 0 {
		t.Fatalf("the shared copy is not context: found=%v missing=%d replicas=%d", found, c.Missing, c.Replicas)
	}
}

// The owner's journey (2026-11-11): a message typed on the person's phone is
// held on their laptop as a copy. Sharing it from the laptop when bringing in
// another host's agent gives that agent the message.
func TestSharedOwnDeviceCopyReachesInvitedAgent(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	phone, awaited, _ := linkPhone(t, w.alice, "phone")
	if err := w.alice.DecideLink(tctx(t), pendingLink(t, w.alice).ID, true); err != nil {
		t.Fatal(err)
	}
	if out := <-awaited; out.err != nil {
		t.Fatal(out.err)
	}
	runAgent(t, phone)
	conv := newDM(t, w.bob, w.alice)
	if _, err := w.bob.SendConv(tctx(t), conv, ConvOutgoing{Body: "peer turn"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the peer turn on the phone", func() bool { return len(convBodies(t, phone, conv)) == 1 })
	if _, err := phone.SendConv(tctx(t), conv, ConvOutgoing{Body: "SHARED_FROM_PHONE"}); err != nil {
		t.Fatal(err)
	}
	var copy ConvMessage
	eventually(t, "the laptop holds the phone's message", func() bool {
		m, n := convMsg(t, w.alice, conv, func(m ConvMessage) bool { return m.Body == "SHARED_FROM_PHONE" })
		copy = m
		return n == 1
	})
	if !copy.Replica {
		t.Logf("fixture note: the phone's message is not held as a replica here (Via %q)", copy.Via)
	}
	p, err := w.alice.InviteAgent(tctx(t), conv, w.bob.Address, []string{copy.LID}, nil, "please look")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob to see the invite", func() bool { return pollState(t, w.bob, p.PID).State == PartInvited })
	if _, err := w.bob.AcceptParticipation(tctx(t), p.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the shared phone message is the agent's context", func() bool {
		c, err := w.bob.ParticipationContext(p.PID, 0)
		if err != nil || c.Missing != 0 {
			return false
		}
		for _, m := range c.Messages {
			if strings.Contains(m.Body, "SHARED_FROM_PHONE") {
				return true
			}
		}
		return false
	})
}
