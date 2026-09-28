package ui

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func testFixture() *Fixture {
	return NewFixture(func() time.Time { return time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC) })
}

func overview(t *testing.T, f *Fixture) Overview {
	t.Helper()
	o, err := f.Overview()
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func reviewOf(t *testing.T, f *Fixture, peer, kind string) string {
	t.Helper()
	for _, it := range overview(t, f).Review {
		if it.Peer == peer && it.Kind == kind {
			return it.ID
		}
	}
	t.Fatalf("no %s from %s in review", kind, peer)
	return ""
}

func TestFixtureOverviewIsThreads(t *testing.T) {
	f := testFixture()
	o := overview(t, f)
	decisions, notices := 0, 0
	for _, it := range o.Review {
		if it.Notice {
			notices++
		} else {
			decisions++
		}
	}
	if !o.Demo || decisions != 3 || notices != 2 || len(o.Quarantine) != 1 {
		t.Fatalf("demo %v decisions %d notices %d quarantine %d", o.Demo, decisions, notices, len(o.Quarantine))
	}
	perPeer := map[string]int{}
	for i, s := range o.Threads {
		perPeer[s.Peer]++
		if (s.Peer == "hub/ops") != s.NoticeOnly || (s.Peer == "hub/ops" && (s.Notices != 1 || s.Review != 0 || s.Unread != 0)) {
			t.Fatalf("notice flags %+v", s)
		}
		if i > 0 && s.LastAt.After(o.Threads[i-1].LastAt) {
			t.Fatalf("threads not newest first at %d", i)
		}
		th, err := f.Thread(s.ID)
		if err != nil || th.Peer != s.Peer || len(th.Messages) != s.Count {
			t.Fatalf("thread %s: %+v %v", s.ID, th, err)
		}
	}
	// One peer, several independent threads.
	if perPeer["carol/ci"] != 4 || perPeer["bob/desk"] != 7 || perPeer["hub/ops"] != 2 {
		t.Fatalf("threads per peer %v", perPeer)
	}
	// Any message of a thread opens the whole thread.
	first := o.Threads[len(o.Threads)-1]
	th, _ := f.Thread(first.ID)
	last := th.Messages[len(th.Messages)-1].ID
	if again, err := f.Thread(last); err != nil || again.ID != last || again.Messages[0].ID != th.Messages[0].ID {
		t.Fatalf("thread by later message: %v", err)
	}
	if _, err := f.Thread("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown thread: %v", err)
	}
}

func TestFixtureDecisions(t *testing.T) {
	f := testFixture()
	task := reviewOf(t, f, "bob/desk", KindTask)
	if _, err := f.Act(Action{ID: task, Do: DoDecline, Reason: "not my repo"}); err != nil {
		t.Fatal(err)
	}
	th, m := f.find(task)
	if m.State != "declined" {
		t.Fatalf("state %q", m.State)
	}
	reply := th.msgs[len(th.msgs)-1]
	if reply.ReplyTo != task || reply.Kind != KindResult || reply.Status != "declined" || reply.Body != "not my repo" ||
		reply.State != "delivered" {
		t.Fatalf("decline reply %+v", reply)
	}
	if _, err := f.Act(Action{ID: task, Do: DoAccept}); !errors.Is(err, ErrRefused) {
		t.Fatalf("accept after decline: %v", err)
	}

	// Approving the sender covers future questions only, as agentnet approve
	// does: the held question waits for a separate accept.
	held := reviewOf(t, f, "dave/srv", KindQuestion)
	if _, err := f.Act(Action{ID: "dave/srv", Do: DoApprove}); err != nil {
		t.Fatal(err)
	}
	th, m = f.find(held)
	if !f.peers["dave/srv"].approved || m.State != "held" {
		t.Fatalf("approve: state %q", m.State)
	}
	if v := f.view(th, m); len(v.Actions) != 4 || v.Actions[1] != DoAccept {
		t.Fatalf("held actions after approve: %v", v.Actions)
	}
	if _, err := f.Act(Action{ID: held, Do: DoAccept}); err != nil {
		t.Fatal(err)
	}
	if _, m := f.find(held); m.State != "running" {
		t.Fatalf("accept after approve: %q", m.State)
	}
	if _, err := f.Act(Action{ID: held, Do: DoAcceptAlways}); !errors.Is(err, ErrRefused) {
		t.Fatalf("accept always on a running question: %v", err)
	}

	// A changed key blocks sending until trusted; trust is once.
	if _, err := f.Send(Draft{To: "erin/lab", Kind: KindMessage, Body: "hi"}); !errors.Is(err, ErrRefused) {
		t.Fatalf("send before trust: %v", err)
	}
	pending := f.peers["erin/lab"].key.Pending
	for _, key := range []string{"", "SHA256:another"} {
		if _, err := f.Act(Action{ID: "erin/lab", Do: DoTrust, Key: key}); !errors.Is(err, ErrRefused) {
			t.Fatalf("trust with key %q: %v", key, err)
		}
	}
	if _, err := f.Act(Action{ID: "erin/lab", Do: DoTrust, Key: pending}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Act(Action{ID: "erin/lab", Do: DoTrust, Key: pending}); !errors.Is(err, ErrRefused) {
		t.Fatalf("second trust: %v", err)
	}
	if _, err := f.Send(Draft{To: "erin/lab", Kind: KindMessage, Body: "welcome back"}); err != nil {
		t.Fatalf("send after trust: %v", err)
	}
}

// Nothing sent to a peer whose computer is not connected shows as delivered.
func TestFixtureOfflinePeerGetsCustody(t *testing.T) {
	f := testFixture()
	held := reviewOf(t, f, "dave/srv", KindQuestion)
	if _, err := f.Act(Action{ID: held, Do: DoDecline}); err != nil {
		t.Fatal(err)
	}
	th, _ := f.find(held)
	if got := th.msgs[len(th.msgs)-1]; got.Status != "declined" || got.State != "custody" {
		t.Fatalf("decline to offline peer: %+v", got)
	}
	s, err := f.Send(Draft{To: "dave/srv", Kind: KindTask, Body: "check the cert pins"})
	if err != nil || s.State != "custody" {
		t.Fatalf("send to offline peer: %+v %v", s, err)
	}
	got, _ := f.Thread(s.ID)
	if m := got.Messages[0]; m.Next != "Waiting on dave/srv" || m.StateText != "Waiting on the server until dave/srv connects" {
		t.Fatalf("new task view %+v", m)
	}
}

func TestFixtureReplyTakesOverAndOnlyOnce(t *testing.T) {
	f := testFixture()
	nh := reviewOf(t, f, "carol/ci", KindQuestion)
	if _, err := f.Act(Action{ID: nh, Do: DoReply, Body: "No: secrets never go through chat."}); err != nil {
		t.Fatal(err)
	}
	th, q := f.find(nh)
	if q.State != "manual" {
		t.Fatalf("question state %q", q.State)
	}
	if r := th.msgs[len(th.msgs)-1]; r.Kind != KindAnswer || r.Status != "done" || r.ReplyTo != nh {
		t.Fatalf("reply %+v", r)
	}
	if _, err := f.Act(Action{ID: nh, Do: DoReply, Body: "again"}); !errors.Is(err, ErrRefused) {
		t.Fatalf("second reply: %v", err)
	}
	// A linked message must stay in its own conversation.
	if _, err := f.Send(Draft{To: "bob/desk", Body: "x", ReplyTo: nh}); !errors.Is(err, ErrRefused) {
		t.Fatalf("cross-conversation link: %v", err)
	}
	for _, it := range overview(t, f).Review {
		if it.ID == nh {
			t.Fatal("answered item still in review")
		}
	}
}

func TestFixtureReadClearsUnread(t *testing.T) {
	f := testFixture()
	task := reviewOf(t, f, "bob/desk", KindTask)
	before := 0
	for _, s := range overview(t, f).Threads {
		before += s.Unread
	}
	if _, err := f.Act(Action{Do: DoRead, IDs: []string{task}}); err != nil {
		t.Fatal(err)
	}
	after := 0
	for _, s := range overview(t, f).Threads {
		after += s.Unread
	}
	if after != before-1 {
		t.Fatalf("unread %d -> %d", before, after)
	}
}

func TestFixtureSimulationAndCounter(t *testing.T) {
	f := testFixture()
	seq, ch := f.Changed()
	if err := f.Simulate("finish"); err != nil { // carol's running task
		t.Fatal(err)
	}
	select {
	case <-ch:
	default:
		t.Fatal("change channel not closed")
	}
	if s, _ := f.Changed(); s != seq+1 {
		t.Fatalf("seq %d", s)
	}
	if err := f.Simulate("finish"); !errors.Is(err, ErrRefused) {
		t.Fatalf("finish with nothing running: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := f.Simulate("arrival"); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Simulate("arrival"); !errors.Is(err, ErrRefused) {
		t.Fatalf("fourth arrival: %v", err)
	}
}

// Plain labels never claim more than the stored state proves.
func TestStateTextAndNext(t *testing.T) {
	if got := StateText("out", KindQuestion, "delivered", "bob/desk"); got != "Delivered to bob/desk" {
		t.Fatalf("delivered: %q", got)
	}
	if got := StateText("out", KindMessage, "custody", "bob/desk"); got != "Waiting on the server until bob/desk connects" {
		t.Fatalf("custody: %q", got)
	}
	if got := Next("out", KindQuestion, "delivered", "bob/desk", true); got != "" {
		t.Fatalf("answered question still waiting: %q", got)
	}
	if got := Next("out", KindMessage, "delivered", "bob/desk", false); got != "" {
		t.Fatalf("a plain message owes nothing: %q", got)
	}
	for _, s := range []string{"held", "awaiting", "needs_human"} {
		if got := Next("in", KindTask, s, "bob/desk", false); got != "you" {
			t.Fatalf("%s: %q", s, got)
		}
	}
	// An address in a reason keeps its case.
	if got := ReviewWhy(KindQuestion, "held", "carol/ci", ""); got != "carol/ci is not approved for automatic answers" {
		t.Fatalf("held reason: %q", got)
	}
	if got := ReviewWhy(KindTask, "awaiting", "carol/ci", ""); got != "Tasks run only if you accept them" {
		t.Fatalf("awaiting reason: %q", got)
	}
	// Only states the client accepts a decision in offer it.
	if a := ActionsFor(KindMessage, "held"); a != nil {
		t.Fatalf("plain message actions %v", a)
	}
	// A notice or a followed-up reply that needs the person can be closed,
	// never accepted or run.
	for _, k := range []string{KindMessage, KindAnswer, KindResult} {
		if a := ActionsFor(k, "needs_human"); len(a) != 1 || a[0] != DoResolve {
			t.Fatalf("%s needs_human actions %v", k, a)
		}
		want := "Needs you: "
		if k == KindMessage {
			want = "Reported: " // a review notice is a report from that machine, not a decision here
		}
		if Next("in", k, "needs_human", "bob/desk", false) != "you" || !strings.HasPrefix(StateText("in", k, "needs_human", "bob/desk"), want) {
			t.Fatalf("%s needs_human shown as %q", k, StateText("in", k, "needs_human", "bob/desk"))
		}
	}
	for _, s := range []string{"answered", "manual", "declined", "resolved", "pending"} {
		if a := ActionsFor(KindTask, s); a != nil {
			t.Fatalf("%s offers %v", s, a)
		}
	}
}
