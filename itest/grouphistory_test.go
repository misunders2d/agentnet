package itest

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestCLIGroupSelectedHistoryFile(t *testing.T) {
	c := buildCLI(t)
	addr, _ := c.setup(t, "history-hub")
	code := c.run("--home", "alice", "admin", "invite", "--raw", "history-caps")
	invitation, err := protocol.DecodeInvite(code)
	if err != nil {
		t.Fatal(err)
	}
	c.run("--home", "carol", "join", "--agent", "desk", c.run("--home", "alice", "admin", "invite", "--raw", "carol"))
	for _, home := range []string{"alice", "bob", "carol"} {
		c.start(home+"-history.log", "--home", home, "daemon")
		c.run("--home", home, "person", "create", home)
	}
	for home, address := range map[string]string{"alice": "admin/laptop", "bob": "bob/desk", "carol": "carol/desk"} {
		groupCLIFixtureCaps(t, c, addr, home, address, invitation.CertPEM)
	}
	persons := map[string]string{}
	for _, home := range []string{"bob", "carol"} {
		a, e := client.Open(filepath.Join(c.dir, home))
		if e != nil {
			t.Fatal(e)
		}
		p, ok, e := a.Person()
		a.Close()
		if e != nil || !ok {
			t.Fatal(e)
		}
		persons[home] = p.Person
	}
	var packet client.GroupContext
	if err = json.Unmarshal([]byte(c.run("--home", "alice", "group", "create", "Selected native history")), &packet); err != nil {
		t.Fatal(err)
	}
	conv := packet.Root.ID()
	defer func() {
		if !t.Failed() {
			return
		}
		// Capture before process cleanup. Only local status/count metadata
		// is emitted: never envelopes, consent bytes, identities or errors.
		for _, home := range []string{"alice", "bob"} {
			for _, line := range groupHistoryStateSummary(filepath.Join(c.dir, home, "agent.db"), conv) {
				t.Logf("group-state %s %s", home, line)
			}
		}
	}()
	var bobInvite client.GroupInvitationInfo
	if err = json.Unmarshal([]byte(c.run("--home", "alice", "group", "invite", conv, persons["bob"])), &bobInvite); err != nil || len(bobInvite.Proposal.History) != 0 {
		t.Fatal("default selected implicit history")
	}
	waitFor(t, "Bob pending native CLI invitation", func() bool { return strings.Contains(c.run("--home", "bob", "group", "invitations"), bobInvite.ID) })
	c.run("--home", "bob", "group", "accept", bobInvite.ID)
	waitFor(t, "Bob exact current group", func() bool {
		a, e := client.Open(filepath.Join(c.dir, "bob"))
		if e != nil {
			return false
		}
		defer a.Close()
		p, e := a.GroupContext(conv)
		return e == nil && p.State.Seq == 1
	})
	unselected := c.writeRandom("unselected.bin", 8193)
	_ = unselected
	c.run("--home", "alice", "dm", "send", "--file", "unselected.bin", conv, "UNSELECTED earlier private turn")
	waitFor(t, "Bob receives parent", func() bool {
		return strings.Contains(dmShowArriving(c, "bob", conv), "UNSELECTED earlier private turn")
	})
	data := c.writeRandom("selected.bin", 45001)
	c.run("--home", "bob", "dm", "send", "--file", "selected.bin", conv, "SELECTED exact historical turn")
	waitFor(t, "Alice receives selected source", func() bool { return strings.Contains(dmShow(c, "alice", conv), "SELECTED exact historical turn") })
	var sinceInvite, selected client.GroupInvitationInfo
	if err = json.Unmarshal([]byte(c.run("--home", "alice", "group", "invite", "--history-since", "2020-01-01T00:00:00Z", conv, persons["carol"])), &sinceInvite); err != nil || len(sinceInvite.Proposal.History) != 2 {
		t.Fatalf("since actual visible refs %v", err)
	}
	if err = json.Unmarshal([]byte(c.run("--home", "alice", "group", "invite", "--history-last", "1", conv, persons["carol"])), &selected); err != nil || len(selected.Proposal.History) != 1 {
		t.Fatalf("last actual visible ref %v", err)
	}
	raw, _ := json.Marshal(selected.Proposal.History)
	if err = os.WriteFile(filepath.Join(c.dir, "selected-refs.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	var explicit client.GroupInvitationInfo
	if err = json.Unmarshal([]byte(c.run("--home", "alice", "group", "invite", "--history-refs", "selected-refs.json", conv, persons["carol"])), &explicit); err != nil || explicit.ID != selected.ID {
		t.Fatal("explicit refs did not resolve original selection")
	}
	if _, err = c.try("--home", "alice", "group", "invite", "--history-last", "65", conv, persons["carol"]); err == nil {
		t.Fatal("unbounded selector accepted")
	}
	waitFor(t, "Carol exact pending selection", func() bool { return strings.Contains(c.run("--home", "carol", "group", "invitations"), selected.ID) })
	// Before accepting, the group is not Carol's conversation: dm show says
	// so, or lists nothing, and never leaks history or file metadata.
	if text, err := c.try("--home", "carol", "dm", "show", conv); (err != nil && !strings.Contains(text, "no such conversation here")) ||
		strings.Contains(text, "earlier private turn") || strings.Contains(text, "historical turn") || strings.Contains(text, "selected.bin") {
		t.Fatalf("preaccept history/file metadata leaked or unexpected failure: %v\n%s", err, text)
	}
	c.run("--home", "carol", "group", "accept", selected.ID)
	var imported client.ConvMessage
	waitFor(t, "Carol only selected native history", func() bool {
		a, e := client.Open(filepath.Join(c.dir, "carol"))
		if e != nil {
			return false
		}
		defer a.Close()
		rows, e := a.ConversationMessages(conv)
		if e != nil || len(rows) != 1 {
			return false
		}
		imported = rows[0]
		return imported.Body == "SELECTED exact historical turn" && imported.History && imported.Key == "" && len(imported.Attachments) == 1
	})
	if text := dmShow(c, "carol", conv); strings.Contains(text, "UNSELECTED") || strings.Contains(text, "unselected.bin") {
		t.Fatal("unselected history reached CLI")
	}
	if _, err = c.try("--home", "alice", "group", "request-file", conv, imported.ID, "0"); err == nil {
		t.Fatal("outbox/direct message became scoped history request")
	}
	if _, err = c.try("--home", "carol", "group", "request-file", strings.Repeat("a", 64), imported.ID, "0"); err == nil {
		t.Fatal("wrong group request accepted")
	}
	if _, err = c.try("--home", "carol", "group", "request-file", conv, imported.ID, "1"); err == nil {
		t.Fatal("wrong file index accepted")
	}
	if out := c.run("--home", "carol", "group", "request-file", conv, imported.ID, "0"); !strings.Contains(out, "requested") || strings.Contains(out, "downloaded") {
		t.Fatal("file request claimed download")
	}
	waitFor(t, "selected native file offer", func() bool {
		a, e := client.Open(filepath.Join(c.dir, "carol"))
		if e != nil {
			return false
		}
		defer a.Close()
		rows, e := a.ConversationMessages(conv)
		return e == nil && len(rows) == 1 && len(rows[0].Attachments) == 1 && !strings.HasPrefix(rows[0].Attachments[0].BlobID, "history-")
	})
	got := c.downloadInto("carol", "selected-download", imported.ID)
	if !bytes.Equal(got, data) {
		t.Fatal("CLI selected file bytes differ")
	}
	c.run("--home", "carol", "dm", "send", conv, "new ordinary future turn")
	waitFor(t, "normal future group turns unchanged", func() bool { return strings.Contains(dmShow(c, "bob", conv), "new ordinary future turn") })
}

// The diagnostic reader must not migrate the store or modify its journal.
// The exact group is bound as a query argument and is never printed.
func groupHistoryStateSummary(path, conv string) []string {
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro&_pragma=busy_timeout(100)")
	if err != nil {
		return []string{"database unavailable"}
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var result []string
	for _, query := range []struct{ label, sql string }{
		{"invitation", `SELECT direction||':'||state,count(*) FROM group_invitations WHERE conv=? GROUP BY direction,state ORDER BY direction,state`},
		{"outbound-control", `SELECT sub||':'||state||CASE WHEN coalesce(error,'')<>'' THEN ':has-error' ELSE '' END,count(*) FROM outbox WHERE conv=? AND sub IN('group-invite','group-consent','group-proof','group-context') GROUP BY sub,state,coalesce(error,'')<>'' ORDER BY sub,state`},
		{"admitted-control", `SELECT sub,count(*) FROM inbox WHERE conv=? AND sub IN('group-invite','group-consent','group-proof','group-context') GROUP BY sub ORDER BY sub`},
		{"publication", `SELECT 'seq='||seq||':published='||published,count(*) FROM group_publications WHERE conv=? GROUP BY seq,published ORDER BY seq`},
		{"context", `SELECT 'seq='||json_extract(payload,'$.state.seq'),count(*) FROM group_context WHERE conv=? GROUP BY json_extract(payload,'$.state.seq')`},
		{"proof", `SELECT 'seq='||seq,count(*) FROM group_proof_records WHERE conv=? GROUP BY seq ORDER BY seq`},
		{"known-head", `SELECT 'seq='||seq,count(*) FROM group_known_heads WHERE conv=? GROUP BY seq ORDER BY seq`},
	} {
		rows, err := db.QueryContext(ctx, query.sql, conv)
		if err != nil {
			result = append(result, query.label+" unavailable")
			continue
		}
		found := false
		for rows.Next() {
			var state string
			var count int
			if err = rows.Scan(&state, &count); err != nil {
				break
			}
			found = true
			result = append(result, fmt.Sprintf("%s %s count=%d", query.label, state, count))
		}
		if err != nil || rows.Err() != nil {
			result = append(result, query.label+" unavailable")
		} else if !found {
			result = append(result, query.label+" count=0")
		}
		rows.Close()
	}
	return result
}

func TestGroupHistoryStateSummaryRedactsAndDoesNotWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
CREATE TABLE group_invitations(conv, direction, state, consent);
CREATE TABLE outbox(conv, sub, state, error);
CREATE TABLE inbox(conv, sub);
CREATE TABLE group_publications(conv, seq, published);
CREATE TABLE group_context(conv, payload);
CREATE TABLE group_proof_records(conv, seq);
CREATE TABLE group_known_heads(conv, seq);
INSERT INTO group_invitations VALUES('private-id','in','accepted','private-consent');
INSERT INTO outbox VALUES('private-id','group-consent','custody','private-error');
INSERT INTO inbox VALUES('private-id','group-context');
INSERT INTO group_publications VALUES('private-id',1,0);
INSERT INTO group_context VALUES('private-id','{"state":{"seq":1},"private":"private-payload"}');
INSERT INTO group_proof_records VALUES('private-id',0);
INSERT INTO group_invitations VALUES('another-group','out','pending','other-private-consent');`)
	closeErr := db.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("diagnostic fixture: %v / %v", err, closeErr)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(groupHistoryStateSummary(path, "private-id"), "\n")
	for _, want := range []string{"invitation in:accepted count=1", "outbound-control group-consent:custody:has-error count=1", "admitted-control group-context count=1", "publication seq=1:published=0 count=1", "context seq=1 count=1", "proof seq=0 count=1", "known-head count=0"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing diagnostic %q: %s", want, text)
		}
	}
	if strings.Contains(text, "private-") || strings.Contains(text, "pending") || strings.Contains(text, "unavailable") {
		t.Fatalf("diagnostic leaked content, another group, or failed: %s", text)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("diagnostic changed database: %v", err)
	}
}
