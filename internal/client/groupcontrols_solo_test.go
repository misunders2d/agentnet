package client

import (
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
)

// The last person in a group still owns their messages. A departed peer is
// not a prerequisite for editing or deleting them on that person's devices.
func TestGroupControlsLastPerson(t *testing.T) {
	for _, linked := range []bool{false, true} {
		name := "single device"
		if linked {
			name = "linked device"
		}
		t.Run(name, func(t *testing.T) {
			w, carol, packet, _ := groupTurnsFixture(t)
			conv := packet.State.Conv
			sent, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "before everyone left"})
			if err != nil {
				t.Fatal(err)
			}
			ref, err := w.alice.RefOf(conv, sent.ID, "out")
			if err != nil {
				t.Fatal(err)
			}
			for _, peer := range []*Agent{w.bob, carol} {
				leave, err := peer.SignGroupWithdrawal(tctx(t), conv)
				if err != nil {
					t.Fatal(err)
				}
				if err = w.alice.AcceptGroupWithdrawal(tctx(t), leave); err != nil {
					t.Fatal(err)
				}
			}
			devices := []*Agent{w.alice}
			if linked {
				phone, await, _ := linkPhone(t, w.alice, "last-person-phone")
				if err = w.alice.DecideLink(tctx(t), pendingLink(t, w.alice).ID, true); err != nil {
					t.Fatal(err)
				}
				if result := <-await; result.err != nil {
					t.Fatal(result.err)
				}
				runAgent(t, phone)
				publishGroupFixtureCaps(t, phone, true)
				eventually(t, "linked device receives original", func() bool { return len(groupTurns(t, phone, conv)) == 1 })
				devices = append(devices, phone)
			}
			if _, err = w.alice.Revise(tctx(t), ref, "edited after departure"); err != nil {
				t.Fatal(err)
			}
			if result, err := w.alice.Retract(tctx(t), ref, ""); err != nil || result.ID == "" {
				t.Fatalf("last person cannot delete: %+v %v", result, err)
			}
			for _, a := range devices {
				eventually(t, "last person's deletion converges", func() bool {
					rows := groupTurns(t, a, conv)
					return len(rows) == 1 && rows[0].Deleted && rows[0].Body == "" && rows[0].Text == ""
				})
			}
			var leaked int
			if err = w.alice.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE conv=? AND sub IN (?,?) AND recipient IN (?,?)`, conv, envelope.SubRevision, envelope.SubRetraction, w.bob.Address, carol.Address).Scan(&leaked); err != nil || leaked != 0 {
				t.Fatalf("control sent to departed person: %d %v", leaked, err)
			}
		})
	}
}
