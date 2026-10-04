package main

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"

	"github.com/misunders2d/agentnet/internal/client"
)

// BUG-30: inbox --review --json lists the reports from other machines (review
// notices) as the text output does, beside the items decided here; each is
// a message with status review_notice.
func TestReviewJSONIncludesNotices(t *testing.T) {
	a, home := diagnosticAgent(t)
	db, err := sql.Open("sqlite", filepath.Join(home, "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const taskID = "11111111111111111111111111111111"
	const noticeID = "22222222222222222222222222222222"
	if _, err := db.Exec(`INSERT INTO inbox(id, sender, ts, kind, body, received_at, state, status) VALUES
		(?, 'peer/device', 1, 'task', 'synthetic awaiting task', 1, 'awaiting', NULL),
		(?, 'peer/server', 2, 'message', '1 request(s) wait for a person''s decision on peer/server. Review there: agentnet inbox --review', 2, 'needs_human', 'review_notice')`,
		taskID, noticeID); err != nil {
		t.Fatal(err)
	}
	out, err := diagnosticOutput(t, func() error { return runInbox(a, []string{"--review", "--json"}) })
	if err != nil {
		t.Fatal(err)
	}
	var msgs []client.Message
	if err := json.Unmarshal([]byte(out), &msgs); err != nil {
		t.Fatalf("not a JSON list of messages: %v\n%s", err, out)
	}
	ids := []string{}
	for _, m := range msgs {
		ids = append(ids, m.ID)
		if m.ID == noticeID && m.Status != "review_notice" {
			t.Fatalf("the notice is not marked as one: %+v", m)
		}
	}
	if !slices.Contains(ids, taskID) || !slices.Contains(ids, noticeID) || len(ids) != 2 {
		t.Fatalf("inbox --review --json lists %v, want the task and the notice", ids)
	}
}
