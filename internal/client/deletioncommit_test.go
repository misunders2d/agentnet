package client

import (
	"database/sql"
	"sync"
	"testing"
)

// deletionTextLeft lists the stored messages (and edits of them) whose text
// a stored authorized deletion removes but which still carry it, in the scope
// redactRetractedText uses: a device message by id, a conversation message
// by its logical id, each under the key the deletion names.
const deletionTextLeft = `WITH c AS (
	SELECT conv, ref_id, ref_fp FROM inbox WHERE sub='retraction' AND ref_id IS NOT NULL
	UNION SELECT conv, ref_id, ref_fp FROM outbox WHERE sub='retraction' AND ref_id IS NOT NULL)
SELECT 'inbox ' || i.id FROM inbox i JOIN c ON coalesce(i.verified_by, i.claimed_fp, '') = c.ref_fp
	AND ((c.conv IS NULL AND i.conv IS NULL AND i.id = c.ref_id) OR (c.conv IS NOT NULL AND i.conv = c.conv AND i.lid = c.ref_id))
	WHERE i.kind = 'message' AND i.body != ''
UNION ALL SELECT 'outbox ' || o.id FROM outbox o JOIN c ON c.ref_fp = ?
	AND ((c.conv IS NULL AND o.conv IS NULL AND o.id = c.ref_id) OR (c.conv IS NOT NULL AND o.conv = c.conv AND o.lid = c.ref_id))
	WHERE coalesce(o.kind, json_extract(o.envelope, '$.kind')) = 'message' AND o.body != ''
UNION ALL SELECT 'inbox edit ' || e.id FROM inbox e JOIN c ON e.conv IS c.conv AND e.ref_id = c.ref_id AND e.ref_fp = c.ref_fp
	WHERE e.sub = 'revision' AND e.body != ''
UNION ALL SELECT 'outbox edit ' || e.id FROM outbox e JOIN c ON e.conv IS c.conv AND e.ref_id = c.ref_id AND e.ref_fp = c.ref_fp
	WHERE e.sub = 'revision' AND e.body != ''`

// watchDeletionCommits checks, right after each commit of a's store and
// through a separate reader (as another process or the view sees it), that
// no stored deletion sits beside the text it removes. The returned function
// stops watching and reports how many commits were checked.
func watchDeletionCommits(t *testing.T, a *Agent) func() int {
	t.Helper()
	_, path := paths(a.home)
	r, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(10000)&_pragma=query_only(1)")
	if err != nil {
		t.Fatal(err)
	}
	r.SetMaxOpenConns(1)
	self := a.Self().Fingerprint()
	var mu sync.Mutex
	var checked int
	var seen []string
	stopped := false
	check := func() {
		mu.Lock()
		defer mu.Unlock()
		if stopped {
			return
		}
		rows, err := r.Query(deletionTextLeft, self)
		if err != nil {
			seen = append(seen, "query: "+err.Error())
			return
		}
		defer rows.Close()
		for rows.Next() {
			var s string
			if rows.Scan(&s) == nil {
				seen = append(seen, s)
			}
		}
		checked++
	}
	a.store.committed.Store(&check)
	stop := func() int {
		t.Helper()
		a.store.committed.Store(nil)
		mu.Lock()
		defer mu.Unlock()
		if stopped {
			return checked
		}
		stopped = true
		r.Close()
		if len(seen) != 0 {
			t.Errorf("a commit stored a deletion beside its text: %v", seen)
		}
		return checked
	}
	t.Cleanup(func() { stop() })
	return stop
}
