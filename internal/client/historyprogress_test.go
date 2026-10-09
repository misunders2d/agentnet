package client

import (
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
