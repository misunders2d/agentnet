package client

import (
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
)

// BUG-29: messages that arrive within one second are listed as they
// arrived, not by their random ids.
func TestInboxListsSameSecondArrivalsInOrder(t *testing.T) {
	w := newWorld(t, "")
	// Descending ids: listing by id would reverse them.
	ids := []string{strings.Repeat("f", 32), strings.Repeat("e", 32), strings.Repeat("d", 32), strings.Repeat("c", 32)}
	for i, id := range ids {
		in := envelope.Inner{ID: id, From: w.alice.Address, To: w.bob.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage, Body: "seq-" + string(rune('1'+i))}
		if err := w.bob.store.addInbox(in, w.alice.Self().Fingerprint()); err != nil {
			t.Fatal(err)
		}
	}
	// All in one second; the last two carry milliseconds, as conversation
	// messages do.
	if _, err := w.bob.store.db.Exec(`UPDATE inbox SET received_at = 1000, received_ms = CASE id WHEN ? THEN 1000500 WHEN ? THEN 1000900 END`, ids[2], ids[3]); err != nil {
		t.Fatal(err)
	}
	msgs, err := w.bob.Inbox(false, false)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range msgs {
		got = append(got, m.Body)
	}
	if strings.Join(got, " ") != "seq-1 seq-2 seq-3 seq-4" {
		t.Fatalf("inbox order %v", got)
	}
}

// BUG-28: records between devices (group proofs, contexts, invitations,
// consents, withdrawals, Drive space records) are never listed as
// messages; a participation event is (the CLI words it).
func TestInboxLeavesOutProtocolRecords(t *testing.T) {
	w := newWorld(t, "")
	subs := []string{"", envelope.SubEvent, envelope.SubGroupProof, envelope.SubGroupContext, envelope.SubGroupInvite,
		envelope.SubGroupConsent, envelope.SubGroupWithdrawal, envelope.SubDriveSpace}
	for i, sub := range subs {
		id := strings.Repeat(string(rune('a'+i)), 32)
		if _, err := w.bob.store.db.Exec(`INSERT INTO inbox(id, sender, ts, kind, body, received_at, state, conv, sub) VALUES(?, ?, 1, 'message', ?, 1, '', ?, nullif(?, ''))`,
			id, w.alice.Address, "body "+sub, strings.Repeat("c", 64), sub); err != nil {
			t.Fatal(err)
		}
	}
	msgs, err := w.bob.Inbox(false, false)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range msgs {
		got = append(got, m.Body)
	}
	if strings.Join(got, "|") != "body |body event" {
		t.Fatalf("inbox lists %q", got)
	}
}

// BUG-29, across formats: an older-format message stored after a
// conversation message within the same second is listed after it, so an
// older-format row carries its arrival to the millisecond too.
func TestInboxOrdersSameSecondAcrossFormats(t *testing.T) {
	w := newWorld(t, "")
	// Start well inside a second, so both arrive within it.
	for ms := time.Now().Nanosecond() / 1e6; ms < 100 || ms > 700; ms = time.Now().Nanosecond() / 1e6 {
		time.Sleep(10 * time.Millisecond)
	}
	now := time.Now()
	if _, err := w.bob.store.db.Exec(`INSERT INTO inbox(id, sender, ts, kind, body, received_at, received_ms, state, conv) VALUES(?, ?, ?, 'message', 'conv-first', ?, ?, '', ?)`,
		strings.Repeat("f", 32), w.alice.Address, now.Unix(), now.Unix(), now.UnixMilli(), strings.Repeat("c", 64)); err != nil {
		t.Fatal(err)
	}
	in := envelope.Inner{ID: strings.Repeat("e", 32), From: w.alice.Address, To: w.bob.Address, TS: now.Unix(), Kind: envelope.KindMessage, Body: "legacy-second"}
	if err := w.bob.store.addInbox(in, w.alice.Self().Fingerprint()); err != nil {
		t.Fatal(err)
	}
	msgs, err := w.bob.Inbox(false, false)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range msgs {
		got = append(got, m.Body)
	}
	if strings.Join(got, " ") != "conv-first legacy-second" {
		t.Fatalf("inbox order %v", got)
	}
}
