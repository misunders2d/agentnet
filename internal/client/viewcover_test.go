package client

import (
	"database/sql"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/sqlitedb"
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
	if !a.ViewKeysScoped() {
		t.Fatal("a new store is not scoped")
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
		{`INSERT INTO alert_mutes(conv) VALUES('C2')`, []string{"vc:C2"}},
		{`INSERT INTO uploads(blob_id,message_id,state) VALUES('b1','i1','done')`, []string{"vc:C1"}},
		{`INSERT INTO uploads(blob_id,message_id,state) VALUES('b2','gone','done')`, []string{"v*"}},
		{`INSERT INTO approvals(address,added_at) VALUES('x',1)`, []string{"v*"}},
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

	// Attention's write check sees only the keys it saw before the step: a
	// cover write, in its chat or in none, never refuses Mark unread or read
	// (busy agents write attention, requests and uploads all the time).
	scope := map[string]protocol.ReadRef{"i1": {Conv: "C1"}}
	_, mark, _ := a.ViewDirtySince(0)
	for _, w := range []string{`INSERT INTO alert_mutes(conv) VALUES('C1')`, `DELETE FROM alert_mutes WHERE conv='C1'`, `INSERT INTO approvals(address,added_at) VALUES('y',1)`, `UPDATE uploads SET state='sent' WHERE blob_id='b1'`} {
		if _, err = s.db.Exec(w); err != nil {
			t.Fatalf("%s: %v", w, err)
		}
	}
	if moved, err := attentionScopeMoved(s.db, mark, scope); err != nil || moved {
		t.Errorf("cover writes moved attention's scope (%v, %v)", moved, err)
	}
	if _, err = s.db.Exec(`UPDATE inbox SET body='edited' WHERE id='i1'`); err != nil {
		t.Fatal(err)
	}
	if moved, _ := attentionScopeMoved(s.db, mark, scope); !moved {
		t.Error("the chat's own message write did not move attention's scope")
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
	if after == before || !slices.Equal(keys, []string{"c:C1"}) { // the page's scope: its key, not the cover's
		t.Errorf("C1's own write: keys %v mark %d -> %d", keys, before, after)
	}
}

// ViewCoverSchema's short triggers mark exactly the keys the same triggers
// written as selects (viewTable, the form of every other view trigger) mark,
// for each kind of key and a row of no chat, under the same names, in at most
// three quarters of the text: every connection parses that text when it opens,
// and the select form took the client race shards past their bound on CI.
func TestViewCoverSchemaMarksTheSelectKeys(t *testing.T) {
	var selects strings.Builder
	for _, c := range viewCover {
		key := c.key
		selects.WriteString(viewTable("cover_"+c.table, c.table, func(row string) string { return "SELECT 'v'||k AS k FROM (" + key(row) + ")" }))
	}
	open := func(steps []string) *sql.DB {
		db, err := sqlitedb.Open(filepath.Join(t.TempDir(), "agent.db"), steps)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		return db
	}
	if schema[len(schema)-1] != ViewCoverSchema {
		t.Fatal("ViewCoverSchema is not the last step")
	}
	long, short := open(append(slices.Clone(schema[:len(schema)-1]), selects.String())), open(schema)
	coverText := func(db *sql.DB) (names []string, size int) {
		rows, err := db.Query(`SELECT name,length(sql) FROM sqlite_master WHERE type='trigger' AND name LIKE 'view\_cover\_%' ESCAPE '\' ORDER BY name`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var name string
			var n int
			if err := rows.Scan(&name, &n); err != nil {
				t.Fatal(err)
			}
			names, size = append(names, name), size+n
		}
		return names, size
	}
	lnames, lsize := coverText(long)
	snames, ssize := coverText(short)
	if len(snames) != 3*len(viewCover) || !slices.Equal(lnames, snames) {
		t.Fatalf("cover triggers: %d as selects, %d short, want the same %d", len(lnames), len(snames), 3*len(viewCover))
	}
	if ssize*4 > lsize*3 {
		t.Errorf("the short cover triggers are %d bytes, as selects %d", ssize, lsize)
	}
	since := func(db *sql.DB, mark int64) ([]string, int64) {
		rows, err := db.Query(`SELECT seq,key FROM view_dirty WHERE seq>? ORDER BY seq`, mark)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var keys []string
		for rows.Next() {
			var k string
			if err := rows.Scan(&mark, &k); err != nil {
				t.Fatal(err)
			}
			keys = append(keys, k)
		}
		slices.Sort(keys)
		return slices.Compact(keys), mark
	}
	_, lmark := since(long, 0)
	_, smark := since(short, 0)
	for _, w := range []string{
		`INSERT INTO inbox(id,sender,ts,kind,body,received_at,conv) VALUES('i1','s',1,'message','b',1,'C1')`,
		`INSERT INTO conversations(id,root,kind,peer,pinned_at) VALUES('C9','r','dm','p',1)`,
		`INSERT INTO alert_mutes(conv) VALUES('C2')`,
		`DELETE FROM alert_mutes WHERE conv='C2'`,
		`INSERT INTO reminders(message,conv,due_at,state,rev,created_at,updated_at) VALUES('m1',NULL,1,'pending',1,1,1)`,
		`UPDATE reminders SET conv='C3' WHERE message='m1'`,
		`UPDATE reminders SET conv=NULL WHERE message='m1'`,
		`INSERT INTO uploads(blob_id,message_id,state) VALUES('b1','i1','done')`,
		`INSERT INTO uploads(blob_id,message_id,state) VALUES('b2','gone','done')`,
		`UPDATE uploads SET message_id='gone' WHERE blob_id='b1'`,
		`INSERT INTO topic_titles(owner,scope,topic,title,revision,writer) VALUES('o','C9','t1','x',1,'w'),('o','peer/x','t2','y',1,'w')`,
		`UPDATE topic_titles SET scope='C9' WHERE topic='t2'`,
		`DELETE FROM topic_titles`,
		`INSERT INTO approvals(address,added_at) VALUES('x',1)`,
		`UPDATE approvals SET added_at=2`,
		`DELETE FROM approvals`,
		`INSERT INTO history_archive_jobs(id,envelope,source_fp) VALUES('j','{}','f')`,
		`UPDATE history_archive_jobs SET done=1`,
	} {
		for _, db := range []*sql.DB{long, short} {
			if _, err := db.Exec(w); err != nil {
				t.Fatalf("%s: %v", w, err)
			}
		}
		var lk, sk []string
		lk, lmark = since(long, lmark)
		sk, smark = since(short, smark)
		if !slices.Equal(lk, sk) {
			t.Errorf("%s: as selects marks %v, short %v", w, lk, sk)
		}
	}
}
