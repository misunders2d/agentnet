package client

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestGroupOwnTargetStatusReorderedBeforeOriginal(t *testing.T) {
	w, _, packet, stops := groupTurnsFixture(t)
	conv := packet.State.Conv
	p := p6Member(t, w.alice, w.alice, conv)
	eventually(t, "peer knows participation", func() bool { return stateAt(t, w.bob, p.PID).Claimable() })
	for _, stop := range stops {
		stop()
	}
	q, e := w.alice.AskAgent(WithQueuedSend(tctx(t), protocol.NewID()), p.PID, envelope.KindQuestion, "synthetic retained own request")
	if e != nil {
		t.Fatal(e)
	}
	var id string
	if e = w.alice.store.db.QueryRow(`SELECT id FROM outbox WHERE conv=? AND lid=? AND recipient=? AND ref_id IS NULL`, conv, q.LID, w.bob.Address).Scan(&id); e != nil {
		t.Fatal(e)
	}
	original := groupTurnEnvelope(t, w.alice, id)
	req, e := envelope.Open(original, w.bob.id, w.bob.Address, w.alice.Self())
	if e != nil {
		t.Fatal(e)
	}
	body, _ := json.Marshal(envelope.Status{State: "running", N: 1, At: time.Now().Unix()})
	in := envelope.Inner{V: envelope.Version3, ID: protocol.NewID(), From: w.alice.Address, To: w.bob.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage, Sub: envelope.SubStatus, Body: string(body), Conv: conv, LID: protocol.NewID(), Ref: &envelope.Ref{ID: q.LID, Fingerprint: w.alice.Self().Fingerprint()}}
	rec, e := w.bob.Self().Recipient()
	if e != nil {
		t.Fatal(e)
	}
	env, e := envelope.Seal(in, w.alice.id.Sign, rec)
	if e != nil {
		t.Fatal(e)
	}
	opened, e := envelope.Open(env, w.bob.id, w.bob.Address, w.alice.Self())
	if e != nil {
		t.Fatal(e)
	}
	before, why := w.bob.statusAllowed(opened, w.alice.Address, w.alice.Self().Fingerprint())
	if before {
		t.Fatal("original unexpectedly present")
	}
	// A status may precede the conversation proof as well as the request.
	unknown := in
	unknown.ID, unknown.LID, unknown.Conv = protocol.NewID(), protocol.NewID(), strings.Repeat("f", 64)
	missingContext, e := envelope.Seal(unknown, w.alice.id.Sign, rec)
	if e != nil {
		t.Fatal(e)
	}
	if e = w.bob.verifyAndStore(tctx(t), missingContext); e != nil {
		t.Fatal(e)
	}
	var contextReason string
	if e = w.bob.store.db.QueryRow(`SELECT reason FROM quarantine WHERE id=?`, unknown.ID).Scan(&contextReason); e != nil {
		t.Fatal(e)
	}
	if contextReason != reasonProof || inboxCount(t, w.bob, `id=?`, unknown.ID) != 0 {
		t.Fatal("status without conversation proof did not remain pending")
	}
	if e = w.bob.verifyAndStore(tctx(t), env); e != nil {
		t.Fatal(e)
	}
	var reason string
	if e = w.bob.store.db.QueryRow(`SELECT reason FROM quarantine WHERE id=?`, env.ID).Scan(&reason); e != nil {
		t.Fatal(e)
	}
	if reason != reasonProof {
		t.Fatalf("signed reordered own-target status held %q, want proof pending; before=%t why=%q", reason, before, why)
	}
	if n := inboxCount(t, w.bob, `id=?`, env.ID); n != 0 {
		t.Fatal("status accepted before original")
	}
	if e = w.bob.verifyAndStore(tctx(t), env); e != nil {
		t.Fatal(e)
	}
	var n int
	w.bob.store.db.QueryRow(`SELECT count(*) FROM quarantine WHERE id=?`, env.ID).Scan(&n)
	if n != 1 {
		t.Fatal("duplicate changed durable pending status identity")
	}
	var kept string
	if e = w.bob.store.db.QueryRow(`SELECT envelope FROM quarantine WHERE id=?`, env.ID).Scan(&kept); e != nil {
		t.Fatal(e)
	}
	var retained envelope.Envelope
	if e = json.Unmarshal([]byte(kept), &retained); e != nil || retained.ID != env.ID || !bytes.Equal(retained.CT, env.CT) {
		t.Fatal("proof hold did not retain the exact ciphertext")
	}
	// Fresh proof-held statuses also fail closed if the executor changes.
	if _, e = w.bob.store.db.Exec(`UPDATE peers SET pending=public WHERE address=?`, w.alice.Address); e != nil {
		t.Fatal(e)
	}
	if e = w.bob.verifyAndStore(tctx(t), original); e != nil {
		t.Fatal(e)
	}
	if w.bob.convWork.bits.Load()&convRetry == 0 {
		t.Fatal("original arrival did not wake proof recovery")
	}
	w.bob.retryProof(tctx(t))
	if inboxCount(t, w.bob, `id=?`, env.ID) != 0 {
		t.Fatal("fresh proof-held status admitted under a pending executor pin")
	}
	tx, e := w.bob.store.db.Begin()
	if e != nil {
		t.Fatal(e)
	}
	guard := statusSenderCheck(tx, w.alice.Self())
	tx.Rollback()
	if guard == nil {
		t.Fatal("fresh status admission transaction accepted pending executor")
	}
	if _, e = w.bob.store.db.Exec(`UPDATE peers SET pending=NULL WHERE address=?`, w.alice.Address); e != nil {
		t.Fatal(e)
	}
	reopened, e := Open(w.bob.home)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	w.bob = reopened
	if e = w.bob.verifyAndStore(tctx(t), original); e != nil {
		t.Fatal(e)
	}
	if n := inboxCount(t, w.bob, `id=?`, req.ID); n != 1 {
		t.Fatal("signed original not admitted")
	}
	w.bob.retryProof(tctx(t))
	if n := inboxCount(t, w.bob, `id=? AND sub=?`, env.ID, envelope.SubStatus); n != 1 {
		t.Fatal("same status did not recover after original")
	}
	w.bob.store.db.QueryRow(`SELECT count(*) FROM quarantine WHERE id=?`, env.ID).Scan(&n)
	if n != 0 {
		t.Fatal("recovered status remains quarantined")
	}
	if e = w.bob.verifyAndStore(tctx(t), env); e != nil {
		t.Fatal(e)
	}
	if n := inboxCount(t, w.bob, `id=? AND sub=?`, env.ID, envelope.SubStatus); n != 1 {
		t.Fatal("status duplicated after recovery")
	}
	var invalidControls []string
	for _, change := range []string{"wrong_host", "wrong_requester_key"} {
		forged := in
		forged.ID = protocol.NewID()
		forged.LID = protocol.NewID()
		ref := *in.Ref
		forged.Ref = &ref
		signer := w.alice
		if change == "wrong_host" {
			signer = w.bob
			forged.From = w.bob.Address
		} else {
			forged.Ref.Fingerprint = w.bob.Self().Fingerprint()
		}
		wrong, e := envelope.Seal(forged, signer.id.Sign, rec)
		if e != nil {
			t.Fatal(e)
		}
		if e = w.bob.verifyAndStore(tctx(t), wrong); e != nil {
			t.Fatal(e)
		}
		if e = w.bob.store.db.QueryRow(`SELECT reason FROM quarantine WHERE id=?`, wrong.ID).Scan(&reason); e != nil {
			t.Fatal(e)
		}
		if reason != reasonInvalid || inboxCount(t, w.bob, `id=?`, wrong.ID) != 0 {
			t.Fatalf("%s status not refused as invalid", change)
		}
		invalidControls = append(invalidControls, wrong.ID)
	}
	t.Run("retained_invalid", func(t *testing.T) {
		legacy := in
		legacy.ID, legacy.LID = protocol.NewID(), protocol.NewID()
		old, e := envelope.Seal(legacy, w.alice.id.Sign, rec)
		if e != nil {
			t.Fatal(e)
		}
		if e = w.bob.store.holdAsDiagnostic(old, reasonInvalid, "the sender is not the device that request (from that key) is for"); e != nil {
			t.Fatal(e)
		}
		if e = w.bob.ArchiveHeldNotice(old.ID); e != nil {
			t.Fatal(e)
		}
		bad := old
		bad.ID = protocol.NewID()
		bad.Sig = append([]byte(nil), old.Sig...)
		bad.Sig[0] ^= 1
		if e = w.bob.store.holdAs(bad, reasonInvalid); e != nil {
			t.Fatal(e)
		}
		missingRequest := req
		missingRequest.ID, missingRequest.LID = protocol.NewID(), protocol.NewID()
		missingStatus := in
		missingStatus.ID, missingStatus.LID = protocol.NewID(), protocol.NewID()
		ref := *in.Ref
		ref.ID = missingRequest.LID
		missingStatus.Ref = &ref
		missing, e := envelope.Seal(missingStatus, w.alice.id.Sign, rec)
		if e != nil {
			t.Fatal(e)
		}
		if e = w.bob.store.holdAs(missing, reasonInvalid); e != nil {
			t.Fatal(e)
		}
		// Only the upgrade marker is reset: simulate a pre-fix retained store.
		if _, e = w.bob.store.db.Exec(`DELETE FROM config WHERE k=?`, statusRecoveryScan); e != nil {
			t.Fatal(e)
		}
		upgraded, e := Open(w.bob.home)
		if e != nil {
			t.Fatal(e)
		}
		defer upgraded.Close()
		historyRecoveryReason(t, upgraded, old.ID, reasonProof)
		historyRecoveryReason(t, upgraded, missing.ID, reasonProof)
		historyRecoveryReason(t, upgraded, bad.ID, reasonInvalid)
		for _, id := range invalidControls {
			historyRecoveryReason(t, upgraded, id, reasonInvalid)
			if inboxCount(t, upgraded, `id=?`, id) != 0 {
				t.Fatal("upgrade admitted a known invalid status")
			}
		}
		var archived bool
		var cipher string
		if e = upgraded.store.db.QueryRow(`SELECT notice_archived,envelope FROM quarantine WHERE id=?`, old.ID).Scan(&archived, &cipher); e != nil {
			t.Fatal(e)
		}
		var exact envelope.Envelope
		if json.Unmarshal([]byte(cipher), &exact) != nil || !archived || exact.ID != old.ID || !bytes.Equal(exact.CT, old.CT) {
			t.Fatal("upgrade changed archive or retained ciphertext")
		}
		if inboxCount(t, upgraded, `id IN (?,?)`, old.ID, missing.ID) != 0 {
			t.Fatal("upgrade admitted statuses without normal ingress")
		}
		// A changed/pending executor remains blocked after migration and reopen.
		if _, e = upgraded.store.db.Exec(`UPDATE peers SET pending=public WHERE address=?`, w.alice.Address); e != nil {
			t.Fatal(e)
		}
		upgraded.retryProof(tctx(t))
		if inboxCount(t, upgraded, `id=?`, old.ID) != 0 {
			t.Fatal("pending executor status admitted")
		}
		tx, e := upgraded.store.db.Begin()
		if e != nil {
			t.Fatal(e)
		}
		guard := statusRecoveryCheck(tx, w.alice.Address, old.ID)
		tx.Rollback()
		if guard == nil {
			t.Fatal("admission transaction accepted a pending executor")
		}
		if _, e = upgraded.store.db.Exec(`UPDATE peers SET pending=NULL WHERE address=?`, w.alice.Address); e != nil {
			t.Fatal(e)
		}
		upgraded.retryProof(tctx(t))
		if inboxCount(t, upgraded, `id=? AND sub=?`, old.ID, envelope.SubStatus) != 1 {
			t.Fatal("retained invalid status did not recover same ID")
		}
		missingOriginal, e := envelope.Seal(missingRequest, w.alice.id.Sign, rec)
		if e != nil {
			t.Fatal(e)
		}
		if e = upgraded.verifyAndStore(tctx(t), missingOriginal); e != nil {
			t.Fatal(e)
		}
		upgraded.retryProof(tctx(t))
		if inboxCount(t, upgraded, `id=? AND sub=?`, missing.ID, envelope.SubStatus) != 1 {
			t.Fatal("old missing-original status did not recover on original arrival")
		}
		late := in
		late.ID, late.LID = protocol.NewID(), protocol.NewID()
		lateEnv, e := envelope.Seal(late, w.alice.id.Sign, rec)
		if e != nil {
			t.Fatal(e)
		}
		if e = upgraded.store.holdAs(lateEnv, reasonInvalid); e != nil {
			t.Fatal(e)
		}
		if e = upgraded.recoverInvalidStatuses(); e != nil {
			t.Fatal(e)
		}
		historyRecoveryReason(t, upgraded, late.ID, reasonInvalid)
		if inboxCount(t, upgraded, `state='running'`) > 0 {
			t.Fatal("inert status recovery ran work")
		}
	})

}
