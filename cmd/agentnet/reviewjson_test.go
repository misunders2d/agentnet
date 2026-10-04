package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
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

// The help says what inbox --review --json leaves out of what the text
// output lists (review finding 5), and that resolve, which sends no reply,
// still tells a requester that reads statuses the request was closed
// (review finding 6).
func TestHelpSaysWhatReviewJSONAndResolveDo(t *testing.T) {
	text := func(args ...string) string {
		t.Helper()
		var out bytes.Buffer
		if err := printHelp(&out, args); err != nil {
			t.Fatal(err)
		}
		return strings.Join(strings.Fields(out.String()), " ")
	}
	if inbox := text("inbox"); strings.Contains(inbox, "decided there, as the text output does") || !strings.Contains(inbox, "is not in it") {
		t.Fatalf("help inbox claims --review --json matches the text output:\n%s", inbox)
	}
	if strings.Contains(text(), "interrupted (nothing is sent)") {
		t.Fatal("help says resolve sends nothing, yet a requester that reads statuses is told")
	}
	if r := text("resolve"); strings.Contains(r, "It sends nothing and runs nothing") || !strings.Contains(r, "reads statuses is told it was closed") {
		t.Fatalf("help resolve:\n%s", r)
	}
}
