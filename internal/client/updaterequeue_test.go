package client

import (
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// A program before v0.8.17 took the Hub's 426 update_required on a send as
// final and failed the row ("hub: update_required (426)", or the same met
// by a file's upload). Once this device runs the updated program, its
// daemon start queues exactly those rows again, whose files are still
// spooled, and they are delivered: the same sealed envelopes, nothing run
// again. A row failed for anything else, one whose file is gone, and one
// the person stopped stay as they are; a second pass changes nothing.
func TestOldUpdateRefusalsQueuedAgain(t *testing.T) {
	w := newWorld(t, "")
	if _, err := w.bob.Send(tctx(t), w.alice.Address, "before", ""); err != nil { // alice's key pinned here
		t.Fatal(err)
	}
	base := w.bob.hub.http.Transport
	w.bob.hub.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) { // offline, as when the old program ran
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("unreachable")}
	})
	file := func(name string) string {
		p := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(p, []byte("contents of "+name), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	queued := func(body string, files ...string) string {
		t.Helper()
		res, err := w.bob.Send(tctx(t), w.alice.Address, body, "", files...)
		if err != nil || res.State != stateQueued {
			t.Fatalf("send %q offline: %+v %v", body, res, err)
		}
		return res.ID
	}
	refused := queued("typed while offline")
	refusedFile := queued("with a file", file("plan.txt"))
	other := queued("failed for another reason")
	gone := queued("its file was cleaned up", file("gone.txt"))
	stopped := queued("stopped by the person")
	w.bob.hub.http.Transport = base
	set := func(id, errText string, stoppedToo bool) {
		t.Helper()
		if _, err := w.bob.store.db.Exec(`UPDATE outbox SET state = ?, error = ?, send_stopped = ? WHERE id = ?`, stateFailed, errText, stoppedToo, id); err != nil {
			t.Fatal(err)
		}
	}
	set(refused, "hub: update_required (426)", false)
	set(refusedFile, "attachment upload: hub: update_required (426)", false)
	set(other, "hub: recipient revoked (403)", false)
	set(gone, "attachment upload: hub: update_required (426)", false)
	set(stopped, "hub: update_required (426)", true)
	env, err := w.bob.store.outboxEnvelope(gone)
	if err != nil || len(env.Blobs) != 1 {
		t.Fatalf("envelope of %s: %+v %v", gone, env, err)
	}
	if err := os.Remove(w.bob.spoolPath(env.Blobs[0].ID)); err != nil {
		t.Fatal(err)
	}
	sealed := map[string]envelope.Envelope{}
	for _, id := range []string{refused, refusedFile} {
		if sealed[id], err = w.bob.store.outboxEnvelope(id); err != nil {
			t.Fatal(err)
		}
	}
	runWith(t, w, w.bob, RunOptions{}) // the updated program's daemon starts
	for _, id := range []string{refused, refusedFile} {
		eventually(t, id+" at the Hub once updated", func() bool {
			st, _, _, _ := w.bob.store.outboxState(id)
			return st == protocol.StateCustody || st == protocol.StateDelivered
		})
	}
	for _, id := range []string{other, gone} {
		if st, _, _, _ := w.bob.store.outboxState(id); st != stateFailed {
			t.Errorf("%s: %q, want still failed", id, st)
		}
	}
	if st, _, _, _ := w.bob.store.outboxState(stopped); st != stateFailed {
		t.Errorf("stopped %s: %q", stopped, st)
	}
	if n, err := w.bob.requeueOldUpdateRefusals(); err != nil || n != 0 {
		t.Fatalf("a second pass: %d %v", n, err)
	}
	for id, before := range sealed {
		if after, err := w.bob.store.outboxEnvelope(id); err != nil || after.ID != before.ID || string(after.CT) != string(before.CT) || string(after.Sig) != string(before.Sig) {
			t.Errorf("%s was sent as another envelope: %v", id, err)
		}
	}
}
