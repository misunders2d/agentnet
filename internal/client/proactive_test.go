package client

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestProactiveAgentMessageAdmission(t *testing.T) {
	for _, mode := range []string{"dm", "external", "group", "group_guest"} {
		t.Run(mode, func(t *testing.T) {
			var inviter, host *Agent
			var readers []*Agent
			var conv, agent string
			var stop func()
			switch mode {
			case "dm":
				w, c, stopped := selfConsentWorld(t)
				stop = stopped
				inviter, host, conv, readers = w.alice, w.alice, c, []*Agent{w.bob}
			case "external":
				w, h, c, _, _, records, stopped := externalAgentWorld(t)
				stop = stopped
				inviter, host, conv, readers, agent = w.alice, h, c, []*Agent{w.alice, w.bob}, records[0].ID
			case "group", "group_guest":
				w, carol, packet, stops := groupTurnsFixture(t)
				stop = stops[w.bob]
				inviter, host, conv, readers = w.alice, w.bob, packet.State.Conv, []*Agent{w.alice, carol}
				if err := host.SetResponder(&Responder{Harness: "claude", Dir: t.TempDir()}); err != nil {
					t.Fatal(err)
				}
				if mode == "group_guest" {
					guest := proofReader(t, w, "proactive-guest")
					runAgent(t, guest)
					publishGroupFixtureCaps(t, guest, true)
					for _, a := range []*Agent{inviter, host, carol, guest} {
						roomReader(t, a)
					}
					invite, err := inviter.InviteHuman(tctx(t), conv, guest.Address, nil, "guest audience")
					if err != nil {
						t.Fatal(err)
					}
					eventually(t, "human guest invited", func() bool { return pollState(t, guest, invite.PID).State == PartInvited })
					if _, err := guest.AcceptParticipation(tctx(t), invite.PID); err != nil {
						t.Fatal(err)
					}
					eventually(t, "host knows current human guest", func() bool { return stateAt(t, host, invite.PID).HumanActive() })
					readers = append(readers, guest)
				}
			}
			topic := protocol.NewID()
			p, err := inviter.InviteAgentInScope(tctx(t), conv, host.Address, agent, nil, nil, "", &topic)
			if err != nil {
				t.Fatal(err)
			}
			eventually(t, "invitation reaches host", func() bool { return stateAt(t, host, p.PID).Invite != "" })
			if !stateAt(t, host, p.PID).Claimable() {
				if _, err := host.AcceptParticipation(tctx(t), p.PID); err != nil {
					t.Fatal(err)
				}
			}
			for _, reader := range readers {
				eventually(t, "reader knows exact acceptance", func() bool { return stateAt(t, reader, p.PID).Claimable() })
			}
			stop()
			host.stopBackgroundPosts() // queuedConv also schedules a sender outside Run
			contents := []string{"# Agent report\n", `{"ready":true}`, "<!doctype html><html><body>Agent report</body></html>"}
			names := []string{"report.md", "report.json", "report.html"}
			files := make([]OutgoingFile, len(names))
			for i, name := range names {
				path := filepath.Join(t.TempDir(), name)
				if err := os.WriteFile(path, []byte(contents[i]), 0o600); err != nil {
					t.Fatal(err)
				}
				files[i] = OutgoingFile{Path: path}
			}
			// No incoming request exists: queue using the ordinary durable sender.
			sent, err := host.SendAgentMessage(WithQueuedSend(context.Background(), protocol.NewID()), p.PID, "", "Unsolicited admitted update", files...)
			if err != nil {
				t.Fatal(err)
			}
			if sent.State != stateQueued && sent.State != stateConvWaiting {
				t.Fatalf("unproven delivery reported: %+v", sent)
			}
			if held, err := host.holdEndedOutputs(""); err != nil || held != 0 {
				t.Fatalf("ordinary agent message requires a fabricated request: %d %v", held, err)
			}
			if err := host.FlushOutbox(tctx(t)); err != nil {
				t.Fatal(err)
			}
			for _, reader := range readers {
				eventually(t, "proactive agent attributed once", func() bool {
					m, n := convMsg(t, reader, conv, func(m ConvMessage) bool { return m.LID == sent.LID })
					return n == 1 && m.VerifiedAgent && m.AgentID == agent && m.PID == p.PID && m.ReplyTo == "" && m.Topic == topic && m.Kind == envelope.KindMessage
				})
				m, _ := convMsg(t, reader, conv, func(m ConvMessage) bool { return m.LID == sent.LID })
				if len(m.Attachments) != len(files) {
					t.Fatalf("agent attachments missing: %+v", m.Attachments)
				}
				for i, name := range names {
					r, f, err := reader.OpenFileFrom(tctx(t), "in", m.ID, i)
					if err != nil {
						t.Fatal(err)
					}
					if f.Name != name || string(readAll(t, r)) != contents[i] {
						t.Fatalf("agent file %d name or bytes changed: %+v", i, f)
					}
				}
			}
			fileOnly, err := host.SendAgentMessage(WithQueuedSend(context.Background(), protocol.NewID()), p.PID, "", "", files[2])
			if err != nil {
				t.Fatal(err)
			}
			if err := host.FlushOutbox(tctx(t)); err != nil {
				t.Fatal(err)
			}
			for _, reader := range readers {
				eventually(t, "file-only agent message arrives", func() bool {
					m, n := convMsg(t, reader, conv, func(m ConvMessage) bool { return m.LID == fileOnly.LID })
					return n == 1 && m.Body == "" && m.VerifiedAgent && len(m.Attachments) == 1
				})
			}
			tooMany := make([]OutgoingFile, envelope.MaxAttachments+1)
			for i := range tooMany {
				tooMany[i] = files[0]
			}
			if _, err := host.SendAgentMessage(tctx(t), p.PID, "", "too many", tooMany...); err == nil {
				t.Fatal("agent bypassed attachment limit")
			}
			if mode == "dm" {
				if _, err := host.SendConv(tctx(t), conv, ConvOutgoing{Kind: envelope.KindMessage, PID: p.PID, Origin: "agent:proactive", Emotion: "neutral", Topic: topic, Files: files}); err == nil {
					t.Fatal("public origin marker bypassed the admitted agent-send path")
				}
			}
			if _, err := host.SendAgentMessage(tctx(t), p.PID, protocol.NewID(), "outside scope", files...); err == nil {
				t.Fatal("topic-bound agent wrote into another topic")
			}
			if host != inviter {
				if _, err := inviter.SendAgentMessage(tctx(t), p.PID, "", "wrong host", files...); err == nil {
					t.Fatal("human member impersonated another host's agent")
				}
			}
			// A queued ordinary update cannot outlive its admission.
			blocked, err := host.SendAgentMessage(WithQueuedSend(context.Background(), protocol.NewID()), p.PID, "", "Must not leave after removal", files...)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := inviter.DismissParticipation(tctx(t), p.PID); err != nil {
				t.Fatal(err)
			}
			if inviter != host {
				// Install the exact signed end atomically for this handover
				// test. Network proof refresh may wake sends while the old
				// admission still holds, before a dismissal is committed.
				events, err := inviter.store.participationEvents(conv, p.PID)
				if err != nil {
					t.Fatal(err)
				}
				for _, ev := range events {
					if ev.Type == protocol.EventDismiss {
						if err := ev.Verify(inviter.Self().SignKey); err != nil {
							t.Fatal(err)
						}
						raw, _ := json.Marshal(ev)
						if err := host.store.addParticipationEvent(ev, raw); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			if stateAt(t, host, p.PID).State != PartDismissed {
				t.Fatal("removal not admitted")
			}
			if err := host.FlushOutbox(tctx(t)); err != nil {
				t.Fatal(err)
			}
			for _, copy := range blocked.Copies {
				state, _, _, err := host.store.outboxState(copy.ID)
				if err != nil || state != stateNotDelivered {
					t.Fatalf("queued proactive message survived removal: %s %v", state, err)
				}
			}
			if _, err := host.SendAgentMessage(tctx(t), p.PID, "", "removed", files...); err == nil {
				t.Fatal("removed agent retained proactive send access")
			}
		})
	}
}

func TestProactiveAgentReaderIsolation(t *testing.T) {
	w, old, packet, stops := groupTurnsFixture(t)
	host := w.bob
	if err := host.SetResponder(&Responder{Harness: "claude", Dir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	p := p6Member(t, w.alice, host, packet.State.Conv)
	eventually(t, "old reader knows admission", func() bool { return stateAt(t, old, p.PID).Claimable() })
	stops[host]()
	host.stopBackgroundPosts()
	signCapsAfter(t, old, without(ownCaps, protocol.CapProactiveAgent))
	sent, err := host.SendAgentMessage(WithQueuedSend(context.Background(), protocol.NewID()), p.PID, "", "New ordinary agent update")
	if err != nil {
		t.Fatal(err)
	}
	releasePasses(t, host, 1)
	eventually(t, "compatible reader gets message", func() bool {
		m, n := convMsg(t, w.alice, packet.State.Conv, func(m ConvMessage) bool { return m.LID == sent.LID })
		return n == 1 && m.VerifiedAgent
	})
	var id, original string
	if err := host.store.db.QueryRow(`SELECT id,envelope FROM outbox WHERE lid=? AND recipient=?`, sent.LID, old.Address).Scan(&id, &original); err != nil {
		t.Fatal(err)
	}
	if outboxState(t, host, id) != stateConvWaiting {
		t.Fatal("older reader received unsupported agent turn")
	}
	signCapsAfter(t, old, ownCaps)
	releasePasses(t, host, 1)
	eventually(t, "upgraded reader receives exact message", func() bool {
		m, n := convMsg(t, old, packet.State.Conv, func(m ConvMessage) bool { return m.LID == sent.LID })
		return n == 1 && m.VerifiedAgent
	})
	var retried string
	if err := host.store.db.QueryRow(`SELECT envelope FROM outbox WHERE id=?`, id).Scan(&retried); err != nil || retried != original {
		t.Fatalf("capability retry changed ciphertext: %v", err)
	}
	releasePasses(t, host, 1)
	if _, n := convMsg(t, old, packet.State.Conv, func(m ConvMessage) bool { return m.LID == sent.LID }); n != 1 {
		t.Fatal("retry duplicated agent message")
	}
}
