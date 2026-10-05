package client

import (
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestMembershipPreparedReadsStayFresh(t *testing.T) {
	for _, name := range []string{"database", "transaction"} {
		t.Run(name, func(t *testing.T) {
			s := securityStore(t)
			id, err := identity.Generate()
			if err != nil {
				t.Fatal(err)
			}
			device := id.Public("fixture/desk")
			roster := protocol.PersonRoster{Person: "fixture", Label: "original", Devices: []identity.Public{device}}
			roster.Sign(id.Sign)
			raw, _ := json.Marshal(roster)
			if _, err := s.db.Exec(`INSERT INTO persons(person,label,seq,hash,record,state,pinned_at) VALUES(?,?,0,?,?,?,1)`, roster.Person, roster.Label, roster.Hash(), raw, personPinned); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec(`INSERT INTO person_devices(address,person,fingerprint,added) VALUES(?,?,?,0)`, device.Address, roster.Person, device.Fingerprint()); err != nil {
				t.Fatal(err)
			}
			var base dbq = s.db
			exec := s.db.Exec
			var tx *sql.Tx
			if name == "transaction" {
				tx, err = s.db.Begin()
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				base = tx
				exec = tx.Exec
			}
			q, closeReads := prepareMembershipReads(base)
			defer closeReads()
			check := func(q dbq, label, state string, added int64) {
				t.Helper()
				p, ok, err := personByIDIn(q, roster.Person)
				if err != nil || !ok || p.info.Label != label || p.info.State != state || len(p.info.Devices) != 1 || p.info.Devices[0].Added != added {
					t.Fatalf("membership read: %+v present=%t error=%v", p.info, ok, err)
				}
			}
			check(q, "original", personPinned, 0)
			if _, err := exec(`UPDATE persons SET label=?,state=? WHERE person=?`, "changed", personConflict, roster.Person); err != nil {
				t.Fatal(err)
			}
			if _, err := exec(`UPDATE person_devices SET added=4 WHERE person=?`, roster.Person); err != nil {
				t.Fatal(err)
			}
			// Reusing SQL must neither reuse its old authority rows nor bind
			// a later lookup to the preceding person's arguments.
			check(q, "changed", personConflict, 4)
			if _, ok, err := personByIDIn(q, "absent"); err != nil || ok {
				t.Fatalf("previous person's rows reused: present=%t error=%v", ok, err)
			}
			check(q, "changed", personConflict, 4)
			closeReads()
			if tx != nil {
				if err := tx.Rollback(); err != nil {
					t.Fatal(err)
				}
				check(s.db, "original", personPinned, 0)
			} else {
				check(s.db, "changed", personConflict, 4)
			}
		})
	}
}
