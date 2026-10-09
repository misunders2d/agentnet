package client

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestRequestFollowupClaimSkipsBlockedRows(t *testing.T) {
	t.Run("device", func(t *testing.T) {
		w := newWorld(t, "")
		if err := w.bob.Approve(w.alice.Address); err != nil {
			t.Fatal(err)
		}
		store := func(ref *envelope.Ref) string {
			t.Helper()
			in := envelope.Inner{V: envelope.Version, ID: protocol.NewID(), From: w.alice.Address, To: w.bob.Address, TS: time.Now().Unix(), Kind: envelope.KindQuestion, Body: "claim regression", Followup: ref}
			if ref != nil {
				in.ReplyTo = ref.ID
			}
			recipient, _ := w.bob.Self().Recipient()
			env, err := envelope.Seal(in, w.alice.id.Sign, recipient)
			if err != nil {
				t.Fatal(err)
			}
			if err = w.bob.verifyAndStore(tctx(t), env); err != nil {
				t.Fatal(err)
			}
			return in.ID
		}
		original := store(nil)
		blocked := store(&envelope.Ref{ID: original, Fingerprint: w.alice.Self().Fingerprint()})
		ordinary := store(nil)
		for i, id := range []string{original, blocked, ordinary} {
			state := stateAccepted
			if id == original {
				state = stateRunning
			}
			if _, err := w.bob.store.db.Exec(`UPDATE inbox SET state=?,received_at=? WHERE id=?`, state, i+1, id); err != nil {
				t.Fatal(err)
			}
		}
		j, found, err := w.bob.store.claimJob("fixture")
		if err != nil || !found || j.ID != ordinary || jobState(t, w.bob, blocked) != stateAccepted {
			t.Fatalf("blocked correction starved ordinary request: %s %v %v", j.ID, found, err)
		}
		if _, err = w.bob.store.db.Exec(`UPDATE inbox SET state=? WHERE id=?`, stateAnswered, original); err != nil {
			t.Fatal(err)
		}
		j, found, err = w.bob.store.claimJob("fixture")
		if err != nil || !found || j.ID != blocked {
			t.Fatalf("completed predecessor failed to release correction: %s %v %v", j.ID, found, err)
		}
	})
	t.Run("participation_page", func(t *testing.T) {
		w, conv, _, stopBob := agentWorld(t)
		if err := w.bob.Approve(w.alice.Address); err != nil {
			t.Fatal(err)
		}
		pid := participate(t, w, conv, nil, nil)
		request, err := w.alice.AskAgent(tctx(t), pid, envelope.KindQuestion, "original")
		if err != nil {
			t.Fatal(err)
		}
		eventually(t, "original request stored", func() bool { return jobState(t, w.bob, request.ID) == stateAgentWaiting })
		stopBob()
		var raw string
		if err = w.alice.store.db.QueryRow(`SELECT envelope FROM outbox WHERE id=?`, request.ID).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var env envelope.Envelope
		if err = json.Unmarshal([]byte(raw), &env); err != nil {
			t.Fatal(err)
		}
		base, err := envelope.Open(env, w.bob.id, w.bob.Address, w.alice.Self())
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w.bob.store.db.Exec(`UPDATE inbox SET state=? WHERE id=?`, stateRunning, request.ID); err != nil {
			t.Fatal(err)
		}
		var blocked []string
		var ordinary string
		for i := 0; i < agentPage+2; i++ {
			in := base
			in.ID, in.LID = protocol.NewID(), protocol.NewID()
			in.ReplyTo = request.LID
			in.Followup = &envelope.Ref{ID: request.LID, Fingerprint: w.alice.Self().Fingerprint()}
			if i == agentPage+1 {
				in.ReplyTo, in.Followup = "", nil
				ordinary = in.ID
			} else {
				blocked = append(blocked, in.ID)
			}
			recipient, _ := w.bob.Self().Recipient()
			sealed, err := envelope.Seal(in, w.alice.id.Sign, recipient)
			if err != nil {
				t.Fatal(err)
			}
			if err = w.bob.verifyAndStore(tctx(t), sealed); err != nil {
				t.Fatal(err)
			}
		}
		_, found, next, full, err := w.bob.store.claimAgentPage("fixture", w.bob.Address, w.bob.Self().Fingerprint(), 0, agentPage)
		if err != nil || found || !full || next == 0 {
			t.Fatalf("blocked page failed to advance: found=%v next=%d full=%v err=%v", found, next, full, err)
		}
		j, found, _, full, err := w.bob.store.claimAgentPage("fixture", w.bob.Address, w.bob.Self().Fingerprint(), next, agentPage)
		if err != nil || !found || full || j.ID != ordinary {
			t.Fatalf("blocked page starved next ordinary request: %s found=%v full=%v err=%v", j.ID, found, full, err)
		}
		for _, id := range blocked {
			if jobState(t, w.bob, id) != stateAgentWaiting {
				t.Fatalf("blocked correction %s changed before predecessor completion", id)
			}
		}
		if _, err = w.bob.store.db.Exec(`UPDATE inbox SET state=? WHERE id=?`, stateAnswered, request.ID); err != nil {
			t.Fatal(err)
		}
		j, found, _, _, err = w.bob.store.claimAgentPage("fixture", w.bob.Address, w.bob.Self().Fingerprint(), 0, agentPage)
		if err != nil || !found || j.ID != blocked[0] {
			t.Fatalf("resweep did not release oldest correction: %s found=%v err=%v", j.ID, found, err)
		}
		if _, found, _, _, err = w.bob.store.claimAgentPage("fixture", w.bob.Address, w.bob.Self().Fingerprint(), 0, agentPage); err != nil || found {
			t.Fatal("later correction passed its running predecessor", found, err)
		}
	})
}
