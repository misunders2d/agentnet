package client

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// BUG-34: status of a message the Hub never got (kept waiting, failed,
// still queued) is this device's record with its reason, not a raw 404;
// a received message is said to be one, never "delivered"; an unknown id
// is said to be unknown here.
func TestStatusGivesLocalReasons(t *testing.T) {
	w := newWorld(t, "")
	for _, row := range []struct{ state, why string }{
		{stateConvWaiting, "bob/laptop needs to update AgentNet before it can read conversations"},
		{stateFailed, "hub: recipient has been revoked (403)"},
		{stateQueued, ""},
	} {
		id := protocol.NewID()
		if _, err := w.alice.store.db.Exec(`INSERT INTO outbox(id, recipient, body, envelope, state, error, created_at) VALUES(?, ?, 'x', '{}', ?, nullif(?, ''), ?)`,
			id, w.bob.Address, row.state, row.why, time.Now().Unix()); err != nil {
			t.Fatal(err)
		}
		r, err := w.alice.Status(tctx(t), id, 0)
		var local *LocalStatus
		if !errors.As(err, &local) || local.Cause != nil || r.ID != id || r.State != row.state || !strings.Contains(err.Error(), "not at the Hub") || !strings.Contains(err.Error(), row.why) {
			t.Fatalf("%s: %+v %v", row.state, r, err)
		}
	}
	runAgent(t, w.bob)
	sent, err := w.alice.Send(tctx(t), w.bob.Address, "for bob", "")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob to hold it", func() bool { m, _ := w.bob.store.inboxMessage(sent.ID); return m != nil })
	if r, err := w.bob.Status(tctx(t), sent.ID, 0); err == nil || !strings.Contains(err.Error(), "received here from "+w.alice.Address) {
		t.Fatalf("received: %+v %v", r, err)
	}
	if r, err := w.alice.Status(tctx(t), protocol.NewID(), 0); err == nil || strings.Contains(err.Error(), "(404)") || !strings.Contains(err.Error(), "no message") {
		t.Fatalf("unknown: %+v %v", r, err)
	}
}

// BUG-34: download says why there is nothing to save: the message has not
// arrived here (or the id is wrong), or it is one sent from here.
func TestDownloadGivesTrueReason(t *testing.T) {
	w := newWorld(t, "")
	path, _ := writeFile(t, t.TempDir(), "notes.txt", 100)
	sent, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "a file", Files: []string{path}})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if _, err := w.bob.Download(tctx(t), sent.ID, dir, false); err == nil || !strings.Contains(err.Error(), "not received here") {
		t.Fatalf("not arrived: %v", err)
	}
	if _, err := w.alice.Download(tctx(t), sent.ID, dir, false); err == nil || !strings.Contains(err.Error(), "sent from here") {
		t.Fatalf("own sent message: %v", err)
	}
	plain, err := w.alice.Send(tctx(t), w.bob.Address, "no files", "")
	if err != nil {
		t.Fatal(err)
	}
	runAgent(t, w.bob)
	eventually(t, "bob to hold it", func() bool { m, _ := w.bob.store.inboxMessage(plain.ID); return m != nil })
	if _, err := w.bob.Download(tctx(t), plain.ID, dir, false); err == nil || !strings.Contains(err.Error(), "has no attachments") {
		t.Fatalf("no files: %v", err)
	}
}
