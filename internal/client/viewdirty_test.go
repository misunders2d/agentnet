package client

import (
	"database/sql"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/sqlitedb"
)

// Every table the chat list reads names the scope of each write, and a copy
// between devices names no chat (MEL-748). A write this table misses would leave a
// kept summary showing old state.
func TestViewDirtyNamesTheScopeOfEveryWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.db")
	s, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	if _, err = s.db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	a := &Agent{store: s}
	_, mark, err := a.ViewDirtySince(0)
	if err != nil {
		t.Fatal(err)
	}
	const inbox = `INSERT INTO inbox(id,sender,ts,kind,body,received_at,conv,sub) VALUES`
	const outbox = `INSERT INTO outbox(id,recipient,body,envelope,state,created_at,conv,sub) VALUES`
	for _, w := range []struct {
		name, sql string
		keys      string
	}{
		// A row received moves the arrival count, which history progress reads.
		{"chat message", inbox + `('i1','s',1,'message','b',1,'C1',NULL)`, "c:C1 h"},
		{"read", `UPDATE inbox SET read_at=2 WHERE id='i1'`, "c:C1"},
		{"moved", `UPDATE inbox SET conv='C2' WHERE id='i1'`, "c:C1 c:C2"},
		{"deleted", `DELETE FROM inbox WHERE id='i1'`, "c:C2"},
		{"device thread message", inbox + `('i2','P1',1,'message','b',1,NULL,NULL)`, "h p:P1"},
		{"device thread history", inbox + `('i3','P1',1,'message','b',1,NULL,'history')`, "h p:P1"},
		{"copied chat row", inbox + `('i4','s',1,'message','b',1,'C1','history')`, "h"},
		{"copied chat file", inbox + `('i5','s',1,'message','b',1,'C1','file')`, "h"},
		{"read sync received", inbox + `('i6','s',1,'message','b',1,NULL,'read-sync')`, "h"},
		{"continuity notice", inbox + `('i7','s',1,'message','b',1,'C3','continuity')`, "c:C3 h"},
		{"chat message sent", outbox + `('o1','r','b','e','queued',1,'C1',NULL)`, "c:C1"},
		{"delivery", `UPDATE outbox SET state='delivered' WHERE id='o1'`, "c:C1"},
		{"device thread sent", outbox + `('o2','P2','b','e','queued',1,NULL,NULL)`, "p:P2"},
		{"carrier sent", outbox + `('o3','r','b','e','queued',1,NULL,'device-history')`, "h"},
		{"carrier delivery", `UPDATE outbox SET state='delivered' WHERE id='o3'`, "h"},
		{"chat copy sent", outbox + `('o4','r','b','e','queued',1,'C1','history')`, "h"},
		{"chat mark", `INSERT INTO attention_marks(owner,conv,fingerprint,lid,state,revision,writer) VALUES('o','C1','f','l','read',1,'w')`, "c:C1"},
		{"device mark", `INSERT INTO attention_marks(owner,conv,fingerprint,lid,state,revision,writer) VALUES('o','','f','i2','read',1,'w')`, "p:P1"},
		{"notice", `INSERT INTO reply_receiver_inputs(binding,inbox_id) VALUES('b','i2')`, "p:P1"},
		{"chat", `INSERT INTO conversations(id,root,kind,peer,pinned_at) VALUES('C4','r','dm','p',1)`, "c:C4"},
		{"chat erased", `INSERT INTO conv_erased(conv,key,lid,deletion,shared) VALUES('C1','k','l','d',0)`, "c:C1"},
		{"device erased", `INSERT INTO conv_erased(conv,key,lid,deletion,shared) VALUES('','k','l','d',0)`, "*"},
		{"participation", `INSERT INTO participation_events(hash,conv,pid,type,author,event,received_at) VALUES('h','C1','p','t','a','e',1)`, "c:C1"},
		{"group", `INSERT INTO group_context(conv,payload) VALUES('C1','x')`, "c:C1"},
		{"group head", `INSERT INTO group_known_heads(conv,bootstrap,seq,hash) VALUES('C1','b',1,'h')`, "c:C1"},
		{"pending withdrawal", `INSERT INTO group_pending_withdrawals(conv,person,admission,record) VALUES('C1','p','a','r')`, "c:C1"},
		{"withdrawal", `INSERT INTO group_withdrawals(conv,person,admission,record) VALUES('C1','p','a','r')`, "c:C1"},
		{"skipped copy", `INSERT INTO skipped_copies(id,conv,lid,recipient,detail,created_ms) VALUES('s','C1','l','r','d',1)`, "c:C1"},
		{"shared title", `INSERT INTO topic_shared_titles(conv,topic,title,rev,writer,lid) VALUES('C1','t','T',1,'w','l')`, "c:C1"},
		{"placement", `INSERT INTO direct_topic_placements(owner,peer,id,author,topic,revision,writer,synthetic,host,host_key,agent_id) VALUES('o','P1','i','a','t',1,'w',0,'h','k','g')`, "p:P1"},
		{"device topic", `INSERT INTO topic_state(peer,topic,title,updated_at) VALUES('P1','t','T',1)`, "p:P1"},
		{"chat topic", `INSERT INTO topic_state(peer,topic,title,updated_at) VALUES('C4','t','T',1)`, "c:C4 p:C4"},
		{"person", `INSERT INTO persons(person,label,seq,hash,record,state,pinned_at) VALUES('p','L',0,'r','{}','pinned',1)`, "*"},
		{"renamed", `UPDATE persons SET label='M' WHERE person='p'`, "*"},
		{"device of a person", `INSERT INTO person_devices(address,person,fingerprint,added) VALUES('d','p','f',1)`, "*"},
		{"chain", `INSERT INTO person_chain(person,seq,hash,record) VALUES('p',1,'h','r')`, "*"},
		{"peer", `INSERT INTO peers(address,public,pinned_at) VALUES('x','k',1)`, "*"},
		{"recovered", `INSERT INTO config(k,v) VALUES('continuity-head/p','h')`, "*"},
		{"suspended", `INSERT INTO config(k,v) VALUES('suspended_devices','[]')`, "*"},
		{"suspended again", `UPDATE config SET v='["x"]' WHERE k='suspended_devices'`, "*"},
		{"arrival", `UPDATE config SET v='70' WHERE k='arrival'`, "h"},
		{"other setting", `INSERT INTO config(k,v) VALUES('unrelated','7')`, ""},
		{"job", `INSERT INTO history_jobs(device,fingerprint,pos,convs_total,state,created_at,updated_at) VALUES('d','f','',0,'running',1,1)`, "h"},
		{"catch-up", `INSERT INTO history_catchup(device,fingerprint,phase,pos,inbox_ceiling,outbox_ceiling,tail) VALUES('d','f','recent','',0,0,0)`, "h"},
		{"deferred", `INSERT INTO history_deferred(recipient_fp,dir,id) VALUES('f','in','i')`, "h"},
		{"copy", `INSERT INTO history_copies(recipient_fp,conv,author,lid,hash,carrier,source_dir,source_id) VALUES('f','C1','a','l','h','c','in','i')`, "h"},
		{"device job", `INSERT INTO device_history_jobs(device,fingerprint,older,tail) VALUES('d','f',0,0)`, "h"},
		{"device pending", `INSERT INTO device_history_pending(recipient_fp,storage,id) VALUES('f','in','i')`, "h"},
		{"device copy", `INSERT INTO device_history_copies(recipient_fp,author,id,hash,carrier) VALUES('f','a','i','h','c')`, "h"},
		{"imported row", `INSERT INTO device_history_rows(storage,id,recipient,recipient_fp,peer,direction,author_fp,hash,reply_to,at) VALUES('in','i2','r','f','P3','in','a','h','',1)`, "p:P3"},
	} {
		if _, err := s.db.Exec(w.sql); err != nil {
			t.Fatalf("%s: %v", w.name, err)
		}
		keys, next, err := a.ViewDirtySince(mark)
		if err != nil {
			t.Fatal(err)
		}
		slices.Sort(keys)
		if got := strings.Join(keys, " "); got != w.keys {
			t.Errorf("%s marks %q, want %q", w.name, got, w.keys)
		}
		mark = next
	}

	// A rolled-back write names nothing, and the count stays in order for the next.
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`INSERT INTO conversations(id,root,kind,peer,pinned_at) VALUES('C9','r','dm','p',1)`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if keys, _, _ := a.ViewDirtySince(mark); len(keys) != 0 {
		t.Errorf("a rolled back write marked %v", keys)
	}

	// Another process's write is seen through this one's mark.
	other, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(10000)")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err = other.Exec(`INSERT INTO conversations(id,root,kind,peer,pinned_at) VALUES('C8','r','dm','p',1)`); err != nil {
		t.Fatal(err)
	}
	if keys, next, _ := a.ViewDirtySince(mark); !slices.Equal(keys, []string{"c:C8"}) || next <= mark {
		t.Errorf("another process's write: %v next %d after %d", keys, next, mark)
	}
}

// A store upgraded with rows in it starts with everything to read: no kept
// summary can predate the step, and every later write is marked.
func TestViewDirtyStartsWithEverythingOnAnUpgrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.db")
	old, err := sqlitedb.Open(path, schema[:len(schema)-1])
	if err != nil {
		t.Fatal(err)
	}
	if _, err = old.Exec(`INSERT INTO conversations(id,root,kind,peer,pinned_at) VALUES('C1','r','dm','p',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err = old.Exec(`INSERT INTO inbox(id,sender,ts,kind,body,received_at,conv) VALUES('i1','s',1,'message','b',1,'C1')`); err != nil {
		t.Fatal(err)
	}
	old.Close()
	s, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	a := &Agent{store: s}
	keys, mark, err := a.ViewDirtySince(0)
	if err != nil || !slices.Equal(keys, []string{"*"}) {
		t.Fatalf("after the upgrade: %v %v", keys, err)
	}
	if _, err = s.db.Exec(`UPDATE inbox SET read_at=2 WHERE id='i1'`); err != nil {
		t.Fatal(err)
	}
	if keys, _, _ = a.ViewDirtySince(mark); !slices.Equal(keys, []string{"c:C1"}) {
		t.Fatalf("a later write marks %v", keys)
	}
}
