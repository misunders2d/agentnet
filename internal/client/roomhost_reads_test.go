package client

import (
	"github.com/misunders2d/agentnet/internal/protocol"
	"testing"
)

// Repeated counted room events must not reload the same current person and
// chain in one reconstruction. A subsequent reconstruction must see changes.
func TestRoomHostEvidenceReadsOnceAndStaysFresh(t *testing.T) {
	w, _, packet, stops := groupTurnsFixture(t)
	for _, stop := range stops {
		stop()
	}
	tx, err := w.alice.store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	m, err := controlMembers(tx, packet.State.Conv)
	if err != nil {
		t.Fatal(err)
	}
	var events []protocol.ParticipationEvent
	for _, member := range packet.State.Members {
		p, ok, err := personByIDIn(tx, member.Person)
		if err != nil || !ok {
			t.Fatal(err)
		}
		for n := 0; n < 4; n++ {
			ev := protocol.ParticipationEvent{V: 1, Conv: packet.State.Conv, PID: protocol.NewID(), Type: protocol.EventAccept, TS: int64(n + 1), Author: protocol.EventAuthor{Person: p.info.Person, Roster: p.info.Roster, Address: p.info.Address, Fingerprint: p.info.Fingerprint}}
			if _, err := tx.Exec(`INSERT INTO room_membership_events VALUES(?,?,?)`, ev.Hash(), ev.Conv, ev.PID); err != nil {
				t.Fatal(err)
			}
			events = append(events, ev)
		}
	}
	q := &countedControlReads{dbq: tx, persons: map[string]int{}}
	if err := m.loadHosts(q, events); err != nil {
		t.Fatal(err)
	}
	t.Logf("events=%d person loads=%v total statements=%d", len(events), q.persons, q.total)
	for _, member := range packet.State.Members {
		if q.persons[member.Person] != 1 {
			t.Errorf("current person reloaded %d times", q.persons[member.Person])
		}
		if _, ok := m.roomAuthors[member.Person]; !ok {
			t.Fatal("missing counted author")
		}
	}
	if q.total != 1+3*len(packet.State.Members) {
		t.Errorf("duplicate current evidence reads: %d", q.total)
	}
	// Exact old-chain membership is checked per event, never inferred from a
	// person's current presence or an earlier counted event.
	for i := range events {
		events[i].Author.Roster = "missing"
		if _, err := tx.Exec(`INSERT INTO room_membership_events VALUES(?,?,?)`, events[i].Hash(), events[i].Conv, events[i].PID); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.loadHosts(tx, events); err != nil {
		t.Fatal(err)
	}
	if len(m.roomAuthors) != 0 {
		t.Fatal("unknown author roster gained authority")
	}
	for i := range events {
		p, _, _ := personByIDIn(tx, events[i].Author.Person)
		events[i].Author.Roster = p.info.Roster
	}
	person := packet.State.Members[0].Person
	for _, mode := range []string{"freeze", "key", "chain"} {
		t.Run(mode, func(t *testing.T) {
			if _, err := tx.Exec(`SAVEPOINT fresh_host`); err != nil {
				t.Fatal(err)
			}
			defer tx.Exec(`ROLLBACK TO fresh_host`)
			var err error
			switch mode {
			case "freeze":
				_, err = tx.Exec(`UPDATE persons SET state=? WHERE person=?`, personConflict, person)
			case "key":
				_, err = tx.Exec(`UPDATE persons SET record=json_set(record,'$.devices',json('[]')) WHERE person=?`, person)
			case "chain":
				_, err = tx.Exec(`DELETE FROM person_chain WHERE person=?`, person)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := m.loadHosts(tx, events); err != nil {
				t.Fatal(err)
			}
			if _, ok := m.roomAuthors[person]; ok {
				t.Fatal("previous call retained author authority", mode)
			}
		})
	}
	// Plain DB and existing prepared readers each release their owned snapshot;
	// a later committed freeze must be visible even with the same dmMembers.
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	original, _, err := personByIDIn(w.alice.store.db, person)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"database", "prepared"} {
		t.Run(mode, func(t *testing.T) {
			var q dbq = w.alice.store.db
			if mode == "prepared" {
				var closeReads func()
				q, closeReads = prepareMembershipReads(q)
				defer closeReads()
			}
			if err := m.loadHosts(q, events); err != nil {
				t.Fatal(err)
			}
			if _, ok := m.roomAuthors[person]; !ok {
				t.Fatal("current author missing")
			}
			if _, err := w.alice.store.db.Exec(`UPDATE persons SET state=? WHERE person=?`, personConflict, person); err != nil {
				t.Fatal(err)
			}
			defer w.alice.store.db.Exec(`UPDATE persons SET state=? WHERE person=?`, original.info.State, person)
			if err := m.loadHosts(q, events); err != nil {
				t.Fatal(err)
			}
			if _, ok := m.roomAuthors[person]; ok {
				t.Fatal("committed freeze hidden by prior snapshot")
			}
		})
	}

}
