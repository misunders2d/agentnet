package client

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
)

func TestTrustKeyReplaysHeldMessageControl(t *testing.T) {
	for _, sub := range []string{envelope.SubRevision, envelope.SubRetraction} {
		t.Run(sub, func(t *testing.T) {
			w := newWorld(t, "")
			ctx := tctx(t)
			bob := w.bob
			original, err := w.alice.Send(ctx, bob.Address, "original text", "")
			if err != nil {
				t.Fatal(err)
			}
			receive := func(id string) string {
				t.Helper()
				env, err := w.alice.store.outboxEnvelope(id)
				if err != nil {
					t.Fatal(err)
				}
				if err := bob.verifyAndStore(ctx, env); err != nil {
					t.Fatal(err)
				}
				raw, err := json.Marshal(env)
				if err != nil {
					t.Fatal(err)
				}
				return string(raw)
			}
			if sub == envelope.SubRetraction {
				receive(original.ID)
			}
			ref, err := w.alice.RefOf("", original.ID, "out")
			if err != nil {
				t.Fatal(err)
			}
			stale, err := identity.Generate()
			if err != nil {
				t.Fatal(err)
			}
			old := stale.Public(w.alice.Address)
			if err := bob.store.pin(old); err != nil {
				t.Fatal(err)
			}
			var sent ControlSent
			if sub == envelope.SubRevision {
				sent, err = w.alice.Revise(WithQueuedSend(ctx, ""), ref, "edited text")
			} else {
				sent, err = w.alice.Retract(WithQueuedSend(ctx, ""), ref, "")
			}
			if err != nil {
				t.Fatal(err)
			}
			raw := receive(sent.ID)
			assertHeld := func(wantReason string) {
				t.Helper()
				var reason, stored string
				err := bob.store.db.QueryRow(`SELECT reason, envelope FROM quarantine WHERE id=?`, sent.ID).Scan(&reason, &stored)
				if err != nil || reason != wantReason || stored != raw {
					t.Fatalf("exact hold missing: reason %q, want %q, err %v", reason, wantReason, err)
				}
				if inboxCount(t, bob, `id=?`, sent.ID) != 0 {
					t.Fatal("held control is already in the inbox")
				}
			}
			if _, err := bob.TrustKey(ctx, w.alice.Address, old.Fingerprint()); err == nil {
				t.Fatal("trust accepted a different key than the one compared")
			}
			assertHeld(reasonKeyChanged)
			trust := func() {
				t.Helper()
				fp, err := bob.TrustKey(ctx, w.alice.Address, ref.Fingerprint)
				if err != nil || fp != ref.Fingerprint {
					t.Fatalf("trust: %q %v", fp, err)
				}
			}
			if sub == envelope.SubRevision {
				trust()
				assertHeld(reasonProof)
				receive(original.ID)
				bob.retryProof(ctx)
			} else {
				// Fail the redaction after the handler has tried to insert the
				// control and remove the hold, then retry after opening the store.
				if _, err := bob.store.db.Exec(`CREATE TRIGGER fail_trusted_redaction BEFORE UPDATE OF body ON inbox
					WHEN OLD.body='original text' AND NEW.body=''
					BEGIN SELECT RAISE(ABORT, 'fixture redaction failure'); END`); err != nil {
					t.Fatal(err)
				}
				if _, err := bob.TrustKey(ctx, w.alice.Address, ref.Fingerprint); err == nil || !strings.Contains(err.Error(), "fixture redaction failure") {
					t.Fatalf("trust did not report the redaction failure: %v", err)
				}
				assertHeld(reasonKeyChanged)
				if m := legacyView(t, bob, original.ID); m.Body != "original text" || m.Edited || m.Deleted {
					t.Fatalf("failed retraction changed original: %+v", m)
				}
				if err := bob.Close(); err != nil {
					t.Fatal(err)
				}
				bob, err = Open(w.bobHome)
				if err != nil {
					t.Fatal(err)
				}
				defer bob.Close()
				if _, err := bob.store.db.Exec(`DROP TRIGGER fail_trusted_redaction`); err != nil {
					t.Fatal(err)
				}
			}
			trust()
			trust()
			if count(t, bob, "quarantine") != 0 || count(t, bob, "inbox") != 2 ||
				inboxCount(t, bob, `id=? AND sub=? AND ref_id=? AND ref_fp=? AND verified_by=?`, sent.ID, sub, original.ID, ref.Fingerprint, ref.Fingerprint) != 1 {
				t.Fatal("expected exactly one admitted control with its signed reference")
			}
			m := legacyView(t, bob, original.ID)
			if sub == envelope.SubRevision {
				if !m.Edited || m.Revision != 1 || m.Text != "edited text" || m.Body != "original text" {
					t.Fatalf("edit: %+v", m)
				}
			} else if !m.Deleted || m.Body != "" || m.Shown(m.Body) != "" {
				t.Fatalf("deletion: %+v", m)
			}
			if msgs, err := bob.Inbox(false, false); err != nil || len(msgs) != 1 || msgs[0].ID != original.ID {
				t.Fatalf("inbox listed a control as a message: %+v %v", msgs, err)
			}
		})
	}
}
