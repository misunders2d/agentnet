package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// BUG-28: inbox and dm show word a participation event instead of
// printing its signed JSON.
func TestParticipationEventsPrintedAsText(t *testing.T) {
	a, home := diagnosticAgent(t)
	db, err := sql.Open("sqlite", filepath.Join(home, "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conv, pid := strings.Repeat("c", 64), strings.Repeat("d", 32)
	ev := protocol.ParticipationEvent{V: 1, Conv: conv, PID: pid, Type: protocol.EventInvite, TS: 1, Audience: protocol.AudienceConversation,
		Author: protocol.EventAuthor{Person: strings.Repeat("a", 32), Roster: strings.Repeat("b", 64), Address: "anna/desk", Fingerprint: "11111111-22222222-33333333-44444444"},
		Host:   &protocol.ParticipationHost{Person: strings.Repeat("e", 32), Address: "bohdan/desk", Fingerprint: "55555555-66666666-77777777-88888888"},
		Sig:    []byte("signature")}
	body, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := protocol.ParseParticipationEvent(body); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO inbox(id, sender, ts, kind, body, received_at, received_ms, state, conv, lid, sub, verified_by, pid)
		VALUES(?, 'anna/desk', 1, 'message', ?, 1, 1000, '', ?, ?, 'event', '11111111-22222222-33333333-44444444', ?)`,
		strings.Repeat("f", 32), string(body), conv, strings.Repeat("9", 32), pid); err != nil {
		t.Fatal(err)
	}
	want := "anna/desk invited the agent on bohdan/desk (participation " + pid + ")"
	inbox, err := diagnosticOutput(t, func() error { return runInbox(a, []string{"--peek"}) })
	if err != nil || strings.Contains(inbox, `"sig"`) || !strings.Contains(inbox, want) {
		t.Fatalf("inbox: %q, %v", inbox, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var show bytes.Buffer
	if err := runDM(ctx, a, []string{"show", conv}, &show); err != nil || strings.Contains(show.String(), `"sig"`) || !strings.Contains(show.String(), want) {
		t.Fatalf("dm show: %q, %v", show.String(), err)
	}
}
