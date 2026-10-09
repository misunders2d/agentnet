package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/client"
)

func TestInboxDefaultOutputIsBounded(t *testing.T) {
	a, home := diagnosticAgent(t)
	db, err := sql.Open("sqlite", filepath.Join(home, "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const id = "11111111111111111111111111111111"
	if _, err := db.Exec(`INSERT INTO inbox(id,sender,ts,kind,body,received_at,state,detail) VALUES(?, 'peer/device',1,'task',?,1,'needs_human',?)`, id, strings.Repeat("large body ", 10000), strings.Repeat("large detail ", 10000)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		if _, err := db.Exec(`INSERT INTO attachments(message_id,blob_id,name,size,sha256,ct_size,ct_sha256,saved_path) VALUES(?,?,?,1,?,1,?,?)`, id, fmt.Sprint(i), strings.Repeat("file", 10000), strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("/path", 10000)); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"--peek"}, {"--peek", "--json"}, {"--review"}, {"--review", "--json"}} {
		out, err := diagnosticOutput(t, func() error { return runInbox(a, args) })
		if err != nil {
			t.Fatal(err)
		}
		if len(out) >= 8000 {
			t.Fatalf("inbox %v captured %d bytes, want less than native lookup limit", args, len(out))
		}
		if !strings.Contains(out, id) {
			t.Fatalf("inbox %v omitted the actionable message id", args)
		}
		if !strings.Contains(out, "summary") && !strings.Contains(out, "truncated") {
			t.Fatalf("inbox %v silently clipped full content", args)
		}
	}
}

func TestInboxEscapedLargeRecordStillReturnsUsefulSummary(t *testing.T) {
	a, home := diagnosticAgent(t)
	db, err := sql.Open("sqlite", filepath.Join(home, "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const id = "11111111111111111111111111111111"
	if _, err := db.Exec(`INSERT INTO inbox(id,sender,ts,kind,body,received_at,state,detail) VALUES(?,'peer/device',1,'task',?,1,'needs_human',?)`, id, strings.Repeat("\x1b", 10000), strings.Repeat("\x1b", 10000)); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"--peek"}, {"--review"}, {"--peek", "--json"}} {
		out, err := diagnosticOutput(t, func() error { return runInbox(a, args) })
		if err != nil || len(out) >= 8000 || !strings.Contains(out, id) {
			t.Fatalf("escaped record: %d bytes %v", len(out), err)
		}
	}
}

func TestInboxSummaryPagesMarkOnlyPrintedAndExactFullIsReadOnly(t *testing.T) {
	a, home := diagnosticAgent(t)
	db, err := sql.Open("sqlite", filepath.Join(home, "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for i := 0; i < 30; i++ {
		if _, err := db.Exec(`INSERT INTO inbox(id,sender,ts,kind,body,received_at,state) VALUES(?,'peer/device',1,'task',?,1,'awaiting')`, fmt.Sprintf("%032x", i+1), strings.Repeat("body", 10000)); err != nil {
			t.Fatal(err)
		}
	}
	var printed []client.Message
	out, err := diagnosticOutput(t, func() error { return runInbox(a, []string{"--json", "--limit", "20"}) })
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(out), &printed); err != nil || len(printed) == 0 || len(printed) >= 20 || len(out) >= 8000 {
		t.Fatalf("bounded valid JSON: %d records, %d bytes: %v", len(printed), len(out), err)
	}
	var read int
	if err := db.QueryRow(`SELECT count(*) FROM inbox WHERE read_at IS NOT NULL`).Scan(&read); err != nil || read != len(printed) {
		t.Fatalf("marked off-page messages read: %d/%d %v", read, len(printed), err)
	}
	id := fmt.Sprintf("%032x", 1)
	name, path := strings.Repeat("long-name", 100), strings.Repeat("/stored-path", 100)
	if _, err := db.Exec(`INSERT INTO attachments(message_id,blob_id,name,size,sha256,ct_size,ct_sha256,saved_path) VALUES(?,'blob',?,1,?,1,?,?)`, id, name, strings.Repeat("a", 64), strings.Repeat("b", 64), path); err != nil {
		t.Fatal(err)
	}
	out, err = diagnosticOutput(t, func() error { return runInbox(a, []string{"--id", id, "--full", "--json"}) })
	if err != nil {
		t.Fatal(err)
	}
	var full []client.Message
	if err := json.Unmarshal([]byte(out), &full); err != nil || len(full) != 1 || full[0].ID != id || full[0].Body != strings.Repeat("body", 10000) {
		t.Fatalf("exact full body changed: %v", err)
	}
	if len(full[0].Attachments) != 1 || full[0].Attachments[0].Name != name || full[0].Attachments[0].SavedPath != path {
		t.Fatal("exact full lookup clipped file metadata")
	}
	var unchanged bool
	if err := db.QueryRow(`SELECT read_at IS NULL AND state='awaiting' FROM inbox WHERE id=?`, id).Scan(&unchanged); err != nil || !unchanged {
		t.Fatalf("exact read mutated task: %v", err)
	}
}

func TestInboxReviewSectionsBoundedAndContinue(t *testing.T) {
	a, home := diagnosticAgent(t)
	db, err := sql.Open("sqlite", filepath.Join(home, "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for i := 0; i < 80; i++ {
		if _, err := db.Exec(`INSERT INTO quarantine(id,sender,reason,envelope,received_at) VALUES(?,'peer/device','key_changed','{}',1)`, fmt.Sprintf("%032x", i+1)); err != nil {
			t.Fatal(err)
		}
	}
	out, err := diagnosticOutput(t, func() error { return runInbox(a, []string{"--review"}) })
	if err != nil || len(out) >= 8000 || !strings.Contains(out, "--section held") || !strings.Contains(out, fmt.Sprintf("%032x", 80)) {
		t.Fatalf("bounded actionable review: %q %v", out, err)
	}
	first, err := a.InspectInboxNotices("held", client.InboxPageOptions{Limit: 3})
	if err != nil || len(first.Items) != 3 || first.Next == "" {
		t.Fatalf("first notice page: %+v %v", first, err)
	}
	out, err = diagnosticOutput(t, func() error {
		return runInbox(a, []string{"--section", "held", "--before", first.Next, "--limit", "3", "--json"})
	})
	if err != nil {
		t.Fatal(err)
	}
	var next []client.InboxNotice
	if err := json.Unmarshal([]byte(out), &next); err != nil || len(next) != 3 || next[0].ID != fmt.Sprintf("%032x", 77) {
		t.Fatalf("notice continuation: %q %v", out, err)
	}
	var retained int
	if err := db.QueryRow(`SELECT count(*) FROM quarantine`).Scan(&retained); err != nil || retained != 80 {
		t.Fatalf("review changed held rows: %d %v", retained, err)
	}
}
