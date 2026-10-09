package client

import (
	"fmt"
	"path/filepath"
	"testing"
)

func TestHistoryProgressDoesNotCompleteDeferredCatchup(t *testing.T) {
	s, err := openStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	a := &Agent{Address: "alice/laptop", store: s}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := s.db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO history_jobs(device,fingerprint,pos,convs_total,state,created_at,updated_at) VALUES('alice/phone','fp','{"conv":"old","ms":1,"id":"old"}',2,'done',1,1)`)
	exec(`INSERT INTO history_catchup(device,fingerprint,phase,pos,inbox_ceiling,outbox_ceiling,tail,context_done) VALUES('alice/phone','fp','done','{}',0,0,0,1)`)
	check := func(state string, done, total int) {
		t.Helper()
		jobs, err := a.HistoryProgress()
		if err != nil || len(jobs) != 1 || jobs[0].State != state || jobs[0].ConvsDone != done || jobs[0].ConvsTotal != total {
			t.Fatalf("progress: %+v, %v", jobs, err)
		}
	}
	check("done", 2, 2)
	exec(`INSERT INTO history_deferred(recipient_fp,dir,id) VALUES('fp','context','missing')`)
	check("running", 0, 0)
	var state, pos string
	if err := s.db.QueryRow(`SELECT state,pos FROM history_jobs`).Scan(&state, &pos); err != nil || state != "done" || pos != `{"conv":"old","ms":1,"id":"old"}` {
		t.Fatalf("projection changed scheduling: %q %q %v", state, pos, err)
	}
	exec(`DELETE FROM history_deferred`)
	check("done", 2, 2)
	exec(`UPDATE history_catchup SET phase='older'`)
	check("running", 0, 0)
	exec(`UPDATE history_catchup SET phase='done',context_done=0`)
	check("running", 0, 0)
	exec(`UPDATE history_catchup SET context_done=1`)
	exec(`UPDATE config SET v='1' WHERE k='arrival'`)
	check("running", 0, 0)
	exec(`UPDATE history_catchup SET tail=1`)
	check("done", 2, 2)
	exec(`INSERT INTO history_deferred(recipient_fp,dir,id) VALUES('old-key','context','missing')`)
	check("done", 2, 2)
	exec(`UPDATE history_jobs SET state='ended'`)
	exec(`UPDATE history_catchup SET phase='recent'`)
	check("ended", 0, 2)
}

func TestHistoryProgressCountsOnlyCurrentExactCarrier(t *testing.T) {
	s, err := openStore(filepath.Join(t.TempDir(), "client.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	a := &Agent{store: s}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO history_jobs(device,fingerprint,pos,convs_total,state,created_at,updated_at) VALUES('alice/phone','current','{}',0,'done',1,1)`)
	exec(`INSERT INTO history_catchup(device,fingerprint,phase,pos,inbox_ceiling,outbox_ceiling,tail,context_done) VALUES('alice/phone','current','done','{}',0,0,0,1)`)
	for i, state := range []string{"queued", "custody", "delivered", "quarantined"} {
		id := fmt.Sprintf("%032x", i+1)
		exec(`INSERT INTO outbox(id,recipient,recipient_fp,sub,body,envelope,state,created_at) VALUES(?,'alice/phone','current','history','','',?,1)`, id, state)
		exec(`INSERT INTO history_copies(recipient_fp,conv,author,lid,hash,carrier,source_dir,source_id) VALUES('current','conv','author',?,'hash',?,'in',?)`, id, id, id)
	}
	// Old rejected attempts and an old recipient key are not this job's copies.
	exec(`INSERT INTO outbox(id,recipient,recipient_fp,sub,body,envelope,state,created_at) VALUES('old-attempt','alice/phone','current','history','','','quarantined',1)`)
	exec(`INSERT INTO history_copies(recipient_fp,conv,author,lid,hash,carrier,source_dir,source_id) VALUES('old-key','conv','author','old','hash','missing','in','old')`)
	jobs, err := a.HistoryProgress()
	if err != nil || len(jobs) != 1 {
		t.Fatalf("progress: %+v %v", jobs, err)
	}
	j := jobs[0]
	if j.State != "done" || !j.DeliveryKnown || j.Queued != 1 || j.Custody != 1 || j.Delivered != 1 || j.Blocked != 1 || j.Deferred != 0 {
		t.Fatalf("producer/receipt distinction lost: %+v", j)
	}
}
