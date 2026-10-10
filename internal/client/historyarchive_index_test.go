package client

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/sqlitedb"
)

func TestHistoryArchivePendingJobsIndexedAfterCompletedHistory(t *testing.T) {
	db, err := sqlitedb.Open(filepath.Join(t.TempDir(), "store.db"), schema)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<10000) INSERT INTO history_archive_jobs(id,envelope,source_fp,done) SELECT 'completed-'||x,'{}','synthetic',1 FROM n`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO history_archive_jobs(id,envelope,source_fp,error) VALUES ('failed','{}','synthetic','unavailable'),('first','{}','synthetic',''),('second','{}','synthetic','')`); err != nil {
		t.Fatal(err)
	}
	const selectPending = `SELECT id,envelope,source_fp,retained,pos FROM history_archive_jobs WHERE done=0 AND error='' ORDER BY rowid LIMIT 1`
	const resetPending = `UPDATE history_archive_jobs SET error='' WHERE done=0`
	for _, query := range []string{selectPending, resetPending} {
		rows, err := db.Query("EXPLAIN QUERY PLAN " + query)
		if err != nil {
			t.Fatal(err)
		}
		indexed := false
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err = rows.Scan(&id, &parent, &unused, &detail); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			if strings.Contains(detail, "SEARCH history_archive_jobs USING INDEX history_archive_jobs_pending") {
				indexed = true
			}
			if strings.Contains(detail, "SCAN history_archive_jobs") || strings.Contains(detail, "TEMP B-TREE") {
				rows.Close()
				t.Fatalf("pending job query scans completed history or sorts: %s", detail)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil || !indexed {
			t.Fatalf("pending job query lacks its bounded index: %v", err)
		}
	}
	assertFirst := func(want string) {
		t.Helper()
		var id, envelope, fingerprint string
		var retained, pos int
		if err := db.QueryRow(selectPending).Scan(&id, &envelope, &fingerprint, &retained, &pos); err != nil || id != want {
			t.Fatalf("earliest eligible job: got %q, want %q: %v", id, want, err)
		}
	}
	assertFirst("first")
	if _, err = db.Exec(`UPDATE history_archive_jobs SET done=1 WHERE id='first'`); err != nil {
		t.Fatal(err)
	}
	assertFirst("second")
	if _, err = db.Exec(resetPending); err != nil {
		t.Fatal(err)
	}
	assertFirst("failed")
	var completed int
	if err = db.QueryRow(`SELECT count(*) FROM history_archive_jobs WHERE done=1`).Scan(&completed); err != nil || completed != 10001 {
		t.Fatalf("wake reset changed completed jobs: %d %v", completed, err)
	}
}
