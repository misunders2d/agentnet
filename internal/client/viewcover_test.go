package client

import (
	"path/filepath"
	"slices"
	"testing"
)

// Every table names its scope in view_dirty, or is one no view reads: a table
// added without a choice fails here, not as an old view (MEL-748 scoped events).
func TestViewCoverNamesEveryTable(t *testing.T) {
	s, err := openStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	a := &Agent{store: s}
	if a.ViewKeysScoped() {
		t.Fatal("scoped before the step")
	}
	if _, err = s.db.Exec(ViewCoverSchema); err != nil {
		t.Fatal(err)
	}
	if !a.ViewKeysScoped() {
		t.Fatal("not scoped after the step")
	}
	rows, err := s.db.Query(`SELECT name FROM sqlite_master t WHERE type='table' AND NOT EXISTS(SELECT 1 FROM sqlite_master g WHERE g.type='trigger' AND g.tbl_name=t.name AND g.name LIKE 'view%')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(viewCoverSkip, name) {
			t.Errorf("table %s names no view scope and is not in viewCoverSkip", name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	// Each kind of key: a chat's own column, its message's chat, no chat's.
	if _, err = s.db.Exec(`INSERT INTO inbox(id,sender,ts,kind,body,received_at,conv) VALUES('i1','s',1,'message','b',1,'C1')`); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		write string
		want  []string
	}{
		{`INSERT INTO alert_mutes(conv) VALUES('C2')`, []string{"c:C2"}},
		{`INSERT INTO uploads(blob_id,message_id,state) VALUES('b1','i1','done')`, []string{"c:C1"}},
		{`INSERT INTO uploads(blob_id,message_id,state) VALUES('b2','gone','done')`, []string{"*"}},
		{`INSERT INTO approvals(address,added_at) VALUES('x',1)`, []string{"*"}},
		{`INSERT INTO routes(endpoint,until) VALUES('e',1)`, nil},
	} {
		_, mark, err := a.ViewDirtySince(0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.db.Exec(c.write); err != nil {
			t.Fatalf("%s: %v", c.write, err)
		}
		if keys, _, _ := a.ViewDirtySince(mark); !slices.Equal(keys, c.want) {
			t.Errorf("%s: keys %v, want %v", c.write, keys, c.want)
		}
	}

	// A chat's mark moves with its own key and with no chat's, not another's.
	_, before, err := a.ChatViewMark("C1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`INSERT INTO alert_mutes(conv) VALUES('C3')`); err != nil {
		t.Fatal(err)
	}
	if _, after, _ := a.ChatViewMark("C1"); after != before {
		t.Errorf("another chat's write moved C1's mark %d -> %d", before, after)
	}
	if _, err = s.db.Exec(`INSERT INTO alert_mutes(conv) VALUES('C1')`); err != nil {
		t.Fatal(err)
	}
	keys, after, _ := a.ChatViewMark("C1")
	if after == before || !slices.Equal(keys, []string{"c:C1"}) {
		t.Errorf("C1's own write: keys %v mark %d -> %d", keys, before, after)
	}
}
