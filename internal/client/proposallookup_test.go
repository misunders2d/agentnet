package client

import (
	"path/filepath"
	"strings"
	"testing"
)

// A chat's task or proposal row runs these checks each time the chat opens. Each
// finds its few rows by id, logical id or reply, never by reading a table or the
// whole chat (MEL-748: half of opening a chat with 10x history).
func TestProposalLookupsFindTheirRowsByIndex(t *testing.T) {
	s, err := openStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	for _, statement := range []string{
		`SELECT id FROM inbox WHERE ` + convRowIs("c") + ` AND kind='answer' AND status='proposal'`,
		`SELECT id FROM outbox WHERE ` + convRowIs("c") + ` AND ?=?`,
		`SELECT body FROM inbox WHERE ` + convIs("c") + ` AND (id=? OR lid=?) AND kind='question'`,
		`SELECT body FROM outbox WHERE ` + convIs("c") + ` AND (id=? OR lid=?)`,
		`SELECT id FROM inbox WHERE ` + convIs("c") + ` AND kind='task' AND reply_to=? ORDER BY received_at,id`,
		`SELECT id FROM outbox WHERE ` + convIs("c") + ` AND ref_id IS NULL AND reply_to=? AND body=? ORDER BY rowid LIMIT 1`,
	} {
		args := make([]any, strings.Count(statement, "?"))
		for i := range args {
			args[i] = "x"
		}
		rows, err := s.db.Query(`EXPLAIN QUERY PLAN `+statement, args...)
		if err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
		for rows.Next() {
			var id, parent, unused int
			var plan string
			if err = rows.Scan(&id, &parent, &unused, &plan); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(plan, "SCAN") || strings.Contains(plan, "_conv") {
				t.Errorf("%s\nreads a whole table or chat: %s", statement, plan)
			}
		}
		rows.Close()
	}
}
