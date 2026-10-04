package main

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// BUG-24: inbox --review also lists what else waits here, each with where
// it is decided: a device asking to be linked, and a message held for a
// changed key (not one only waiting for proof, which is retried by
// itself). Before, review said nothing about them.
func TestReviewListsWhatElseWaits(t *testing.T) {
	a, home := diagnosticAgent(t)
	db, err := sql.Open("sqlite", filepath.Join(home, "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now().Unix()
	if _, err := db.Exec(`INSERT INTO device_links(offer, address, public, join_sig, requested_at, expires, state, updated_at)
		VALUES('33333333333333333333333333333333', 'diag/tablet', '{}', x'00', ?, ?, 'pending', ?)`, now, now+600, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO quarantine(id, sender, reason, envelope, received_at) VALUES
		('44444444444444444444444444444444', 'peer/device', 'key_changed', '{}', ?),
		('55555555555555555555555555555555', 'peer/device', 'proof_pending', '{}', ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	out, err := diagnosticOutput(t, func() error { return runInbox(a, []string{"--review"}) })
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"devices asking to be linked to your person (agentnet person approve ID, or person refuse ID):",
		"33333333333333333333333333333333  diag/tablet",
		"messages held here, not shown (nothing runs them):",
		"44444444444444444444444444444444  peer/device",
		"run agentnet trust peer/device once you verified the new key with them",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("inbox --review lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "55555555555555555555555555555555") {
		t.Fatalf("inbox --review lists a message only waiting for proof:\n%s", out)
	}
}
