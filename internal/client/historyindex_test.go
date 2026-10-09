package client

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/sqlitedb"
)

// Upgrading a populated pre-index store must retain the exact logical controls,
// while every correlated dedup lookup can address a conversation and logical ID.
// The repeated recipient copies model an old, busy linked-device conversation.
func TestHistoryDedupIndexUpgrade(t *testing.T) {
	turns, copies := 30, 4
	// The correctness gate stays small under the race detector. The larger
	// quadratic pre-index comparison is an explicit performance measurement.
	if os.Getenv("AGENTNET_PROFILE_HISTORY") == "1" {
		turns, copies = 300, 12
	}
	step := -1
	for i, sql := range schema {
		if strings.Contains(sql, "CREATE INDEX outbox_conv_lid") {
			step = i
		}
	}
	if step < 0 {
		t.Fatal("missing appended conversation/logical-ID index migration")
	}
	path := filepath.Join(t.TempDir(), "agent.db")
	db, err := sqlitedb.Open(path, schema[:step])
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := &store{db: db}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for turn := 0; turn < turns; turn++ {
		for copy := 0; copy < copies; copy++ {
			_, err = tx.Exec(`INSERT INTO outbox(id,recipient,body,envelope,state,created_at,conv,lid,kind,sub,ref_id,ref_fp,created_ms) VALUES(?,?,'{}','{}','delivered',1,'busy',?,'message','status',?,'key',?)`, fmt.Sprintf("copy-%03d-%02d", turn, copy), fmt.Sprintf("person/device-%d", copy), fmt.Sprintf("logical-%03d", turn), fmt.Sprintf("request-%03d", turn), turn+1)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	before, err := s.convControls("busy", "self/device", "self-key")
	if err != nil || len(before) != turns {
		t.Fatalf("before controls=%d err=%v", len(before), err)
	}
	beforeTime := time.Since(start)
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	start = time.Now()
	after, err := s.convControls("busy", "self/device", "self-key")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("upgrade changed logical controls: before=%d after=%d err=%v", len(before), len(after), err)
	}
	t.Logf("%d recipient copies / %d logical controls: old=%s indexed=%s", turns*copies, turns, beforeTime, time.Since(start))
	rows, err := s.db.Query(`EXPLAIN QUERY PLAN SELECT min(rowid) FROM outbox WHERE conv=? AND lid=?`, "busy", "logical-010")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	indexed := false
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err = rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		indexed = indexed || strings.Contains(detail, "outbox_conv_lid") && strings.Contains(detail, "conv=? AND lid=?")
	}
	if err = rows.Err(); err != nil || !indexed {
		t.Fatalf("dedup lacks indexed conv/lid lookup: indexed=%v err=%v", indexed, err)
	}
}
