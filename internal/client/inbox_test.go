package client

import (
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/gdrive"
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

// BUG-28 follow-up: `inbox` leaves records between devices out, yet still
// marks them read with what it lists, as it did when it listed them: a
// Drive space record stored unread never leaves the attention overview
// at one unread.
func TestInboxMarkReadClearsRecords(t *testing.T) {
	w := newWorld(t, "")
	for i, sub := range []string{"", envelope.SubDriveSpace} {
		id := strings.Repeat(string(rune('a'+i)), 32)
		if _, err := w.bob.store.db.Exec(`INSERT INTO inbox(id, sender, ts, kind, body, received_at, state, conv, sub) VALUES(?, ?, 1, 'message', ?, 1, '', ?, nullif(?, ''))`,
			id, w.alice.Address, "body "+sub, strings.Repeat("c", 64), sub); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.bob.Inbox(false, true); err != nil {
		t.Fatal(err)
	}
	var unread int
	if err := w.bob.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE read_at IS NULL`).Scan(&unread); err != nil {
		t.Fatal(err)
	}
	if ov, err := w.bob.overview(); err != nil || unread != 0 || !strings.Contains(ov, " 0 unread") {
		t.Fatalf("%d unread after inbox; overview %q %v", unread, ov, err)
	}
}

// BUG-28 follow-up: a Drive space record is stored read when it arrives,
// as group records are: it is never a message to read.
func TestDriveSpaceRecordArrivesRead(t *testing.T) {
	w, conv, _ := dmWithHistory(t)
	me, _, err := w.alice.Person()
	if err != nil {
		t.Fatal(err)
	}
	sp := gdrive.Space{Conv: conv, Folder: "read-folder", Name: "Read project", Owner: me.Person, Revision: 1}
	if err := w.alice.PublishDriveSpace(tctx(t), sp); err != nil {
		t.Fatal(err)
	}
	stored := func() (n, unread int) {
		w.bob.store.db.QueryRow(`SELECT count(*), coalesce(sum(read_at IS NULL), 0) FROM inbox WHERE sub = ?`, envelope.SubDriveSpace).Scan(&n, &unread)
		return
	}
	eventually(t, "the Drive space record at bob", func() bool { n, _ := stored(); return n == 1 })
	if _, unread := stored(); unread != 0 {
		t.Fatal("the Drive space record arrived unread")
	}
	unread, err := w.bob.ConvUnread()
	if err != nil || len(unread[conv]) != 3 {
		t.Fatalf("unread in the DM %v %v, want its 3 messages only", unread[conv], err)
	}
}
