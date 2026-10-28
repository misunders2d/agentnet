package client

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Aggregate fixture diagnostics only: never log bodies, keys or recipients.
func logFixtureOutbox(t *testing.T, a *Agent, phase string) {
	t.Helper()
	t.Logf("fixture outbox phase=%s normal_lock=%d sync_lock=%d", phase, len(a.flushLock), len(a.syncFlushLock))
	rows, err := a.store.db.Query(`SELECT CASE WHEN coalesce(sub,'') IN `+syncSubs+` AND replication_live=0 THEN 'sync' ELSE 'normal' END,coalesce(sub,''),state,count(*) FROM outbox WHERE state IN (?,?,?,?) GROUP BY 1,2,3 ORDER BY 1,2,3`, stateQueued, stateConvWaiting, stateReceiverWaiting, protocol.StateQuarantined)
	if err != nil {
		t.Logf("fixture outbox phase=%s snapshot_error=%v", phase, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var lane, sub, state string
		var count int
		if err := rows.Scan(&lane, &sub, &state, &count); err != nil {
			t.Logf("fixture outbox phase=%s snapshot_error=%v", phase, err)
			return
		}
		t.Logf("fixture outbox phase=%s lane=%s sub=%q state=%s count=%d", phase, lane, sub, state, count)
	}
	if err := rows.Err(); err != nil {
		t.Logf("fixture outbox phase=%s snapshot_error=%v", phase, err)
	}
}

// Keep a strict correction and the following ordinary turns in the real
// durable queue. Reader requirements must not make an authorized PID turn
// disappear between the group delivery guards and block the per-reader FIFO.
func TestGroupSteeringDoesNotBlockFollowingMessages(t *testing.T) {
	w, carol, packet, stops := groupTurnsFixture(t)
	a := w.alice
	p := p6Member(t, a, a, packet.State.Conv)
	original, err := a.AskAgent(tctx(t), p.PID, envelope.KindQuestion, "Explain the mobile client")
	if err != nil {
		t.Fatal(err)
	}
	stops[a]()
	// Own this queue's only pump: all three durable turns must be present
	// before the explicit FlushOutbox below, without background handovers.
	a.stopBackgroundPosts()
	if _, err = a.store.db.Exec(`UPDATE inbox SET state=?,attempts=1,last_attempt_at=? WHERE id=?`, stateRunning, time.Now().Unix(), original.LID); err != nil {
		t.Fatal(err)
	}
	steering := protocol.NewID()
	if _, err = a.QueueRequestFollowup(tctx(t), RequestFollowup{Conv: packet.State.Conv, Ref: envelope.Ref{ID: original.LID, Fingerprint: a.Self().Fingerprint()}, Attempt: 1, ID: steering, Body: "Focus on mobile in particular"}); err != nil {
		t.Fatal(err)
	}
	ids := []string{steering}
	for _, body := range []string{"The correction is still sending", "An ordinary later message"} {
		sent, err := a.SendConv(WithQueuedSend(tctx(t), protocol.NewID()), packet.State.Conv, ConvOutgoing{Body: body})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, sent.LID)
	}
	// The answer may already have finished while the sender's retained copies
	// wait. Delivery never needs to restart this original or its correction.
	if _, err = a.store.db.Exec(`UPDATE inbox SET state=? WHERE id=?`, stateAnswered, original.LID); err != nil {
		t.Fatal(err)
	}
	type copy struct {
		id, lid, required string
		raw               []byte
		env               envelope.Envelope
	}
	rows, err := a.store.db.Query(`SELECT id,lid,required_cap,envelope FROM outbox WHERE conv=? AND lid IN (?,?,?) ORDER BY rowid`, packet.State.Conv, ids[0], ids[1], ids[2])
	if err != nil {
		t.Fatal(err)
	}
	var copies []copy
	for rows.Next() {
		var c copy
		if err = rows.Scan(&c.id, &c.lid, &c.required, &c.raw); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		if err = json.Unmarshal(c.raw, &c.env); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		copies = append(copies, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(copies) != 6 {
		t.Fatalf("three logical turns, two exact readers: copies=%d err=%v", len(copies), err)
	}
	for _, c := range copies {
		if c.lid != steering {
			continue
		}
		if c.required != protocol.CapGroup+"+"+protocol.CapRequestSteer {
			t.Fatalf("strict intent requirement changed: %q", c.required)
		}
		handled, allowed, gateErr := a.mayDeliverGroupTurn(c.env)
		pHandled, pAllowed, pErr := a.mayDeliverGroupParticipation(c.env)
		t.Logf("strict copy: group=%t/%t/%v participation=%t/%t/%v", handled, allowed, gateErr, pHandled, pAllowed, pErr)
	}
	var queued int
	if err = a.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE conv=? AND lid IN (?,?,?) AND state=?`, packet.State.Conv, ids[0], ids[1], ids[2], stateQueued).Scan(&queued); err != nil || queued != len(copies) {
		t.Fatalf("retained queue before explicit flush: queued=%d copies=%d err=%v", queued, len(copies), err)
	}
	// An app restart must retain the strict intent and the ordinary turns;
	// recovery uses the normal queue pump, without editing or resealing rows.
	if err = a.Close(); err != nil {
		t.Fatal(err)
	}
	a, err = Open(a.home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	logFixtureOutbox(t, a, "steering-before-flush")
	flushCtx, traceFlush := traceFixtureHTTP(t, tctx(t))
	flushStarted := time.Now()
	err = a.FlushOutbox(flushCtx)
	traceFlush(err)
	t.Logf("fixture steering flush elapsed_ms=%d context_error=%v", time.Since(flushStarted).Milliseconds(), flushCtx.Err())
	logFixtureOutbox(t, a, "steering-after-flush")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range copies {
		var state string
		var raw []byte
		if err = a.store.db.QueryRow(`SELECT state,envelope FROM outbox WHERE id=?`, c.id).Scan(&state, &raw); err != nil {
			t.Fatal(err)
		}
		if state != protocol.StateCustody && state != protocol.StateDelivered {
			t.Errorf("logical %s reader %s remains %q after normal flush", c.lid, c.env.To, state)
			result, deliveryErr := a.deliver(tctx(t), c.env, nil)
			t.Logf("normal delivery diagnostic: result=%+v err=%v", result, deliveryErr)
		}
		if !bytes.Equal(c.raw, raw) {
			t.Error("retry rewrote a retained signed envelope")
		}
	}
	if t.Failed() {
		return
	}
	if err = a.FlushOutbox(tctx(t)); err != nil {
		t.Fatal("repeated retry", err)
	}
	for _, reader := range []*Agent{w.bob, carol} {
		eventually(t, "three retained turns arrive once", func() bool {
			var count int
			err := reader.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE conv=? AND lid IN (?,?,?)`, packet.State.Conv, ids[0], ids[1], ids[2]).Scan(&count)
			return err == nil && count == 3
		})
		rows, err := reader.store.db.Query(`SELECT lid,attempts FROM inbox WHERE conv=? AND lid IN (?,?,?) ORDER BY arrival`, packet.State.Conv, ids[0], ids[1], ids[2])
		if err != nil {
			t.Fatal(err)
		}
		var index int
		for rows.Next() {
			var lid string
			var attempts int
			if err = rows.Scan(&lid, &attempts); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			if index >= len(ids) || lid != ids[index] || attempts != 0 {
				t.Errorf("reader %s turn %d: lid=%s attempts=%d; wanted original FIFO and no execution", reader.Address, index, lid, attempts)
			}
			index++
		}
		err = rows.Err()
		rows.Close()
		if err != nil || index != len(ids) {
			t.Fatalf("reader originals after repeated retry: count=%d err=%v", index, err)
		}
	}
}
