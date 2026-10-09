package client

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestOwnSyncQuarantinedFullBatchStaysBounded(t *testing.T) {
	for _, sub := range []string{envelope.SubReadSync, envelope.SubTopicSync} {
		t.Run(sub, func(t *testing.T) {
			w := newWorld(t, "")
			stop := runAgent(t, w.alice)
			persons(t, w.alice)
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
			for i := 0; i < protocol.MaxReadRefs; i++ {
				id := protocol.NewID()
				if i == 0 {
					id = topic
				}
				if sub == envelope.SubReadSync {
					_, err = a.store.db.Exec(`INSERT INTO read_marks VALUES(?,?,?,?)`, own.info.Person, conv, w.bob.Self().Fingerprint(), id)
				} else {
					_, err = a.store.db.Exec(`INSERT INTO topic_titles VALUES(?,?,?,?,?,?)`, own.info.Person, conv, id, "Private name", 2, a.Self().Fingerprint())
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			sync := func() (bool, error) {
				if sub == envelope.SubReadSync {
					return a.syncReadMarks()
				}
				return a.syncTopicTitles()
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
			if err = phone.verifyAndStore(tctx(t), env); err != nil {
				t.Fatal(err)
			}
			if heldReason(t, phone, id) == "" {
				t.Fatal("pending key did not quarantine the real encrypted carrier")
			}
			if err = a.store.setOutboxState(id, protocol.StateQuarantined, "", ""); err != nil {
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
			if n := count(t, a, "outbox WHERE sub='"+sub+"'"); n != 1 {
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
			} else {
				if titleAt(t, phone, conv, topic) != "Private name" {
					t.Fatal("retained title was not applied")
				}
				err = a.setTopicTitle(conv, topic, "") // an explicit new reset is a different fact
			}
			if err != nil {
				t.Fatal(err)
			}
			if more, err := sync(); err != nil || more {
				t.Fatalf("new single fact: more=%v err=%v", more, err)
			}
			if n := count(t, a, "outbox WHERE sub='"+sub+"'"); n != 2 {
				t.Fatalf("new semantic fact did not get one new carrier: %d", n)
			}
		})
	}
}
