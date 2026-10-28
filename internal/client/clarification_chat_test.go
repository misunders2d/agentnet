package client

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// A current linked human is the same owner, including when another of that
// person's devices authored the original. Settlement belongs to the new claim.
func TestOwnLinkedHumanTaggedFollowupSettlesExactParent(t *testing.T) {
	st := installStub(t, "answer")
	w := newWorld(t, "")
	persons(t, w.alice)
	stop := runAgent(t, w.alice)
	phone := linked(t, w.alice)
	tablet := linkedVia(t, w.alice, "tablet", func(ctx context.Context, id string) error {
		return w.alice.DecideLink(ctx, id, true)
	})
	stop()
	setResponder(t, w.alice, "stub", st.dir, time.Minute)
	var original envelope.Envelope
	for _, body := range []string{"owner's report with completed research", "unrelated owner permission remains open"} {
		env := sealTo(t, phone, w.alice, envelope.Inner{Kind: envelope.KindTask, Body: body})
		if err := w.alice.verifyAndStore(tctx(t), env); err != nil {
			t.Fatal(err)
		}
		if _, err := w.alice.store.db.Exec(`UPDATE inbox SET state=?,attempts=1,detail='Which folder?' WHERE id=?`, stateNeedHuman, env.ID); err != nil {
			t.Fatal(err)
		}
		if original.ID == "" {
			original = env
		}
	}
	ref := &envelope.FollowupRef{Ref: envelope.Ref{ID: original.ID, Fingerprint: phone.Self().Fingerprint()}}
	correction := sealTo(t, tablet, w.alice, envelope.Inner{Kind: envelope.KindTask, Body: "Use the owner's approved reports folder", ReplyTo: original.ID, Followup: ref})
	if err := w.alice.verifyAndStore(tctx(t), correction); err != nil {
		t.Fatal(err)
	}
	if jobState(t, w.alice, original.ID) != stateNeedHuman || st.count() != 0 {
		t.Fatal("receiving a linked tagged answer settled before a successful claim")
	}
	if !w.alice.runNext(tctx(t), nil) || jobState(t, w.alice, correction.ID) != stateAnswered || jobState(t, w.alice, original.ID) != stateResolved || st.count() != 1 {
		t.Fatal("current own-human roster did not permit one exact tagged continuation")
	}
	var attempts, unrelated int
	if err := w.alice.store.db.QueryRow(`SELECT attempts FROM inbox WHERE id=?`, original.ID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if err := w.alice.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE state=?`, stateNeedHuman).Scan(&unrelated); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || unrelated != 1 {
		t.Fatalf("settlement reran the original or changed another card: attempts=%d unrelated=%d", attempts, unrelated)
	}
	if err := w.alice.verifyAndStore(tctx(t), correction); err != nil {
		t.Fatal(err)
	}
	if w.alice.runNext(tctx(t), nil) || st.count() != 1 {
		t.Fatal("duplicate own-human continuation replayed")
	}
}

func TestClarificationPromptReservesNeedsHumanForPermission(t *testing.T) {
	w := newWorld(t, "")
	for _, kind := range []string{envelope.KindQuestion, envelope.KindTask} {
		t.Run(kind, func(t *testing.T) {
			prompt, err := w.bob.prompt(job{From: w.alice.Address, Kind: kind, Body: "Prepare the report"}, &Responder{})
			if err != nil {
				t.Fatal(err)
			}
			var stopLine string
			for _, line := range strings.Split(prompt, "\n") {
				if strings.Contains(line, needsHumanMarker) {
					stopLine = line
				}
			}
			if stopLine == "" || !strings.Contains(stopLine, "native permission") {
				t.Errorf("missing concrete native-permission boundary: %s", stopLine)
			}
			for _, overloaded := range []string{"a choice", "money", "must decide or act"} {
				if strings.Contains(stopLine, overloaded) {
					t.Errorf("ordinary clarification still enters a blocking decision: %s", stopLine)
				}
			}
			if !strings.Contains(prompt, "tag") || !strings.Contains(prompt, "ordinary") {
				t.Error("ordinary clarification has no tagged-chat instruction")
			}
			if strings.Contains(prompt, "environment failure is needs-human") || strings.Contains(prompt, "needs its usual OK here") {
				t.Error("another prompt section invents environment/task authorization")
			}
			for _, want := range []string{"Missing checkout or session context is not a permission denial", "within the requested scope and native policy", "exact failed operation and evidence", "who can fix it", "do not repeat unexplained environment jargon"} {
				if !strings.Contains(prompt, want) {
					t.Errorf("missing context-reporting requirement %q", want)
				}
			}
		})
	}
}

// Missing working/session context can be reported to the same owner on their
// other device. It neither invents an approval nor restarts the original work.
// The stub supplies a factual environment result; this checks routing, not a
// claim that a real model always follows the prompt correctly.
func TestOwnLinkedHumanEnvironmentReport(t *testing.T) {
	st := installStub(t, "environment")
	t.Setenv("HERDR_ENV", "")
	w := newWorld(t, "")
	persons(t, w.alice)
	runAgent(t, w.alice)
	phone := linked(t, w.alice)
	setResponder(t, w.alice, "stub", st.dir, time.Minute)
	for _, kind := range []string{envelope.KindQuestion, envelope.KindTask} {
		q, err := phone.SendMessage(tctx(t), Outgoing{To: w.alice.Address, Kind: kind, Body: "Read-only preflight: inspect available checkout and session capabilities; do not start or modify work."})
		if err != nil {
			t.Fatal(err)
		}
		waitState(t, w.alice, q.ID, stateAnswered)
		eventually(t, "correlated environment report reaches own device", func() bool {
			m, ok := findReply(phone, q.ID)
			return ok && m.Kind == replyKind(kind) && m.Status == envelope.StatusDone && strings.Contains(m.Body, "No changes made")
		})
		prompt := mustRead(t, st.log+".stdin")
		for _, want := range []string{"your owner's own", "Missing checkout or session context is not a permission denial", "do not start or modify work"} {
			if !strings.Contains(prompt, want) {
				t.Errorf("own-device preflight omitted %q", want)
			}
		}
		items, err := w.alice.Review()
		if err != nil || len(items) != 0 {
			t.Fatalf("ordinary environment result became attention: %d %v", len(items), err)
		}
	}
	if st.count() != 2 {
		t.Fatalf("own question/task ran %d times", st.count())
	}
}

func TestContinueRequestGroupWorkerRepeatedRounds(t *testing.T) {
	st := installStub(t, "clarify")
	w, _, packet, stops := groupTurnsFixture(t)
	setResponder(t, w.alice, "stub", st.dir, time.Minute)
	p := p6Member(t, w.alice, w.alice, packet.State.Conv)
	for _, stop := range stops {
		stop()
	}
	start := time.Now()
	q, err := w.alice.AskAgent(tctx(t), p.PID, envelope.KindQuestion, "bound group work with preserved completed effects")
	if err != nil {
		t.Fatal(err)
	}
	var id string
	if err := w.alice.store.db.QueryRow(`SELECT id FROM inbox WHERE lid=? AND local=1`, q.LID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	queued := time.Now()
	if !w.alice.runNext(tctx(t), nil) {
		t.Fatal("group worker did not claim original")
	}
	if got := jobState(t, w.alice, id); got != stateNeedHuman {
		t.Fatalf("fixture did not record old clarification: %s", got)
	}
	for round, answer := range []string{"release branch", "approved output folder"} {
		attempt := int64(round + 1)
		token := protocol.NewID()
		if err := w.alice.ContinueRequest(id, token, attempt, answer); err != nil {
			t.Fatal(err)
		}
		if round == 1 {
			t.Setenv("STUB_MODE", "answer")
		}
		claimAt := time.Now()
		if !w.alice.runNext(tctx(t), nil) {
			t.Fatalf("group worker did not claim continuation round %d", round+1)
		}
		prompt, err := os.ReadFile(st.log + ".stdin")
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"bound group work", "Which branch?", "release branch", "do not replay successful effects"} {
			if !strings.Contains(string(prompt), want) {
				t.Errorf("actual group worker round %d omitted %q", round+1, want)
			}
		}
		if !strings.Contains(string(prompt), answer) {
			t.Errorf("actual group worker lost current answer in round %d", round+1)
		}
		wantState := stateNeedHuman
		if round == 1 {
			wantState = stateAnswered
		}
		if got := jobState(t, w.alice, id); got != wantState {
			t.Fatalf("group worker round %d ended %s, want %s", round+1, got, wantState)
		}
		if err := w.alice.ContinueRequest(id, token, attempt, answer); err != nil {
			t.Fatal("exact continuation retry", err)
		}
		t.Logf("group continuation round=%d claim/run/response=%s", round+1, time.Since(claimAt))
	}
	if st.count() != 3 {
		t.Fatalf("original and two explicit continuations ran %d times", st.count())
	}
	if w.alice.runNext(tctx(t), nil) || st.count() != 3 {
		t.Fatal("completed group continuation replayed")
	}
	t.Logf("synthetic group enqueue=%s total=%s; no claim about reported thirty-minute request", queued.Sub(start), time.Since(start))
}

// An exact, freshly authorized tagged follow-up is new work in the existing
// request context. An older clarification must not strand it in the queue.
func TestTaggedFollowupPastStaleClarification(t *testing.T) {
	w := newWorld(t, "")
	if err := w.bob.Approve(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	original := sealTo(t, w.alice, w.bob, envelope.Inner{Kind: envelope.KindQuestion, Body: "Prepare contribution-margin.md"})
	if err := w.bob.verifyAndStore(tctx(t), original); err != nil {
		t.Fatal(err)
	}
	if _, err := w.bob.store.db.Exec(`UPDATE inbox SET state=?,attempts=1,detail='Which budget should the report use?' WHERE id=?`, stateNeedHuman, original.ID); err != nil {
		t.Fatal(err)
	}
	ref := &envelope.FollowupRef{Ref: envelope.Ref{ID: original.ID, Fingerprint: w.alice.Self().Fingerprint()}}
	correction := sealTo(t, w.alice, w.bob, envelope.Inner{Kind: envelope.KindQuestion, Body: "Use the release budget", ReplyTo: original.ID, Followup: ref})
	if err := w.bob.verifyAndStore(tctx(t), correction); err != nil {
		t.Fatal(err)
	}
	ready, err := requestFollowupReadyIn(w.bob.store.db, correction.ID)
	if err != nil || !ready {
		t.Errorf("authorized tagged reply blocked behind stale clarification: ready=%v err=%v", ready, err)
	}
	j := job{ID: correction.ID, From: w.alice.Address, Key: ref.Fingerprint, Kind: envelope.KindQuestion, Body: "Use the release budget", ReplyTo: original.ID}
	prompt, err := w.bob.requestFollowupPrompt(j)
	if err != nil {
		t.Errorf("tagged reply requires a separate manual continuation: %v", err)
	} else if !strings.Contains(prompt, "Prepare contribution-margin.md") || !strings.Contains(prompt, "Which budget should the report use?") {
		t.Errorf("tagged reply lost the exact original work or recorded clarification: %s", prompt)
	}
}

func TestTaggedClarificationWorkerSettlesExactParent(t *testing.T) {
	st := installStub(t, "answer")
	w := newWorld(t, "")
	setResponder(t, w.bob, "stubhuman", st.dir, time.Minute)
	if err := w.bob.Approve(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	q, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindQuestion, Body: "original report with completed research"})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, q.ID, stateNeedHuman)
	other, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindQuestion, Body: "unrelated request remains unresolved"})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, other.ID, stateNeedHuman)
	setResponder(t, w.bob, "stub", st.dir, time.Minute)
	t.Setenv("STUB_MODE", "clarify")
	x := RequestFollowup{Ref: envelope.Ref{ID: q.ID, Fingerprint: w.alice.Self().Fingerprint()}, ID: protocol.NewID(), Body: "Use the release budget and keep the existing research"}
	queuedAt := time.Now()
	if _, err := w.alice.QueueRequestFollowup(tctx(t), x); err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, x.ID, stateNeedHuman)
	if got := jobState(t, w.bob, q.ID); got != stateResolved {
		t.Errorf("matching stale parent remained %s", got)
	}
	if got := jobState(t, w.bob, other.ID); got != stateNeedHuman {
		t.Errorf("tagged reply changed unrelated card: %s", got)
	}
	prompt, err := os.ReadFile(st.log + ".stdin")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"original report with completed research", "which budget applies?", x.Body, "Do not rerun completed effects"} {
		if !strings.Contains(string(prompt), want) {
			t.Errorf("fresh tagged continuation lost %q", want)
		}
	}
	t.Setenv("STUB_MODE", "answer")
	next := RequestFollowup{Ref: envelope.Ref{ID: x.ID, Fingerprint: w.alice.Self().Fingerprint()}, ID: protocol.NewID(), Body: "Use report.md in the approved folder"}
	if _, err := w.alice.QueueRequestFollowup(tctx(t), next); err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, next.ID, stateAnswered)
	if got := jobState(t, w.bob, x.ID); got != stateResolved {
		t.Errorf("second tagged round left its exact earlier correction %s", got)
	}
	prompt, err = os.ReadFile(st.log + ".stdin")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"original report with completed research", "which budget applies?", "Which branch?", x.Body, next.Body} {
		if !strings.Contains(string(prompt), want) {
			t.Errorf("second tagged continuation lost %q", want)
		}
	}
	var attempts int
	if err := w.bob.store.db.QueryRow(`SELECT attempts FROM inbox WHERE id=?`, q.ID).Scan(&attempts); err != nil || attempts != 1 {
		t.Errorf("old request reran: attempts=%d err=%v", attempts, err)
	}
	review, err := w.bob.Review()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range review {
		if r.ID == q.ID {
			t.Error("matching stale parent still appears in Review")
		}
	}
	at := attention(t, w.bob, "after-tagged-reply", "SessionStart")
	if strings.Contains(at.Text, q.ID) || !strings.Contains(at.Text, other.ID) {
		t.Errorf("corresponding attention did not settle exactly: %s", at.Text)
	}
	if _, err := w.alice.QueueRequestFollowup(tctx(t), next); err != nil {
		t.Fatal("exact tagged retry", err)
	}
	if _, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindMessage, ReplyTo: other.ID, Body: "untagged context only"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "untagged context is stored", func() bool { return hasInbox(w.bob, "untagged context only") })
	if got := jobState(t, w.bob, other.ID); got != stateNeedHuman || st.count() != 4 {
		t.Errorf("untagged context or duplicate retry started work: state=%s runs=%d", got, st.count())
	}
	t.Logf("synthetic tagged enqueue-to-answer=%s; original attempts=%d; no live thirty-minute diagnosis", time.Since(queuedAt), attempts)
}

func TestTaggedFollowupCannotSettleBeforeTaskConsent(t *testing.T) {
	st := installStub(t, "answer")
	w := newWorld(t, "")
	setResponder(t, w.bob, "stub", st.dir, time.Minute)
	original := sealTo(t, w.alice, w.bob, envelope.Inner{Kind: envelope.KindTask, Body: "original accepted task"})
	if err := w.bob.verifyAndStore(tctx(t), original); err != nil {
		t.Fatal(err)
	}
	if _, err := w.bob.store.db.Exec(`UPDATE inbox SET state=?,attempts=1,detail='Old task clarification' WHERE id=?`, stateNeedHuman, original.ID); err != nil {
		t.Fatal(err)
	}
	ref := &envelope.FollowupRef{Ref: envelope.Ref{ID: original.ID, Fingerprint: w.alice.Self().Fingerprint()}}
	correction := sealTo(t, w.alice, w.bob, envelope.Inner{Kind: envelope.KindTask, Body: "Continue in the approved folder", ReplyTo: original.ID, Followup: ref})
	if err := w.bob.verifyAndStore(tctx(t), correction); err != nil {
		t.Fatal(err)
	}
	if w.bob.runNext(tctx(t), nil) || st.count() != 0 || jobState(t, w.bob, original.ID) != stateNeedHuman {
		t.Fatal("tagged task bypassed current consent or settled the old card before claim")
	}
	if err := w.bob.Accept(correction.ID); err != nil {
		t.Fatal(err)
	}
	if !w.bob.runNext(tctx(t), nil) || st.count() != 1 || jobState(t, w.bob, original.ID) != stateResolved {
		t.Fatal("explicit task consent did not permit one exact continuation and settlement")
	}
}
