package client

import (
	"path/filepath"
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
	if schema[len(schema)-1] != outboxLogicalIDSchema {
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
