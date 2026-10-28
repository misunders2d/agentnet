package client

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestAcceptedSteeringFollowsExactParentOutcome(t *testing.T) {
	for _, state := range []string{stateAnswered, stateCancelled, stateJobFailed, stateInterrupt} {
		t.Run(state, func(t *testing.T) {
			a, _, parent, id := directNativeCorrection(t, false, envelope.KindQuestion)
			ready := make(chan struct{})
			close(ready)
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan struct{})
			go func() {
				defer close(done)
				a.watchNativeFollowups(ctx, parent, refusedFollowup{ready, steerAccepted}, nil)
			}()
			waitState(t, a, id, stateSteered)
			cancel()
			<-done
			if state == stateAnswered {
				a.finish(tctx(t), parent, envelope.StatusDone, "The exact corrected native result")
			} else if err := a.endJob(parent.ID, state, "Exact parent outcome"); err != nil {
				t.Fatal(err)
			}
			if got := jobState(t, a, id); got != state {
				t.Fatalf("absorbed correction remained %s after exact parent became %s", got, state)
			}
			if state == stateAnswered {
				var parentResult, correctionResult string
				if err := a.store.db.QueryRow(`SELECT coalesce(result_id,'') FROM inbox WHERE id=?`, parent.ID).Scan(&parentResult); err != nil {
					t.Fatal(err)
				}
				if err := a.store.db.QueryRow(`SELECT coalesce(result_id,'') FROM inbox WHERE id=?`, id).Scan(&correctionResult); err != nil || parentResult == "" || correctionResult != parentResult {
					t.Fatalf("exact result linkage parent=%s correction=%s error=%v", parentResult, correctionResult, err)
				}
			}
			if _, found, err := a.claimNativeFollowup(parent); err != nil || found {
				t.Fatalf("settled correction replayed: %t %v", found, err)
			}
		})
	}
}

func TestSteeringSignedTerminalStatusDominatesStaleAcceptance(t *testing.T) {
	a, sender, parent, id := directNativeCorrection(t, false, envelope.KindQuestion)
	stop := runAgent(t, sender)
	signCapsAfter(t, sender, ownCaps)
	stop()
	if _, found, err := a.claimNativeFollowup(parent); err != nil || !found {
		t.Fatal(found, err)
	}
	if err := a.endJob(id, stateSteered, "accepted"); err != nil {
		t.Fatal(err)
	}
	status := func() envelope.Envelope {
		t.Helper()
		if !a.tellStatus(tctx(t), id) {
			t.Fatal("status could not be stored")
		}
		var raw []byte
		var env envelope.Envelope
		if err := a.store.db.QueryRow(`SELECT envelope FROM outbox WHERE sub=? AND ref_id=? ORDER BY rowid DESC LIMIT 1`, envelope.SubStatus, id).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatal(err)
		}
		return env
	}
	accepted := status()
	a.finish(tctx(t), parent, envelope.StatusDone, "One exact native result")
	terminal := status()
	var resultRaw []byte
	var resultEnv envelope.Envelope
	if err := a.store.db.QueryRow(`SELECT envelope FROM outbox WHERE id=(SELECT result_id FROM inbox WHERE id=?)`, parent.ID).Scan(&resultRaw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(resultRaw, &resultEnv); err != nil {
		t.Fatal(err)
	}
	if err := sender.verifyAndStore(tctx(t), resultEnv); err != nil {
		t.Fatal(err)
	}
	newer := sealTo(t, a, sender, envelope.Inner{Kind: envelope.KindMessage, Body: "Independent newer unread turn"})
	if err := sender.verifyAndStore(tctx(t), newer); err != nil {
		t.Fatal(err)
	}
	topicID := topicOf(t, sender, id).ID
	for _, env := range []envelope.Envelope{terminal, accepted, terminal} {
		if err := sender.verifyAndStore(tctx(t), env); err != nil {
			t.Fatal(err)
		}
		view := execOf(t, sender, id)
		if view == nil || view.State != stateAnswered || view.Attempt != 1 || !strings.Contains(view.Detail, parent.ID) {
			t.Fatalf("terminal correction status lost to stale/duplicate acceptance: %+v", view)
		}
		review, err := a.PageReview()
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range review.Device {
			if row.ID == id || row.ID == parent.ID {
				t.Fatal("settled correction/parent retained receiver Review attention")
			}
		}
		for _, row := range append(review.Conv, review.Held...) {
			if row.ID == id || row.ID == parent.ID {
				t.Fatal("settled correction/parent retained conversation Review attention")
			}
		}
		waiting, err := a.Waiting()
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range waiting.AgentRequests {
			if row.ID == id || row.ID == parent.ID {
				t.Fatal("settled correction/parent retained receiver Waiting attention")
			}
		}
		threads, _, err := sender.TopicOverview(true)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, topic := range threads {
			if topic.ID == topicID {
				found = true
				if topic.Pending || topic.Waiting {
					t.Fatal("terminal/stale/duplicate status resurrected exact requester Pending topic")
				}
			}
		}
		if !found {
			t.Fatal("exact requester topic disappeared")
		}
		var unread int
		if err := sender.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE id IN (?,?) AND read_at IS NULL`, resultEnv.ID, newer.ID).Scan(&unread); err != nil || unread != 2 {
			t.Fatal("completion incorrectly cleared real result/newer personal unread", unread, err)
		}
	}
	var result string
	if err := a.store.db.QueryRow(`SELECT result_id FROM inbox WHERE id=?`, parent.ID).Scan(&result); err != nil || !strings.Contains(execOf(t, sender, id).Detail, result) {
		t.Fatal("signed terminal detail lost result provenance", err)
	}
}

type completingSteering struct {
	ready  chan struct{}
	finish func()
}

func (b completingSteering) followupReady() <-chan struct{} { return b.ready }
func (b completingSteering) steer(context.Context, string, string) string {
	b.finish()
	return steerAccepted
}

func TestAcceptedSteeringCompletionBeforeAcknowledgement(t *testing.T) {
	a, _, parent, id := directNativeCorrection(t, false, envelope.KindQuestion)
	ready := make(chan struct{})
	close(ready)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.watchNativeFollowups(ctx, parent, completingSteering{ready, func() {
			a.finish(tctx(t), parent, envelope.StatusDone, "Exact native result before acceptance callback")
		}}, nil)
	}()
	waitState(t, a, id, stateAnswered)
	cancel()
	<-done
	var matches bool
	if err := a.store.db.QueryRow(`SELECT c.result_id=p.result_id AND c.result_id IS NOT NULL FROM inbox c JOIN inbox p ON p.id=c.steering_parent WHERE c.id=?`, id).Scan(&matches); err != nil || !matches {
		t.Fatal("late acceptance lost its exact committed result", matches, err)
	}
}

func TestAcceptedSteeringAttemptAndTerminalFences(t *testing.T) {
	a, _, parent, id := directNativeCorrection(t, false, envelope.KindQuestion)
	if _, found, err := a.claimNativeFollowup(parent); err != nil || !found {
		t.Fatal(found, err)
	}
	if err := a.endJob(id, stateSteered, "accepted", 1); err != nil {
		t.Fatal(err)
	}
	if err := a.reconcileSteeringAfterStop(); err != nil || jobState(t, a, id) != stateSteered {
		t.Fatal("active accepted input became manually recoverable", err)
	}
	if _, err := a.store.db.Exec(`UPDATE inbox SET attempts=2,state=? WHERE id=?`, stateRunning, parent.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.endJob(parent.ID, stateCancelled, "stale first attempt", 1); err != nil || jobState(t, a, parent.ID) != stateRunning {
		t.Fatal("stale parent termination changed newer attempt", err)
	}
	parent.nativeAttempt = 2
	a.finish(tctx(t), parent, envelope.StatusDone, "A different attempt's result")
	if got := jobState(t, a, id); got != stateSteered {
		t.Fatal("later attempt settled prior correction", got)
	}
	if err := a.reconcileSteeringAfterStop(); err != nil || jobState(t, a, id) != stateInterrupt {
		t.Fatal("unproven older attempt did not retain honest recovery", err)
	}
	if err := a.Resolve(id); err != nil {
		t.Fatal(err)
	}
	if err := a.endJob(id, stateSteered, "stale acknowledgement", 1); err != nil || jobState(t, a, id) != stateResolved {
		t.Fatal("late native acknowledgement resurrected resolved correction", err)
	}
}

func TestAcceptedSteeringRejectsGenericParentReply(t *testing.T) {
	a, _, parent, id := directNativeCorrection(t, false, envelope.KindQuestion)
	if _, found, err := a.claimNativeFollowup(parent); err != nil || !found {
		t.Fatal(found, err)
	}
	if err := a.endJob(id, stateSteered, "accepted"); err != nil {
		t.Fatal(err)
	}
	// Model a crash after interruptRunning committed but before recovery's
	// reconciliation. A human's subsequent reply is not this run's result.
	if _, err := a.store.db.Exec(`UPDATE inbox SET state=? WHERE id=?`, stateInterrupt, parent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Reply(tctx(t), parent.ID, "A separate manual reply"); err != nil {
		t.Fatal(err)
	}
	if got := jobState(t, a, id); got != stateInterrupt {
		t.Fatal("manual parent reply did not preserve unknown native outcome", got)
	}
	if err := a.reconcileSteeringAfterStop(); err != nil || jobState(t, a, id) != stateInterrupt {
		t.Fatal("manual reply hid unknown native outcome", err)
	}
}

func TestAcceptedSteeringNativeReactionResultAndStaleAttempt(t *testing.T) {
	for _, stale := range []bool{false, true} {
		name := "exact"
		if stale {
			name = "stale"
		}
		t.Run(name, func(t *testing.T) {
			a, sender, parent, id := directNativeCorrection(t, false, envelope.KindQuestion)
			stop := runAgent(t, sender)
			signCapsAfter(t, sender, ownCaps)
			stop()
			if _, err := a.store.db.Exec(`UPDATE inbox SET responder='codex' WHERE id=?`, parent.ID); err != nil {
				t.Fatal(err)
			}
			if _, found, err := a.claimNativeFollowup(parent); err != nil || !found {
				t.Fatal(found, err)
			}
			if err := a.endJob(id, stateSteered, "accepted"); err != nil {
				t.Fatal(err)
			}
			if stale {
				if _, err := a.store.db.Exec(`UPDATE inbox SET attempts=2 WHERE id=?`, parent.ID); err != nil {
					t.Fatal(err)
				}
			}
			a.finishWithReaction(tctx(t), parent, "codex", &reactionChoice{emoji: "👍"})
			if stale {
				var reactions int
				if err := a.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE sub=?`, envelope.SubReaction).Scan(&reactions); err != nil || reactions != 0 || jobState(t, a, parent.ID) != stateRunning || jobState(t, a, id) != stateSteered {
					t.Fatal("stale native result changed current attempt or sent reaction", reactions, err)
				}
				return
			}
			var exact bool
			if err := a.store.db.QueryRow(`SELECT c.state=? AND c.result_id=p.result_id AND r.sub=? AND r.ref_id=p.id AND r.ref_fp=p.verified_by FROM inbox c JOIN inbox p ON p.id=c.steering_parent JOIN outbox r ON r.id=p.result_id WHERE c.id=?`, stateAnswered, envelope.SubReaction, id).Scan(&exact); err != nil || !exact {
				t.Fatal("successful native reaction lost exact terminal proof", exact, err)
			}
		})
	}
}

func TestLegacySteeringExactRecovery(t *testing.T) {
	for _, mode := range []string{"answer", "cancelled", "ambiguous", "unlinked-result", "executor-mismatch", "empty-executor", "later-attempt"} {
		t.Run(mode, func(t *testing.T) {
			a, _, parent, id := directNativeCorrection(t, false, envelope.KindQuestion)
			if _, found, err := a.claimNativeFollowup(parent); err != nil || !found {
				t.Fatal(found, err)
			}
			if err := a.endJob(id, stateSteered, "shipped accepted correction"); err != nil {
				t.Fatal(err)
			}
			if _, err := a.store.db.Exec(`UPDATE inbox SET steering_parent=NULL,steering_attempt=NULL WHERE id=?`, id); err != nil {
				t.Fatal(err)
			}
			if mode == "executor-mismatch" {
				if _, err := a.store.db.Exec(`UPDATE inbox SET executor='{}' WHERE id=?`, id); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "empty-executor" {
				if _, err := a.store.db.Exec(`UPDATE inbox SET executor='' WHERE id=?`, id); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "later-attempt" {
				if _, err := a.store.db.Exec(`UPDATE inbox SET attempts=2 WHERE id=?`, parent.ID); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "ambiguous" {
				ref, err := storedRequestFollowup(a.store.db, "in", id)
				if err != nil {
					t.Fatal(err)
				}
				ref.Attempt = 0
				raw, _ := json.Marshal(ref)
				if _, err := a.store.db.Exec(`UPDATE inbox SET request_followup=? WHERE id=?`, string(raw), id); err != nil {
					t.Fatal(err)
				}
			}
			want := stateCancelled
			switch mode {
			case "answer":
				a.finish(tctx(t), parent, envelope.StatusDone, "Legacy exact native result")
				want = stateAnswered
			case "unlinked-result":
				if err := a.endJob(parent.ID, stateAnswered, "No stored native result"); err != nil {
					t.Fatal(err)
				}
				want = stateInterrupt
			default:
				if err := a.endJob(parent.ID, stateCancelled, "Stopped exact parent"); err != nil {
					t.Fatal(err)
				}
				if mode == "ambiguous" || mode == "executor-mismatch" || mode == "empty-executor" || mode == "later-attempt" {
					want = stateInterrupt
				}
			}
			for range 2 {
				if err := a.reconcileSteeringAfterStop(); err != nil || jobState(t, a, id) != want {
					t.Fatalf("legacy outcome want=%s got=%s error=%v", want, jobState(t, a, id), err)
				}
			}
			if _, found, err := a.claimNativeFollowup(parent); err != nil || found {
				t.Fatal("legacy recovery replayed work", found, err)
			}
		})
	}
}

func TestLinkedSteeringTerminalStatusSurvivesRestart(t *testing.T) {
	a, sender, parent, id := directNativeCorrection(t, false, envelope.KindQuestion)
	persons(t, sender, a)
	stop := runAgent(t, sender)
	signCapsAfter(t, sender, ownCaps)
	phone, awaited, _ := linkPhone(t, sender, "steering-reader")
	pending := pendingLink(t, sender)
	stop()
	if err := sender.DecideLink(tctx(t), pending.ID, true); err != nil {
		t.Fatal(err)
	}
	if r := <-awaited; r.err != nil {
		t.Fatal(r.err)
	}
	stopPhone := runAgent(t, phone)
	signCapsAfter(t, phone, without(ownCaps, protocol.CapHistoryArchive))
	stopPhone()
	if _, found, err := a.claimNativeFollowup(parent); err != nil || !found {
		t.Fatal(found, err)
	}
	if err := a.endJob(id, stateSteered, "accepted", 1); err != nil {
		t.Fatal(err)
	}
	status := func() envelope.Envelope {
		t.Helper()
		if !a.tellStatus(tctx(t), id) {
			t.Fatal("status send")
		}
		var raw []byte
		var e envelope.Envelope
		if err := a.store.db.QueryRow(`SELECT envelope FROM outbox WHERE sub=? AND ref_id=? ORDER BY rowid DESC LIMIT 1`, envelope.SubStatus, id).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &e); err != nil {
			t.Fatal(err)
		}
		return e
	}
	page := func() {
		t.Helper()
		for range 4 {
			if _, err := sender.deviceHistoryPage(phone.Self()); err != nil {
				t.Fatal(err)
			}
		}
		if n := deliverDeviceHistory(t, sender, phone, envelope.SubDeviceHistory); n == 0 {
			t.Fatal("no history carriers")
		}
		phone.retryProof(tctx(t))
	}
	accepted := status()
	if err := sender.verifyAndStore(tctx(t), accepted); err != nil {
		t.Fatal(err)
	}
	page()
	if e := execOf(t, phone, id); e == nil || e.State != stateSteered {
		t.Fatalf("linked acceptance=%+v", e)
	}
	home := phone.home
	if err := phone.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	phone, err = Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer phone.Close()
	a.finish(tctx(t), parent, envelope.StatusDone, "Exact linked corrected result")
	terminal := status()
	for _, e := range []envelope.Envelope{terminal, accepted, terminal} {
		if err := sender.verifyAndStore(tctx(t), e); err != nil {
			t.Fatal(err)
		}
		page()
		for _, reader := range []*Agent{sender, phone} {
			v := execOf(t, reader, id)
			if v == nil || v.State != stateAnswered || v.Attempt != 1 || !strings.Contains(v.Detail, parent.ID) {
				t.Fatalf("terminal projection %s=%+v", reader.Address, v)
			}
		}
	}
	var result string
	if err := a.store.db.QueryRow(`SELECT result_id FROM inbox WHERE id=?`, parent.ID).Scan(&result); err != nil || result == "" || !strings.Contains(execOf(t, phone, id).Detail, result) {
		t.Fatal("linked exact result provenance", err)
	}
}
