package client

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func remotePrepared(t *testing.T, w *world, phone *Agent, kind string) (SendResult, string) {
	t.Helper()
	r := ReplyReceiver{Kind: "human", Host: &ReplyReceiverHost{Address: w.alice.Address, Fingerprint: w.alice.Self().Fingerprint()}}
	sent, e := phone.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: kind, Body: "EXACT_ORIGINAL_RETAINED_BASELINE", ReplyReceiver: &r})
	if e != nil {
		t.Fatal(e)
	}
	b := groupReceiverBound(t, phone, sent.ID)
	full, e := replyReceiverIn(phone.store.db, b.ID)
	if e != nil {
		t.Fatal(e)
	}
	setup := full.remote.Route.DelegationID
	eventually(t, "unapproved typed setup", func() bool { s, e := w.alice.store.jobState(setup); return e == nil && s == stateAwaiting })
	return sent, setup
}

func TestReceiverRetractionBeforeApproval(t *testing.T) {
	for _, kind := range []string{envelope.KindMessage, envelope.KindQuestion, envelope.KindTask} {
		t.Run(kind, func(t *testing.T) {
			w, phone, _, _ := remoteReceiverWorld(t)
			sent, setup := remotePrepared(t, w, phone, kind)
			if _, e := phone.Retract(tctx(t), ControlRef{ID: sent.ID, Fingerprint: phone.Self().Fingerprint()}, "delete own original"); e != nil {
				t.Fatal(e)
			}
			eventually(t, "exact retraction held pending original proof", func() bool {
				var n int
				w.alice.store.db.QueryRow(`SELECT count(*) FROM quarantine WHERE sender=? AND reason=?`, phone.Address, reasonProof).Scan(&n)
				return n > 0
			})
			if e := w.alice.Accept(setup); e != nil {
				t.Fatal(e)
			}
			eventually(t, "approved import observes prior deletion", func() bool { b, e := replyReceiverIn(w.alice.store.db, setup); return e == nil && b.remote != nil })
			b, e := replyReceiverIn(w.alice.store.db, setup)
			if e != nil {
				t.Fatal(e)
			}
			if !retractedRef(w.alice.store.db, "", sent.ID, phone.Self().Fingerprint()) {
				t.Fatal("signed prior deletion not stored atomically")
			}
			if kind == envelope.KindMessage {
				if b.State != "canceled" || !b.remote.Redacted || b.remote.Request.Body != "" {
					t.Fatalf("ordinary resurrected %+v", b)
				}
				var n int
				w.alice.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE reply_to=? AND state=?`, setup, stateQueued).Scan(&n)
				if n != 0 {
					t.Fatal("prior ordinary deletion emitted executable ready")
				}
			} else {
				if b.State == "canceled" || b.remote.Redacted || b.remote.Request.Body != "EXACT_ORIGINAL_RETAINED_BASELINE" {
					t.Fatalf("immutable Q/T baseline cleared %+v", b)
				}
				// The selected host already has custody of the immutable setup,
				// but the original is still local. Later approval must not
				// release an execution request the sender deleted meanwhile.
				eventually(t, "exact ready cannot release deleted local original", func() bool {
					var n int
					phone.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE reply_to=? AND json_extract(receiver_route,'$.op')='ready' AND detail='receiver original batch changed before exact ready'`, setup).Scan(&n)
					return n == 1
				})
				if e := phone.FlushOutbox(tctx(t)); e != nil {
					t.Fatal(e)
				}
				var state string
				var stopped bool
				var started int
				if e := phone.store.db.QueryRow(`SELECT state,send_stopped,handover_started FROM outbox WHERE id=?`, sent.ID).Scan(&state, &stopped, &started); e != nil {
					t.Fatal(e)
				}
				if state != stateNotDelivered || !stopped || started != 0 {
					t.Fatalf("deleted local Q/T released: %s stopped=%t started=%d", state, stopped, started)
				}
				var received int
				if e := w.bob.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE id=?`, sent.ID).Scan(&received); e != nil {
					t.Fatal(e)
				}
				if received != 0 {
					t.Fatal("later receiver approval silently delivered a deleted local Q/T")
				}
			}
			if j, claimed, e := w.alice.store.claimJob("default"); e != nil || claimed {
				t.Fatalf("deleted setup/default %+v %t %v", j, claimed, e)
			}
		})
	}
}

func TestReceiverRetractionImportedOrdinaryAndForgedProof(t *testing.T) {
	w, phone, _, _ := remoteReceiverWorld(t)
	sent, setup := remotePrepared(t, w, phone, envelope.KindMessage)
	to, e := w.alice.Self().Recipient()
	if e != nil {
		t.Fatal(e)
	}
	body, _ := json.Marshal(envelope.Retraction{Reason: "forged"})
	for _, bad := range []string{"signature", "key", "id", "scope"} {
		in := envelope.Inner{V: envelope.Version3, ID: protocol.NewID(), From: phone.Address, To: w.alice.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage, Sub: envelope.SubRetraction, Body: string(body), Ref: &envelope.Ref{ID: sent.ID, Fingerprint: phone.Self().Fingerprint()}}
		signer := phone.id.Sign
		switch bad {
		case "signature":
			signer = w.bob.id.Sign
		case "key":
			in.Ref.Fingerprint = w.bob.Self().Fingerprint()
		case "id":
			in.Ref.ID = protocol.NewID()
		case "scope":
			in.Conv = strings.Repeat("a", 64)
			in.LID = protocol.NewID()
		}
		env, e := envelope.Seal(in, signer, to)
		if e != nil {
			t.Fatal(e)
		}
		if e = w.alice.store.holdAs(env, reasonProof); e != nil {
			t.Fatal(e)
		}
	}
	if e = w.alice.Accept(setup); e != nil {
		t.Fatal(e)
	}
	eventually(t, "forged controls do not suppress exact ready", func() bool {
		var n int
		w.bob.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE id=?`, sent.ID).Scan(&n)
		return n == 1
	})
	if retractedRef(w.alice.store.db, "", sent.ID, phone.Self().Fingerprint()) {
		t.Fatal("forged held original proof authorized deletion")
	}
	if _, e = phone.Retract(tctx(t), ControlRef{ID: sent.ID, Fingerprint: phone.Self().Fingerprint()}, "legitimate delivered deletion"); e != nil {
		t.Fatal(e)
	}
	eventually(t, "selected imported ordinary copy redacted", func() bool {
		b, e := replyReceiverIn(w.alice.store.db, setup)
		return e == nil && b.State == "canceled" && b.remote.Redacted && b.remote.Request.Body == ""
	})
	var held int
	w.alice.store.db.QueryRow(`SELECT count(*) FROM quarantine WHERE reason=?`, reasonProof).Scan(&held)
	if held != 4 {
		t.Fatalf("foreign/forged held controls changed %d", held)
	}
	if j, claimed, e := w.alice.store.claimJob("default"); e != nil || claimed {
		t.Fatalf("retention/default %+v %t %v", j, claimed, e)
	}
}
