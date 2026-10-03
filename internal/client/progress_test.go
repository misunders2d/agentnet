package client

import (
	"database/sql"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestSendProgressMarksExactCorrelatedMessage(t *testing.T) {
	w := newWorld(t, "")
	progressReader(t, w.alice, w.bob)
	requestID := protocol.NewID()
	request := envelope.Inner{ID: requestID, From: w.alice.Address, To: w.bob.Address, TS: time.Now().Unix(), Kind: envelope.KindTask, Body: "work"}
	if err := w.bob.store.addInbox(request, w.alice.Self().Fingerprint()); err != nil {
		t.Fatal(err)
	}

	got, err := w.bob.SendProgress(tctx(t), w.alice.Address, requestID, "accepted; checking", 0, false)
	if err != nil {
		t.Fatal(err)
	}
	var raw string
	if err = w.bob.store.db.QueryRow(`SELECT envelope FROM outbox WHERE id=?`, got.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var sealed envelope.Envelope
	if err = json.Unmarshal([]byte(raw), &sealed); err != nil {
		t.Fatal(err)
	}
	in, err := envelope.Open(sealed, w.alice.id, w.alice.Address, w.bob.Self())
	if err != nil {
		t.Fatal(err)
	}
	if in.Kind != envelope.KindMessage || in.Status != envelope.StatusProgress || in.ReplyTo != requestID || in.Body != "accepted; checking" {
		t.Fatalf("progress = %+v", in)
	}
	if _, err = w.bob.SendProgress(tctx(t), w.alice.Address, protocol.NewID(), "unknown", 0, false); err == nil || !strings.Contains(err.Error(), "no message") {
		t.Fatalf("unknown request error = %v", err)
	}
	if _, err = w.bob.SendProgress(tctx(t), "carol/desk", requestID, "wrong peer", 0, false); err == nil || !strings.Contains(err.Error(), "is with") {
		t.Fatalf("wrong peer error = %v", err)
	}
	if _, err = w.bob.SendProgress(tctx(t), w.alice.Address, requestID, " ", 0, false); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("empty progress error = %v", err)
	}
	if _, err = w.bob.SendProgress(tctx(t), w.alice.Address, requestID, "file", 0, false, "private.txt"); err == nil || !strings.Contains(err.Error(), "cannot attach") {
		t.Fatalf("progress file error = %v", err)
	}
}

func TestProgressDoesNotBindContinuationReceiverOrReply(t *testing.T) {
	w := newWorld(t, "")
	follow, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindQuestion, Body: "follow", FollowUp: "continue"})
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindQuestion, Body: "receiver", ReplyReceiver: &ReplyReceiver{Kind: "human"}})
	if err != nil {
		t.Fatal(err)
	}
	terminal, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindQuestion, Body: "terminal"})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{follow.ID, receiver.ID, terminal.ID} {
		in := envelope.Inner{ID: protocol.NewID(), From: w.bob.Address, To: w.alice.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage,
			Body: "still working", ReplyTo: id, Status: envelope.StatusProgress}
		if err = w.alice.store.addInbox(in, w.bob.Self().Fingerprint()); err != nil {
			t.Fatal(err)
		}
		if reply, e := w.alice.store.replyTo(id, w.bob.Address); e != nil || reply != "" {
			t.Fatalf("progress counted as reply: %q %v", reply, e)
		}
	}
	var followed sql.NullString
	if err = w.alice.store.db.QueryRow(`SELECT followed_by FROM outbox WHERE id=?`, follow.ID).Scan(&followed); err != nil || followed.Valid {
		t.Fatalf("progress bound follow-up: %+v %v", followed, err)
	}
	bindings := receiverBindings(t, w.alice)
	if len(bindings) != 1 || bindings[0].RequestRef != receiver.ID || len(bindings[0].Inputs) != 0 {
		t.Fatalf("progress bound receiver: %+v", bindings)
	}
	threads, err := w.alice.Threads()
	if err != nil {
		t.Fatal(err)
	}
	waitingCount := 0
	for _, thread := range threads {
		if thread.Waiting {
			waitingCount++
		}
	}
	if waitingCount != 3 {
		t.Fatalf("progress ended waiting state: %+v", threads)
	}

	final := envelope.Inner{ID: protocol.NewID(), From: w.bob.Address, To: w.alice.Address, TS: time.Now().Unix(), Kind: envelope.KindAnswer, Body: "done", ReplyTo: terminal.ID, Status: envelope.StatusDone}
	if err = w.alice.store.addInbox(final, w.bob.Self().Fingerprint()); err != nil {
		t.Fatal(err)
	}
	if reply, e := w.alice.store.replyTo(terminal.ID, w.bob.Address); e != nil || reply != final.ID {
		t.Fatalf("terminal reply missing after progress: %q %v", reply, e)
	}

	ordinaryFollow := envelope.Inner{ID: protocol.NewID(), From: w.bob.Address, To: w.alice.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage, Body: "clarification", ReplyTo: follow.ID}
	ordinaryReceiver := envelope.Inner{ID: protocol.NewID(), From: w.bob.Address, To: w.alice.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage, Body: "clarification", ReplyTo: receiver.ID}
	for _, in := range []envelope.Inner{ordinaryFollow, ordinaryReceiver} {
		if err = w.alice.store.addInbox(in, w.bob.Self().Fingerprint()); err != nil {
			t.Fatal(err)
		}
	}
	if err = w.alice.store.db.QueryRow(`SELECT followed_by FROM outbox WHERE id=?`, follow.ID).Scan(&followed); err != nil || !followed.Valid || followed.String != ordinaryFollow.ID {
		t.Fatalf("ordinary reply did not bind follow-up: %+v %v", followed, err)
	}
	bindings = receiverBindings(t, w.alice)
	if len(bindings) != 1 || len(bindings[0].Inputs) != 1 || bindings[0].Inputs[0].ID != ordinaryReceiver.ID {
		t.Fatalf("ordinary reply did not bind receiver: %+v", bindings)
	}
	if reply, e := w.alice.store.replyTo(receiver.ID, w.bob.Address); e != nil || reply != ordinaryReceiver.ID {
		t.Fatalf("ordinary reply missing: %q %v", reply, e)
	}
	threads, err = w.alice.Threads()
	if err != nil {
		t.Fatal(err)
	}
	for _, thread := range threads {
		if thread.Waiting {
			t.Fatalf("ordinary reply did not end waiting state: %+v", thread)
		}
	}
}

// progressReader runs reader and waits until sender sees its signed prg1.
func progressReader(t *testing.T, reader, sender *Agent) protocol.Profile {
	t.Helper()
	runAgent(t, reader)
	var profile protocol.Profile
	eventually(t, "signed progress capability", func() bool {
		return sender.hub.do(tctx(t), "GET", "/v1/agents/"+reader.Address+"/profile", nil, &profile) == nil && profile.Supports(reader.Address, reader.Self().SignKey, protocol.CapProgress)
	})
	return profile
}

func outboxCap(t *testing.T, a *Agent, id string) (state, required string) {
	t.Helper()
	if err := a.store.db.QueryRow(`SELECT state, coalesce(required_cap, '') FROM outbox WHERE id=?`, id).Scan(&state, &required); err != nil {
		t.Fatal(err)
	}
	return state, required
}

func inboxStatus(a *Agent, id string) (string, bool) {
	var status string
	err := a.store.db.QueryRow(`SELECT coalesce(status, '') FROM inbox WHERE id=?`, id).Scan(&status)
	return status, err == nil
}

// Progress needs the requester's signed prg1 before it is queued and again
// whenever the outbox retries it. An old session never gets an unmarked copy;
// an ordinary correlated clarification still reaches it.
func TestProgressRequiresSignedCapabilityQueueAndRetry(t *testing.T) {
	if !slices.IsSorted(ownCaps) || !slices.Contains(ownCaps, protocol.CapProgress) {
		t.Fatalf("own capabilities %v", ownCaps)
	}
	progress := envelope.Inner{Kind: envelope.KindMessage, Status: envelope.StatusProgress, ReplyTo: protocol.NewID(), Body: "working"}
	if agentRequirement(progress) != protocol.CapProgress || copyRequirement(outCopy{in: progress}) != protocol.CapProgress {
		t.Fatal("progress copy has no prg1 requirement")
	}
	if clarification := (envelope.Inner{Kind: envelope.KindMessage, ReplyTo: progress.ReplyTo, Body: "which city?"}); agentRequirement(clarification) != "" {
		t.Fatal("ordinary clarification needs a capability")
	}

	w := newWorld(t, "")
	progressReader(t, w.alice, w.bob)
	request := envelope.Inner{ID: protocol.NewID(), From: w.alice.Address, To: w.bob.Address, TS: time.Now().Unix(), Kind: envelope.KindTask, Body: "work"}
	if err := w.bob.store.addInbox(request, w.alice.Self().Fingerprint()); err != nil {
		t.Fatal(err)
	}

	// The Hub cannot say what alice reads: queued with prg1, not sent.
	f := injectFaults(w.bob)
	f.add("GET", "/profile", 1, false)
	queued, err := w.bob.SendProgress(tctx(t), w.alice.Address, request.ID, "PROGRESS RETRIED", 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if state, required := outboxCap(t, w.bob, queued.ID); state != stateQueued || required != protocol.CapProgress {
		t.Fatalf("offline progress %s %q", state, required)
	}
	if _, ok := inboxStatus(w.alice, queued.ID); ok {
		t.Fatal("queued progress reached alice")
	}

	// A retry with the capability present sends the marked message.
	if err = w.bob.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "retried progress", func() bool { s, ok := inboxStatus(w.alice, queued.ID); return ok && s == envelope.StatusProgress })
	f.add("GET", "/profile", 1, false)
	held, err := w.bob.SendProgress(tctx(t), w.alice.Address, request.ID, "PROGRESS HELD", 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if state, required := outboxCap(t, w.bob, held.ID); state != stateQueued || required != protocol.CapProgress {
		t.Fatalf("second offline progress %s %q", state, required)
	}

	// alice's session now says it is an older program.
	signCapsNow(t, w.alice, without(ownCaps, protocol.CapProgress))
	if err = w.bob.requireParticipationCaps(tctx(t), w.alice.Self(), protocol.CapProgress); err == nil {
		t.Fatal("older session accepted progress")
	}
	// A queued copy rechecks at retry and is held, unsent and unchanged.
	var sealed string
	w.bob.store.db.QueryRow(`SELECT envelope FROM outbox WHERE id=?`, held.ID).Scan(&sealed)
	if err = w.bob.FlushOutbox(tctx(t)); err != nil {
		t.Logf("flush: %v", err)
	}
	if state, required := outboxCap(t, w.bob, held.ID); state != stateConvWaiting || required != protocol.CapProgress {
		t.Fatalf("held progress after downgrade %s %q", state, required)
	}
	// A new update is held too, never refused into an unmarked resend.
	later, err := w.bob.SendProgress(tctx(t), w.alice.Address, request.ID, "PROGRESS LATER", 0, false)
	if err != nil || later.State != stateConvWaiting || !strings.Contains(later.Detail, "cannot read nonterminal responder progress") {
		t.Fatalf("progress to older session: %+v %v", later, err)
	}
	// The ordinary correlated clarification is unchanged for the older session.
	clarify, err := w.bob.SendMessage(tctx(t), Outgoing{To: w.alice.Address, Body: "which city?", ReplyTo: request.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, required := outboxCap(t, w.bob, clarify.ID); required != "" {
		t.Fatalf("clarification requirement %q", required)
	}
	eventually(t, "clarification", func() bool { s, ok := inboxStatus(w.alice, clarify.ID); return ok && s == "" })
	feats, err := w.bob.relayFeatures(tctx(t))
	if err != nil {
		t.Fatal(err)
	}
	w.bob.releaseConv(tctx(t), feats)
	if err = w.bob.FlushOutbox(tctx(t)); err != nil {
		t.Logf("flush: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	var downgraded int
	w.alice.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE body IN ('PROGRESS HELD','PROGRESS LATER')`).Scan(&downgraded)
	if downgraded != 0 {
		t.Fatal("older session received progress")
	}

	// Held copies survive a restart, then release once alice reads prg1.
	w.bob.Close()
	bob, err := Open(w.bobHome)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bob.Close() })
	for _, id := range []string{held.ID, later.ID} {
		var after string
		bob.store.db.QueryRow(`SELECT envelope FROM outbox WHERE id=?`, id).Scan(&after)
		if state, required := outboxCap(t, bob, id); state != stateConvWaiting || required != protocol.CapProgress || id == held.ID && after != sealed {
			t.Fatalf("held progress after restart %s %q", state, required)
		}
	}
	signCapsNow(t, w.alice, ownCaps)
	bob.releaseConv(tctx(t), feats)
	if err = bob.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{held.ID, later.ID} {
		eventually(t, "released progress", func() bool { s, ok := inboxStatus(w.alice, id); return ok && s == envelope.StatusProgress })
	}
	bob.releaseConv(tctx(t), feats)
	bob.FlushOutbox(tctx(t))
	time.Sleep(200 * time.Millisecond)
	var copies int
	w.alice.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE body IN ('PROGRESS RETRIED','PROGRESS HELD','PROGRESS LATER') AND status=?`, envelope.StatusProgress).Scan(&copies)
	if copies != 3 {
		t.Fatalf("progress copies at alice %d, want exactly 3", copies)
	}
}

// Progress binds the exact stored request: a named executor only for this
// device's own target, a participation request through its conversation,
// never a plain conversation item or a non-request; eligibility follows.
func TestProgressRequestBindingAndEligibility(t *testing.T) {
	w := newWorld(t, "")
	progressReader(t, w.alice, w.bob)
	add := func(in envelope.Inner) string {
		t.Helper()
		in.ID, in.From, in.To, in.TS = protocol.NewID(), w.alice.Address, w.bob.Address, time.Now().Unix()
		if err := w.bob.store.addInbox(in, w.alice.Self().Fingerprint()); err != nil {
			t.Fatal(err)
		}
		return in.ID
	}
	other := add(envelope.Inner{Kind: envelope.KindQuestion, Body: "named elsewhere", Target: &envelope.Target{Address: "carol/desk", Fingerprint: w.bob.Self().Fingerprint(), AgentID: protocol.NewID()}})
	conv := add(envelope.Inner{Kind: envelope.KindTask, Body: "dm"})
	if _, err := w.bob.store.db.Exec(`UPDATE inbox SET conv=? WHERE id=?`, protocol.NewID(), conv); err != nil {
		t.Fatal(err)
	}
	plain := add(envelope.Inner{Kind: envelope.KindMessage, Body: "hello"})
	before := count(t, w.bob, "outbox")
	for id, want := range map[string]string{other: "own named executor", conv: ErrConversationItem.Error(), plain: "question or task"} {
		if _, err := w.bob.SendProgress(tctx(t), w.alice.Address, id, "update", 0, false); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("progress to %s: %v, want %q", id, err, want)
		}
	}
	if count(t, w.bob, "outbox") != before {
		t.Fatal("refused progress was queued")
	}

	r := &Responder{Harness: "claude", Dir: t.TempDir()}
	base := job{ID: protocol.NewID(), From: w.alice.Address, Kind: envelope.KindTask, Body: "work"}
	for name, j := range map[string]job{
		"default":       {},
		"named":         {Target: &envelope.Target{Address: w.bob.Address, AgentID: protocol.NewID()}, AgentID: protocol.NewID()},
		"participation": {Conv: protocol.NewID(), PID: protocol.NewID()},
	} {
		j.ID, j.From, j.Kind, j.Body = base.ID, base.From, base.Kind, base.Body
		if !j.progressEligible() {
			t.Fatalf("%s job not offered progress", name)
		}
		if name == "default" {
			if p, err := w.bob.prompt(j, r); err != nil || !strings.Contains(p, "--progress") {
				t.Fatalf("default prompt %v", err)
			}
		}
	}
	for name, j := range map[string]job{
		"conversation": {Conv: protocol.NewID()},
		"receiver":     {Receiver: &ReplyReceiverBinding{ID: protocol.NewID()}},
		"local":        {Local: true},
		"follow-up":    {Kind: envelope.KindAnswer},
	} {
		j.ID, j.From, j.Body = base.ID, base.From, base.Body
		if j.Kind == "" {
			j.Kind = base.Kind
		}
		if j.progressEligible() {
			t.Fatalf("%s job offered progress", name)
		}
		if name == "follow-up" {
			continue // its prompt reads stored follow-up instructions
		}
		if p, err := w.bob.prompt(j, r); err != nil || strings.Contains(p, "--progress") || strings.Contains(p, ProgressRequestEnv) {
			t.Fatalf("%s prompt offered progress (%v)", name, err)
		}
	}
}
