package client

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// A move changes the reviewed history selection, while the original remains
// in its signed topic for replies and execution.
func TestParticipationTopicMovedHistoryInvite(t *testing.T) {
	for _, group := range []bool{false, true} {
		for _, source := range []string{"", "new"} {
			t.Run(map[bool]string{false: "DM", true: "group"}[group]+"/"+map[string]string{"": "Main", "new": "named"}[source], func(t *testing.T) {
				var w *world
				var conv string
				if group {
					var packet GroupContext
					w, _, packet, _ = groupTurnsFixture(t)
					conv = packet.State.Conv
				} else {
					w, conv, _ = dmFiles(t)
				}
				path, want := writeFile(t, t.TempDir(), "selected.txt", 40)
				selected, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "exact selected history", Topic: source, Files: []OutgoingFile{{Path: path}}})
				if err != nil {
					t.Fatal(err)
				}
				dest, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "destination", Topic: "new"})
				if err != nil {
					t.Fatal(err)
				}
				destination, _ := convMsg(t, w.alice, conv, func(m ConvMessage) bool { return m.LID == dest.LID })
				topic := destination.Topic
				review, err := w.alice.PreviewTopicOrganization(TopicOrganizationRequest{Conv: conv, IDs: []string{selected.LID}, Topic: topic})
				if err != nil {
					t.Fatal(err)
				}
				if _, err = w.alice.ApplyTopicOrganization(tctx(t), review); err != nil {
					t.Fatal(err)
				}
				p, err := w.alice.InviteAgentInScope(tctx(t), conv, w.bob.Address, "", []string{selected.LID}, nil, "reviewed moved history", &topic)
				if err != nil {
					t.Fatalf("reviewed moved history invitation: %v", err)
				}
				t.Cleanup(func() {
					if !t.Failed() {
						return
					}
					c, e := w.bob.ParticipationContext(p.PID, 0)
					t.Logf("receiver context=%+v err=%v", c, e)
					rows, e := w.alice.store.db.Query(`SELECT sub,state,error FROM outbox WHERE pid=?`, p.PID)
					if e == nil {
						defer rows.Close()
						for rows.Next() {
							var sub, state, detail string
							rows.Scan(&sub, &state, &detail)
							t.Logf("sender copy %s %s %s", sub, state, detail)
						}
					}
				})
				eventually(t, "exact moved selection reaches receiver context", func() bool {
					c, err := w.bob.ParticipationContext(p.PID, 0)
					return err == nil && c.Missing == 0 && len(c.Messages) == 1 && c.Messages[0].LID == selected.LID && len(c.Messages[0].Attachments) == 1
				})
				c, err := w.bob.ParticipationContext(p.PID, 0)
				if err != nil {
					t.Fatal(err)
				}
				m := c.Messages[0]
				// Actual signed receiver admission rejects substituted content,
				// manifests and source scope before the duplicate/history path.
				h, _, err := grantedAgentSnapshot(w.bob.store.db, stateAt(t, w.bob, p.PID), protocol.GrantRef{LID: selected.LID, Fingerprint: w.alice.Self().Fingerprint()})
				if err != nil {
					t.Fatal(err)
				}
				_, root, _, err := w.alice.store.conversation(conv)
				if err != nil {
					t.Fatal(err)
				}
				for _, bad := range []string{"text", "manifest", "scope"} {
					changed := h
					changed.Attachments = append([]envelope.Attachment{}, h.Attachments...)
					switch bad {
					case "text":
						changed.Body = "unreviewed replacement"
					case "manifest":
						changed.Attachments[0].SHA256 = strings.Repeat("b", 64)
					case "scope":
						changed.Topic = protocol.NewID()
					}
					body, _ := json.Marshal(changed)
					in := envelope.Inner{V: envelope.Version2, ID: protocol.NewID(), LID: protocol.NewID(), From: w.alice.Address, To: w.bob.Address, Conv: conv, Root: root, PID: p.PID, Sub: envelope.SubExcerpt, Replica: true, TS: time.Now().Unix(), Kind: envelope.KindMessage, Body: string(body)}
					to, e := w.bob.Self().Recipient()
					if e != nil {
						t.Fatal(e)
					}
					forged, e := envelope.Seal(in, w.alice.id.Sign, to)
					if e != nil {
						t.Fatal(e)
					}
					if e = w.bob.verifyAndStore(tctx(t), forged); e != nil {
						t.Fatal(e)
					}
					var reason string
					if e = w.bob.store.db.QueryRow(`SELECT reason FROM quarantine WHERE id=?`, in.ID).Scan(&reason); e != nil || reason != reasonInvalid {
						t.Fatalf("signed forged %s admitted: %q %v", bad, reason, e)
					}
				}
				file, _, err := w.bob.OpenFileFrom(tctx(t), m.Dir, m.ID, 0)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(readAll(t, file), want) {
					t.Fatal("selected receiver file bytes differ")
				}
				original, _ := convMsg(t, w.alice, conv, func(m ConvMessage) bool { return m.LID == selected.LID && m.ExcerptPID == "" })
				got, err := topicReference(w.alice.store.db, conv, selected.LID, w.alice.Self().Fingerprint())
				if err != nil || got != original.Topic {
					t.Fatalf("move changed original request topic: %q %v", got, err)
				}
				if err := checkParticipationTopic(w.alice.store.db, &topic, envelope.Inner{Conv: conv, Ref: &envelope.Ref{ID: selected.LID, Fingerprint: w.alice.Self().Fingerprint()}}); !errors.Is(err, errParticipationTopic) {
					t.Fatalf("moved history expanded ongoing reference authority: %v", err)
				}
				// A later display move adds no grant and does not erase the exact
				// already disclosed snapshot.
				later, e := w.alice.PreviewTopicOrganization(TopicOrganizationRequest{Conv: conv, IDs: []string{selected.LID}, Main: true})
				if e != nil {
					t.Fatal(e)
				}
				if _, e = w.alice.ApplyTopicOrganization(tctx(t), later); e != nil {
					t.Fatal(e)
				}
				retained, e := w.bob.ParticipationContext(p.PID, 0)
				if e != nil || len(retained.Messages) != 1 || retained.Messages[0].Body != h.Body {
					t.Fatalf("later display move changed disclosed context: %+v %v", retained, e)
				}
			})
		}
	}
}

func TestTopicSnapshotOversizeFailsBeforeCommit(t *testing.T) {
	w, conv, _ := dmFiles(t)
	me, _, err := w.alice.store.selfPerson(w.alice.Address)
	if err != nil {
		t.Fatal(err)
	}
	host, _, err := w.bob.store.selfPerson(w.bob.Address)
	if err != nil {
		t.Fatal(err)
	}
	topic := protocol.NewID()
	e := protocol.ParticipationEvent{V: 1, Conv: conv, PID: protocol.NewID(), Type: protocol.EventInvite, TS: time.Now().Unix(), Author: protocol.EventAuthor{Person: me.info.Person, Roster: me.info.Roster, Address: w.alice.Address, Fingerprint: w.alice.Self().Fingerprint()}, Host: &protocol.ParticipationHost{Person: host.info.Person, Address: w.bob.Address, Fingerprint: w.bob.Self().Fingerprint()}, Audience: protocol.AudienceConversation, Topic: &topic}
	for range 80 {
		ref := protocol.GrantRef{LID: protocol.NewID(), Fingerprint: w.alice.Self().Fingerprint()}
		e.Grant = append(e.Grant, ref)
		e.Snapshots = append(e.Snapshots, protocol.GrantSnapshot{LID: ref.LID, Fingerprint: ref.Fingerprint, Hash: strings.Repeat("a", 64)})
	}
	if e.Validate() != nil {
		t.Fatal("size fixture must otherwise have valid shape")
	}
	if err := w.alice.recordAndSend(tctx(t), e); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("oversize accepted: %v", err)
	}
	var effects int
	w.alice.store.db.QueryRow(`SELECT (SELECT count(*) FROM participation_events WHERE pid=?)+(SELECT count(*) FROM outbox WHERE pid=?)`, e.PID, e.PID).Scan(&effects)
	if effects != 0 {
		t.Fatal("oversize left a partial invitation or copies")
	}
}

func TestTopicSnapshotStaleCommitRefused(t *testing.T) {
	w, conv, _ := dmFiles(t)
	selected, e := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "selected source"})
	if e != nil {
		t.Fatal(e)
	}
	dest, e := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "destination", Topic: "new"})
	if e != nil {
		t.Fatal(e)
	}
	m, _ := convMsg(t, w.alice, conv, func(m ConvMessage) bool { return m.LID == dest.LID })
	topic := m.Topic
	review, e := w.alice.PreviewTopicOrganization(TopicOrganizationRequest{Conv: conv, IDs: []string{selected.LID}, Topic: topic})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = w.alice.ApplyTopicOrganization(tctx(t), review); e != nil {
		t.Fatal(e)
	}
	refs := []protocol.GrantRef{{LID: selected.LID, Fingerprint: w.alice.Self().Fingerprint()}}
	_, claim, e := w.alice.prepareGrantSnapshots(conv, &topic, refs)
	if e != nil || claim == nil {
		t.Fatal("no reviewed snapshot claim", e)
	}
	back, e := w.alice.PreviewTopicOrganization(TopicOrganizationRequest{Conv: conv, IDs: []string{selected.LID}, Main: true})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = w.alice.ApplyTopicOrganization(tctx(t), back); e != nil {
		t.Fatal(e)
	}
	tx, e := w.alice.store.db.Begin()
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	if e = claim(tx); !errors.Is(e, ErrTopicOrganizationChanged) {
		t.Fatalf("stale move review committed: %v", e)
	}
}

func TestTopicSnapshotSelfAndUnseenExternalContext(t *testing.T) {
	for _, external := range []bool{false, true} {
		t.Run(map[bool]string{false: "self", true: "unseen external"}[external], func(t *testing.T) {
			var w *world
			var host *Agent
			var conv, agent string
			if external {
				var records []protocol.AgentRecord
				w, host, conv, _, _, records, _ = externalAgentWorld(t)
				agent = records[0].ID
			} else {
				w, conv, _ = dmFiles(t)
				host = w.alice
				if err := host.SetResponder(&Responder{Harness: "claude", Dir: t.TempDir()}); err != nil {
					t.Fatal(err)
				}
			}
			file, want := writeFile(t, t.TempDir(), "selected.txt", 31)
			selected, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "exact disclosed source", Files: []OutgoingFile{{Path: file}}})
			if err != nil {
				t.Fatal(err)
			}
			topic := protocol.NewID()
			review, err := w.alice.PreviewTopicOrganization(TopicOrganizationRequest{Conv: conv, IDs: []string{selected.LID}, Topic: topic, New: true, Title: "Selected context"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = w.alice.ApplyTopicOrganization(tctx(t), review); err != nil {
				t.Fatal(err)
			}
			p, err := w.alice.InviteAgentInScope(tctx(t), conv, host.Address, agent, []string{selected.LID}, nil, "exact re-disclosure", &topic)
			if err != nil {
				t.Fatal(err)
			}
			eventually(t, "frozen context without original move", func() bool {
				c, e := host.ParticipationContext(p.PID, 0)
				return e == nil && c.Missing == 0 && len(c.Messages) == 1 && c.Messages[0].LID == selected.LID
			})
			c, err := host.ParticipationContext(p.PID, 0)
			if err != nil {
				t.Fatal(err)
			}
			m := c.Messages[0]
			if m.Topic != "" || m.Body != "exact disclosed source" || m.ExcerptPID != p.PID {
				t.Fatalf("snapshot metadata rewritten: %+v", m)
			}
			r, _, err := host.OpenFileFrom(tctx(t), m.Dir, m.ID, 0)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(readAll(t, r), want) {
				t.Fatal("snapshot selected file differs")
			}
			if !external {
				var queued int
				if err := host.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE pid=? AND sub='excerpt' AND state IN ('queued','waiting')`, p.PID).Scan(&queued); err != nil || queued != 0 {
					t.Fatalf("own local disclosure queued a relay self-send: %d %v", queued, err)
				}
			}
			if external {
				msgs, e := host.ConversationMessages(conv)
				if e != nil {
					t.Fatal(e)
				}
				for _, m := range msgs {
					if m.LID == selected.LID && m.ExcerptPID == "" || envelope.TopicOrganization(m.TopicEvent) {
						t.Fatal("external context fetched ambient original/organization")
					}
				}
			}
		})
	}
}

func TestTopicSnapshotOlderReaderCopiesWaitIndependently(t *testing.T) {
	w, old, packet, stops := groupTurnsFixture(t)
	a := w.alice
	stops[a]()
	signCapsAfter(t, old, without(without(ownCaps, protocol.CapTopicContext), protocol.CapGuestAgent))
	listMembers(t, a, map[string]bool{old.Address: true}, a.Address, w.bob.Address, old.Address)
	selected, err := a.SendConv(tctx(t), packet.State.Conv, ConvOutgoing{Body: "selected Main context"})
	if err != nil {
		t.Fatal(err)
	}
	topic := protocol.NewID()
	review, err := a.PreviewTopicOrganization(TopicOrganizationRequest{Conv: packet.State.Conv, IDs: []string{selected.LID}, Topic: topic, New: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.ApplyTopicOrganization(tctx(t), review); err != nil {
		t.Fatal(err)
	}
	p, err := a.InviteAgentInScope(tctx(t), packet.State.Conv, w.bob.Address, "", []string{selected.LID}, nil, "", &topic)
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "compatible host context despite older member", func() bool {
		c, e := w.bob.ParticipationContext(p.PID, 0)
		return e == nil && c.Missing == 0 && len(c.Messages) == 1
	})
	var id, raw string
	if err = a.store.db.QueryRow(`SELECT id,envelope FROM outbox WHERE recipient=? AND pid=? AND sub='event' AND json_array_length(body,'$.snapshots')>0`, old.Address, p.PID).Scan(&id, &raw); err != nil {
		t.Fatal(err)
	}
	if outboxState(t, a, id) != stateConvWaiting {
		t.Fatal("incompatible member's copy did not wait")
	}
	signCapsAfter(t, old, ownCaps)
	listMembers(t, a, nil, a.Address, w.bob.Address, old.Address)
	features, err := a.relayFeatures(tctx(t))
	if err != nil {
		t.Fatal(err)
	}
	a.releaseConv(tctx(t), features)
	if err = a.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "updated member receives retained invitation", func() bool { return stateAt(t, old, p.PID).Invite != "" })
	var after string
	if err = a.store.db.QueryRow(`SELECT envelope FROM outbox WHERE id=?`, id).Scan(&after); err != nil || after != raw {
		t.Fatal("reader update changed exact ciphertext", err)
	}
}
