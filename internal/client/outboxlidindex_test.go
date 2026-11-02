package client

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The overview and conversation views look up each shown request, proposal and
// sent message by logical ID alone. Without an index on outbox.lid each lookup
// read every outbox row with its inline envelope: on the owner's 196 MB store
// an overview took seconds and the daemon held a full core (November 1, 2026).
// Each lookup shape below is the one its function runs; all must search.
func TestOutboxLogicalIDLookupsSearchIndex(t *testing.T) {
	s, err := openStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	if i := slices.Index(schema, outboxLogicalIDSchema); i < 0 || schema[i+1] != outboxConvSubSchema {
		t.Fatal("outbox_lid must be an appended schema step")
	}
	for name, q := range map[string]string{
		"remoteRequestFor":      `SELECT count(*) FROM outbox WHERE id=? OR lid=?`,
		"SentCopies":            `SELECT id, recipient, state FROM outbox WHERE lid = ? AND conv IS NOT NULL ORDER BY rowid`,
		"confirmableProposalIn": `SELECT coalesce(target,''),coalesce(pid,''),coalesce(topic,'') FROM outbox WHERE (id=? OR lid=?) AND coalesce(conv,'')=? AND ref_id IS NULL AND coalesce(kind,json_extract(envelope,'$.kind'))=? AND (?<>'' OR recipient=? OR substr(recipient,1,length(?)+1)=?||'#')`,
		"retainedAgentRequest":  `SELECT conv,lid FROM outbox WHERE (id=? OR lid=?) AND conv IS NOT NULL AND ref_id IS NULL AND coalesce(sub,'')='' ORDER BY rowid LIMIT 1`,
	} {
		args := make([]any, strings.Count(q, "?"))
		for i := range args {
			args[i] = "x"
		}
		rows, err := s.db.Query("EXPLAIN QUERY PLAN "+q, args...)
		if err != nil {
			t.Fatal(name, err)
		}
		var plan []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err = rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(name, err)
			}
			plan = append(plan, detail)
		}
		rows.Close()
		joined := strings.Join(plan, "; ")
		if strings.Contains(joined, "SCAN outbox") || !strings.Contains(joined, "outbox_lid") {
			t.Errorf("%s reads every outbox row: %s", name, joined)
		}
	}
}

// A conversation view skips history, sync and proof copies by sub. The table
// row stores sub after the inline envelope, so a filter there read each skipped
// copy's envelope: in the owner's biggest chat 3,169 of 3,266 outbox rows were
// history copies, and every chat list rebuild read them all (November 1, 2026).
// With sub in the index, SQLite rejects those copies before reading the row.
func TestConversationViewSkipsCopiesInIndex(t *testing.T) {
	s, err := openStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	if i := slices.Index(schema, outboxConvSubSchema); i < 1 || schema[i-1] != outboxLogicalIDSchema {
		t.Fatal("outbox_conv_sub must be an appended schema step")
	}
	q := `SELECT o.id FROM outbox o LEFT JOIN inbox j ON j.id = o.id AND j.local = 1 WHERE o.conv = ? AND coalesce(o.sub, '') NOT IN ('read-sync', 'root-sync', 'history', 'file', 'drive-space', 'group-proof', 'group-context','group-invite','group-consent','group-withdrawal') AND o.ref_id IS NULL AND NOT ` + erasedOut
	args := make([]any, strings.Count(q, "?"))
	for i := range args {
		args[i] = "x"
	}
	rows, err := s.db.Query("EXPLAIN QUERY PLAN "+q, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err = rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	if joined := strings.Join(plan, "; "); !strings.Contains(joined, "SEARCH o USING INDEX outbox_conv_sub (conv=?)") {
		t.Errorf("conversation view reads skipped copies from the table: %s", joined)
	}
}
