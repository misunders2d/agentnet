package client

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestOwnSyncQuarantinedFullBatchStaysBounded(t *testing.T) {
	for _, sub := range []string{envelope.SubReadSync, envelope.SubTopicSync, envelope.SubRootSync, envelope.SubInvitationSync} {
		t.Run(sub, func(t *testing.T) {
			w := newWorld(t, "")
			stop := runAgent(t, w.alice)
			persons(t, w.alice, w.bob)
			phone, awaited, _ := linkPhone(t, w.alice, "phone")
			if err := w.alice.DecideLink(tctx(t), pendingLink(t, w.alice).ID, true); err != nil {
				t.Fatal(err)
			}
			if linked := <-awaited; linked.err != nil {
				t.Fatal(linked.err)
			}
			stop() // each following producer pass is explicit and bounded
			a := w.alice
			own, ok, err := a.store.selfPerson(a.Address)
			if err != nil || !ok {
				t.Fatalf("verified owner: %v %v", ok, err)
			}
			conv, topic := strings.Repeat("c", 64), protocol.NewID()
			batch, carriers := protocol.MaxReadRefs, 1
			var root protocol.ConvRoot
			var invitation protocol.InvitationSync
			if sub == envelope.SubRootSync {
				stopBob := runAgent(t, w.bob)
				conv = newDM(t, a, w.bob)
				stopBob()
				root, _ = rootOf(t, a, conv)
				batch, carriers = historyPage, historyPage
			} else if sub == envelope.SubInvitationSync {
				group, err := a.CreateGroup(tctx(t), "Private pending invitations")
				if err != nil {
					t.Fatal(err)
				}
				bob, _, err := w.bob.store.selfPerson(w.bob.Address)
				if err != nil {
					t.Fatal(err)
				}
				invitation = protocol.InvitationSync{V: 1, Person: own.info.Person, Roster: own.info.Roster, Revision: 1, Status: "pending",
					Proposal: protocol.GroupInvitation{V: 1, Root: group.Root, State: group.State, Target: bob.info.Person, Roster: bob.info.Roster, Seq: group.State.Seq + 1, Prev: group.State.Hash()}}
				batch, carriers = historyPage, historyPage
			}
			addRoot := func() {
				t.Helper()
				root.Nonce = protocol.NewID()
				root.Sign(a.id.Sign)
				raw, err := json.Marshal(root)
				if err == nil {
					peer := root.Members[0].Person
					if peer == own.info.Person {
						peer = root.Members[1].Person
					}
					err = a.store.addConversation(root, raw, peer)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			putInvitation := func() {
				t.Helper()
				invitation.ID = invitation.Proposal.ID()
				tx, err := a.store.db.Begin()
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				if err = saveInvitationView(tx, invitation, a.Address, a.Self().Fingerprint()); err == nil {
					err = tx.Commit()
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			for i := 0; i < batch; i++ {
				id := protocol.NewID()
				if i == 0 {
					id = topic
				}
				if sub == envelope.SubReadSync {
					_, err = a.store.db.Exec(`INSERT INTO read_marks VALUES(?,?,?,?)`, own.info.Person, conv, w.bob.Self().Fingerprint(), id)
				} else if sub == envelope.SubTopicSync {
					_, err = a.store.db.Exec(`INSERT INTO topic_titles VALUES(?,?,?,?,?,?)`, own.info.Person, conv, id, "Private name", 2, a.Self().Fingerprint())
				} else if sub == envelope.SubRootSync {
					if i > 0 { // CreateDM already stored the first signed root.
						addRoot()
					}
				} else {
					invitation.Proposal.Nonce = protocol.NewID()
					putInvitation()
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			sync := func() (bool, error) {
				if sub == envelope.SubReadSync {
					return a.syncReadMarks()
				}
				if sub == envelope.SubTopicSync {
					return a.syncTopicTitles()
				}
				if sub == envelope.SubRootSync {
					return a.syncRoots()
				}
				return a.syncInvitations()
			}
			if more, err := sync(); err != nil || !more {
				t.Fatalf("full first page: more=%v err=%v", more, err)
			}
			var id, sealed string
			if err = a.store.db.QueryRow(`SELECT id,envelope FROM outbox WHERE sub=? AND recipient=?`, sub, phone.Address).Scan(&id, &sealed); err != nil {
				t.Fatal(err)
			}
			var env envelope.Envelope
			if err = json.Unmarshal([]byte(sealed), &env); err != nil {
				t.Fatal(err)
			}
			if err = phone.store.pin(a.Self()); err != nil {
				t.Fatal(err)
			}
			if _, err = phone.store.db.Exec(`UPDATE peers SET pending=public WHERE address=?`, a.Address); err != nil {
				t.Fatal(err)
			}
			if sub == envelope.SubRootSync {
				// Root admission uses the verified person as authority; exercise
				// its frozen-person hold without bypassing the transport pin gate.
				if _, err = phone.store.db.Exec(`UPDATE persons SET state=? WHERE state=?`, personConflict, personSelf); err != nil {
					t.Fatal(err)
				}
			}
			if err = phone.verifyAndStore(tctx(t), env); err != nil {
				t.Fatal(err)
			}
			if heldReason(t, phone, id) == "" {
				t.Fatal("blocked authority did not quarantine the real encrypted carrier")
			}
			if err = a.store.setOutboxState(id, protocol.StateQuarantined, "", ""); err != nil {
				t.Fatal(err)
			}
			// Root/invitation pages use one carrier per fact; model the same
			// quarantine receipt for the rest of this full producer page.
			if _, err = a.store.db.Exec(`UPDATE outbox SET state=? WHERE sub=? AND recipient=?`, protocol.StateQuarantined, sub, phone.Address); err != nil {
				t.Fatal(err)
			}
			// convSync immediately asks for another page after a full batch.
			// A held carrier must not manufacture another full batch forever.
			if more, err := sync(); err != nil || more {
				t.Fatalf("quarantined full batch requeued immediately: more=%v err=%v", more, err)
			}
			if err = a.Close(); err != nil {
				t.Fatal(err)
			}
			a, err = Open(a.home)
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			for range 2 {
				if more, err := sync(); err != nil || more {
					t.Fatalf("restart reissued held facts: more=%v err=%v", more, err)
				}
			}
			if n := count(t, a, "outbox WHERE sub='"+sub+"'"); n != carriers {
				t.Fatalf("held facts produced %d carriers", n)
			}
			var kept string
			if err = a.store.db.QueryRow(`SELECT envelope FROM outbox WHERE id=?`, id).Scan(&kept); err != nil || kept != sealed {
				t.Fatalf("held ciphertext changed: %v", err)
			}
			// Receiver recovery admits the exact retained envelope after the
			// real key problem is resolved, without another source send.
			if _, err = phone.store.db.Exec(`UPDATE peers SET pending=NULL WHERE address=?`, a.Address); err != nil {
				t.Fatal(err)
			}
			if sub == envelope.SubRootSync {
				if _, err = phone.store.db.Exec(`UPDATE persons SET state=? WHERE state=?`, personSelf, personConflict); err != nil {
					t.Fatal(err)
				}
			}
			in, err := envelope.Open(env, phone.id, phone.Address, a.Self())
			if err != nil {
				t.Fatal(err)
			}
			if err = phone.admitConv(tctx(t), env, in, a.Self(), true); err != nil {
				t.Fatal(err)
			}
			if heldReason(t, phone, id) != "" {
				t.Fatal("retained carrier did not recover")
			}
			if sub == envelope.SubReadSync {
				if n := count(t, phone, "read_marks"); n != protocol.MaxReadRefs {
					t.Fatalf("recovered read facts: %d", n)
				}
				_, err = a.store.db.Exec(`INSERT INTO read_marks VALUES(?,?,?,?)`, own.info.Person, conv, w.bob.Self().Fingerprint(), protocol.NewID())
			} else if sub == envelope.SubTopicSync {
				if titleAt(t, phone, conv, topic) != "Private name" {
					t.Fatal("retained title was not applied")
				}
				err = a.setTopicTitle(conv, topic, "") // an explicit new reset is a different fact
			} else if sub == envelope.SubRootSync {
				if !hasRoot(t, phone, in.Conv) {
					t.Fatal("retained root was not admitted")
				}
				addRoot()
			} else {
				if n := count(t, phone, "own_invitation_views"); n != 1 {
					t.Fatalf("retained invitation view not admitted: %d", n)
				}
				invitation.Revision++
				invitation.Status = "cancelled"
				putInvitation()
			}
			if err != nil {
				t.Fatal(err)
			}
			if more, err := sync(); err != nil || more {
				t.Fatalf("new single fact: more=%v err=%v", more, err)
			}
			if n := count(t, a, "outbox WHERE sub='"+sub+"'"); n != carriers+1 {
				t.Fatalf("new semantic fact did not get one new carrier: %d", n)
			}
		})
	}
}
