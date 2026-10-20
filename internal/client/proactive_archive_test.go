package client

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestProactiveHistoryArchiveReaderRequirement(t *testing.T) {
	for _, proactive := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "proactive"}[proactive], func(t *testing.T) {
			w, a, phone, conv := archiveFixture(t, 1)
			_, root := rootOf(t, a, conv)
			var item HistoryItem
			if proactive {
				if err := a.SetResponder(&Responder{Harness: "claude", Dir: t.TempDir()}); err != nil {
					t.Fatal(err)
				}
				part, err := a.InviteAgent(tctx(t), conv, a.Address, nil, nil, "")
				if err != nil {
					t.Fatal(err)
				}
				sent, err := a.SendAgentMessage(WithQueuedSend(context.Background(), protocol.NewID()), part.PID, "", "Archived proactive update")
				if err != nil {
					t.Fatal(err)
				}
				a.stopBackgroundPosts()
				var raw string
				if err := a.store.db.QueryRow(`SELECT envelope FROM outbox WHERE lid=? AND recipient=? AND sub IS NULL`, sent.LID, w.bob.Address).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				var env envelope.Envelope
				if err := json.Unmarshal([]byte(raw), &env); err != nil {
					t.Fatal(err)
				}
				in, err := envelope.Open(env, w.bob.id, w.bob.Address, a.Self())
				if err != nil {
					t.Fatal(err)
				}
				item = itemOf(in, a.Self().Fingerprint(), in.TS*1000)
			} else {
				sent, err := a.SendConv(tctx(t), conv, ConvOutgoing{Body: "Archived ordinary update"})
				if err != nil {
					t.Fatal(err)
				}
				m, _ := convMsg(t, a, conv, func(m ConvMessage) bool { return m.LID == sent.LID })
				item = HistoryItem{V: 1, ID: m.ID, LID: m.LID, From: a.Address, FromKey: a.Self().Fingerprint(), TS: m.At / 1000, At: m.At, Kind: envelope.KindMessage, Body: m.Body, Origin: envelope.OriginUI}
			}
			copy, err := a.historyCopy(phone.Self(), conv, root, item)
			if err != nil {
				t.Fatal(err)
			}
			copy.state = archiveStaged
			copy.recipientFP = phone.Self().Fingerprint()
			tx, err := a.store.db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			if err := insertCopies(tx, []outCopy{copy}); err != nil {
				tx.Rollback()
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			childNeeds, err := copyNeedsOf(a.store.db, copy.env.ID, copy.env.V)
			if err != nil || childNeeds.proactive != proactive {
				t.Fatalf("history lost proactive shape: %+v %v", childNeeds, err)
			}
			if err := a.archivePack(tctx(t)); err != nil {
				t.Fatal(err)
			}
			descriptor := archiveDescriptor(t, a)
			needs, err := copyNeedsOf(a.store.db, descriptor.ID, descriptor.V)
			if err != nil {
				t.Fatal(err)
			}
			want := protocol.CapHistoryArchive
			if proactive {
				want = protocol.CapProactiveAgent
			}
			if needs.required != want {
				t.Fatalf("archive lost child requirement: %s want %s", needs.required, want)
			}
			signCapsAfter(t, phone, without(ownCaps, protocol.CapProactiveAgent))
			err = a.readerCheck(tctx(t), phone.Address, phone.Self(), needs)
			if (err != nil) != proactive {
				t.Fatalf("older reader: proactive=%v err=%v", proactive, err)
			}
			// A later local erase must not narrow the retained archive's requirement.
			if _, err := a.store.db.Exec(`UPDATE outbox SET body='' WHERE id=?`, copy.env.ID); err != nil {
				t.Fatal(err)
			}
			retained, err := copyNeedsOf(a.store.db, descriptor.ID, descriptor.V)
			if err != nil || retained.required != want {
				t.Fatalf("child erase downgraded requirement: %+v %v", retained, err)
			}
			var before string
			if err := a.store.db.QueryRow(`SELECT envelope FROM outbox WHERE id=?`, descriptor.ID).Scan(&before); err != nil {
				t.Fatal(err)
			}
			if err := a.archivePack(tctx(t)); err != nil {
				t.Fatal(err)
			}
			var after string
			if err := a.store.db.QueryRow(`SELECT envelope FROM outbox WHERE id=?`, descriptor.ID).Scan(&after); err != nil || after != before {
				t.Fatalf("upgrade resealed retained descriptor: %v", err)
			}
			// Exercise the actual handover, not only capability classification.
			if err := a.store.setOutboxState(descriptor.ID, stateQueued, "", ""); err != nil {
				t.Fatal(err)
			}
			missing := []string{protocol.CapHistoryArchive}
			if proactive {
				missing = append(missing, protocol.CapProactiveAgent)
			}
			for _, cap := range missing {
				signCapsAfter(t, phone, without(ownCaps, cap))
				result, err := a.deliver(tctx(t), descriptor, nil)
				if err != nil || result.State != stateConvWaiting || outboxState(t, a, descriptor.ID) != stateConvWaiting {
					t.Fatalf("missing %s must park descriptor: %+v %v", cap, result, err)
				}
			}
			signCapsAfter(t, phone, ownCaps)
			a.releaseConv(tctx(t), nil)
			if outboxState(t, a, descriptor.ID) != stateQueued {
				t.Fatal("signed reader update did not release descriptor")
			}
			result, err := a.deliver(tctx(t), descriptor, nil)
			if err != nil || result.State != protocol.StateCustody {
				t.Fatalf("supported reader did not take custody: %+v %v", result, err)
			}
			if err := a.store.db.QueryRow(`SELECT envelope FROM outbox WHERE id=?`, descriptor.ID).Scan(&after); err != nil || after != before {
				t.Fatalf("delivery resealed retained descriptor: %v", err)
			}
			// Expiry repacks the same staged ciphertext, whose local plaintext
			// was erased above. Inherit the prior descriptor's requirement.
			if err := a.store.setOutboxState(descriptor.ID, protocol.StateExpired, "", ""); err != nil {
				t.Fatal(err)
			}
			if err := a.archivePack(tctx(t)); err != nil {
				t.Fatal(err)
			}
			repacked := archiveDescriptor(t, a)
			repackedNeeds, err := copyNeedsOf(a.store.db, repacked.ID, repacked.V)
			if repacked.ID == descriptor.ID || err != nil || repackedNeeds.required != want {
				t.Fatalf("expired manifest downgraded retained ciphertext: %+v %v", repackedNeeds, err)
			}
			for _, cap := range missing {
				signCapsAfter(t, phone, without(ownCaps, cap))
				if err := a.readerCheck(tctx(t), phone.Address, phone.Self(), repackedNeeds); err == nil {
					t.Fatalf("repacked descriptor dropped %s", cap)
				}
			}
		})
	}
}
