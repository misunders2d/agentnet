package client

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

type countedControlReads struct {
	dbq
	persons map[string]int
	heads   int
	total   int
}

func (q *countedControlReads) QueryRow(query string, args ...any) *sql.Row {
	q.total++
	if query == `SELECT `+personCols+` FROM persons WHERE person = ?` {
		q.persons[args[0].(string)]++
	}
	if query == `SELECT max(seq) FROM group_proof_records WHERE conv=? AND bootstrap=?` {
		q.heads++
	}
	return q.dbq.QueryRow(query, args...)
}
func (q *countedControlReads) Query(query string, args ...any) (*sql.Rows, error) {
	q.total++
	return q.dbq.Query(query, args...)
}
func TestControlMembersReadsEachCurrentPersonOnce(t *testing.T) {
	w, _, packet, stops := groupTurnsFixture(t)
	for _, stop := range stops {
		stop()
	}
	tx, err := w.alice.store.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	q := &countedControlReads{dbq: tx, persons: map[string]int{}}
	members, err := controlMembers(q, packet.State.Conv)
	if err != nil {
		t.Fatal(err)
	}
	if len(members.persons) != len(packet.State.Members) {
		t.Fatal("missing current members", len(members.persons))
	}
	t.Logf("members=%d person loads=%v group head reads=%d total statements=%d", len(packet.State.Members), q.persons, q.heads, q.total)
	for _, member := range packet.State.Members {
		if q.persons[member.Person] != 1 {
			t.Errorf("current person reloaded %d times in one authority snapshot", q.persons[member.Person])
		}
	}
	if q.heads != 1 {
		t.Errorf("same group head reread %d times", q.heads)
	}
}

func TestControlMembersSnapshotKeepsAuthorityFresh(t *testing.T) {
	w, _, packet, stops := groupTurnsFixture(t)
	for _, stop := range stops {
		stop()
	}
	for _, mode := range []string{"head", "freeze", "pending-withdrawal", "withdrawal", "key"} {
		t.Run(mode, func(t *testing.T) {
			tx, err := w.alice.store.db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			q, closeReads := prepareMembershipReads(tx)
			defer closeReads()
			initial, err := controlMembers(q, packet.State.Conv)
			if err != nil || !initial.device(w.bob.Address, w.bob.Self().Fingerprint()) {
				t.Fatal("initial authority", err)
			}
			p, ok, err := personByIDIn(tx, packet.State.Members[0].Person)
			if err != nil || !ok {
				t.Fatal(err)
			}
			switch mode {
			case "head":
				_, err = tx.Exec(`INSERT OR REPLACE INTO group_known_heads(conv,bootstrap,seq,hash) VALUES(?,?,?,?)`, packet.State.Conv, packet.Root.Creator.Fingerprint, packet.State.Seq+1, "newer")
			case "freeze":
				_, err = tx.Exec(`UPDATE persons SET state=? WHERE person=?`, personConflict, p.info.Person)
			case "pending-withdrawal":
				_, err = tx.Exec(`INSERT INTO group_pending_withdrawals(conv,person,admission,record) VALUES(?,?,?,'{}')`, packet.State.Conv, p.info.Person, packet.State.Members[0].Admission.Hash())
			case "withdrawal":
				_, err = tx.Exec(`INSERT INTO group_withdrawals(conv,person,admission,record) VALUES(?,?,?,'{}')`, packet.State.Conv, p.info.Person, packet.State.Members[0].Admission.Hash())
			case "key":
				_, err = tx.Exec(`UPDATE persons SET record=json_set(record,'$.devices',json('[]')) WHERE person=?`, p.info.Person)
			}
			if err != nil {
				t.Fatal(err)
			}
			after, err := controlMembers(q, packet.State.Conv)
			if mode == "withdrawal" || mode == "key" {
				if err != nil {
					t.Fatal(err)
				}
				for _, d := range p.roster.Devices {
					if after.device(d.Address, d.Fingerprint()) {
						t.Fatal("preceding snapshot retained removed authority", mode)
					}
				}
			} else if !errors.Is(err, ErrGroupContextPending) {
				t.Fatal("fresh refusal missing", mode, err)
			}
		})
	}
	// Plain DB and existing statement wrappers must close their owned snapshot
	// and see a committed freeze on the next call, without memoizing authority.
	for _, mode := range []string{"database", "prepared"} {
		t.Run(mode, func(t *testing.T) {
			var q dbq = w.alice.store.db
			if mode == "prepared" {
				var closeReads func()
				q, closeReads = prepareMembershipReads(q)
				defer closeReads()
			}
			if _, err := controlMembers(q, packet.State.Conv); err != nil {
				t.Fatal(err)
			}
			p := packet.State.Members[0].Person
			var state string
			if err := w.alice.store.db.QueryRow(`SELECT state FROM persons WHERE person=?`, p).Scan(&state); err != nil {
				t.Fatal(err)
			}
			if _, err := w.alice.store.db.Exec(`UPDATE persons SET state=? WHERE person=?`, personConflict, p); err != nil {
				t.Fatal(err)
			}
			defer w.alice.store.db.Exec(`UPDATE persons SET state=? WHERE person=?`, state, p)
			if _, err := controlMembers(q, packet.State.Conv); !errors.Is(err, ErrGroupContextPending) {
				t.Fatal("DB snapshot outlived decision", err)
			}
		})
	}
}
