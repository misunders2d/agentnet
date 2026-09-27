package ui

import (
	"errors"
	"testing"
	"time"
)

func testFixture() *Fixture {
	return NewFixture(func() time.Time { return time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC) })
}

func reviewItem(t *testing.T, f *Fixture, kind string) string {
	t.Helper()
	for _, it := range f.State().Review {
		if it.Kind == kind {
			return it.ID
		}
	}
	t.Fatalf("no %s in review", kind)
	return ""
}

func TestFixtureDecisions(t *testing.T) {
	f := testFixture()
	task := reviewItem(t, f, KindTask)
	if err := f.Act(Action{ID: task, Do: "decline", Reason: "not my repo"}); err != nil {
		t.Fatal(err)
	}
	p, m := f.find(task)
	if m.State != "declined" {
		t.Fatalf("state %q", m.State)
	}
	reply := p.msgs[len(p.msgs)-1]
	if reply.ReplyTo != task || reply.Kind != KindResult || reply.Status != "declined" || reply.Body != "not my repo" ||
		reply.State != "delivered" {
		t.Fatalf("decline reply %+v", reply)
	}
	if err := f.Act(Action{ID: task, Do: "accept"}); !errors.Is(err, ErrRefused) {
		t.Fatalf("accept after decline: %v", err)
	}

	held := reviewItem(t, f, KindQuestion) // carol's needs_human or dave's held; find dave's
	for _, it := range f.State().Review {
		if it.Peer == "dave/srv" {
			held = it.ID
		}
	}
	// Approving the sender covers future questions only, as agentnet approve
	// does: the held question waits for a separate accept.
	if err := f.Act(Action{ID: held, Do: "approve"}); err != nil {
		t.Fatal(err)
	}
	p, m = f.find(held)
	if !p.approved || m.State != "held" {
		t.Fatalf("approve: approved %v state %q", p.approved, m.State)
	}
	if v := f.view(p, m); len(v.Actions) != 3 || v.Actions[1] != "accept" || v.StateText != "Needs you: it arrived before you approved dave/srv" {
		t.Fatalf("held actions after approve: %v", v.Actions)
	}
	if err := f.Act(Action{ID: held, Do: "approve"}); !errors.Is(err, ErrRefused) {
		t.Fatalf("second approve: %v", err)
	}
	if err := f.Act(Action{ID: held, Do: "accept"}); err != nil {
		t.Fatal(err)
	}
	if _, m := f.find(held); m.State != "running" {
		t.Fatalf("accept after approve: %q", m.State)
	}

	if err := f.Act(Action{ID: "erin/lab", Do: "trust"}); err != nil {
		t.Fatal(err)
	}
	if err := f.Act(Action{ID: "erin/lab", Do: "trust"}); !errors.Is(err, ErrRefused) {
		t.Fatalf("second trust: %v", err)
	}
	if _, err := f.Send(Draft{Peer: "erin/lab", Kind: KindMessage, Body: "welcome back"}); err != nil {
		t.Fatalf("send after trust: %v", err)
	}
}

// Nothing sent to a peer whose computer is not connected shows as delivered.
func TestFixtureOfflinePeerGetsCustody(t *testing.T) {
	f := testFixture()
	var held string
	for _, it := range f.State().Review {
		if it.Peer == "dave/srv" {
			held = it.ID
		}
	}
	if err := f.Act(Action{ID: held, Do: "decline"}); err != nil {
		t.Fatal(err)
	}
	p, _ := f.find(held)
	if got := p.msgs[len(p.msgs)-1]; got.Status != "declined" || got.State != "custody" {
		t.Fatalf("decline to offline peer: %+v", got)
	}
	m, err := f.Send(Draft{Peer: "dave/srv", Kind: KindTask, Body: "check the cert pins"})
	if err != nil || m.State != "custody" || m.Next != "Waiting on dave/srv" {
		t.Fatalf("send to offline peer: %+v %v", m, err)
	}
}

func TestFixtureReplyTakesOverAndOnlyOnce(t *testing.T) {
	f := testFixture()
	var nh string
	for _, it := range f.State().Review {
		if it.Peer == "carol/ci" {
			nh = it.ID
		}
	}
	m, err := f.Send(Draft{Peer: "carol/ci", Body: "No: secrets never go through chat.", ReplyTo: nh})
	if err != nil {
		t.Fatal(err)
	}
	if m.Kind != KindAnswer || m.Status != "done" {
		t.Fatalf("reply %+v", m)
	}
	if _, q := f.find(nh); q.State != "manual" {
		t.Fatalf("question state %q", q.State)
	}
	if _, err := f.Send(Draft{Peer: "carol/ci", Body: "again", ReplyTo: nh}); !errors.Is(err, ErrRefused) {
		t.Fatalf("second reply: %v", err)
	}
	// A reply must stay in its own conversation.
	if _, err := f.Send(Draft{Peer: "bob/desk", Body: "x", ReplyTo: nh}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-conversation reply: %v", err)
	}
	for _, it := range f.State().Review {
		if it.ID == nh {
			t.Fatal("answered item still in review")
		}
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
}
