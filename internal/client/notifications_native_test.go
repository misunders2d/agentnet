package client

import (
	"sync"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestNativeReviewAndReminderExactDestinations(t *testing.T) {
	w := newWorld(t, "")
	n := fakeNotify(w.bob)
	var got []string
	w.bob.nativeNotify = func(fragment string) { got = append(got, fragment) }
	id := protocol.NewID()
	_, e := w.bob.store.db.Exec(`INSERT INTO inbox(id,sender,ts,kind,body,received_at,state,verified_by) VALUES(?,?,?,?,?,?,?,?)`, id, w.alice.Address, 1, envelope.KindQuestion, "PRIVATE content", 1, stateHeld, w.alice.Self().Fingerprint())
	if e != nil {
		t.Fatal(e)
	}
	w.bob.notifyReview()
	w.bob.notifyReview()
	if len(got) != 1 || got[0] != "msg="+id+"&dir=in" {
		t.Fatalf("review routes %v", got)
	}
	if _, e = w.bob.SetReminder(id, time.Now().Add(time.Hour)); e != nil {
		t.Fatal(e)
	}
	if _, e = w.bob.remindDue(time.Now().Add(2 * time.Hour)); e != nil {
		t.Fatal(e)
	}
	if len(got) != 2 || got[1] != "msg="+id+"&dir=in" {
		t.Fatalf("reminder routes %v", got)
	}
	if _, e = w.bob.remindDue(time.Now().Add(2 * time.Hour)); e != nil {
		t.Fatal(e)
	}
	if len(got) != 2 || n.count() != 0 {
		t.Fatal("repeat or desktop notification")
	}
	notice := insertReviewAlert(t, w.bob, w.alice.Address, w.alice.Self().Fingerprint(), reviewAlertBody(t, w.alice.Address, reviewAlertItem(w.alice)))
	if route := w.bob.nativeReviewFragment(notice); route != "review" {
		t.Fatal("received report did not open actionable activity", route)
	}
}
func TestHumanOnlyReviewAttentionUsesChanges(t *testing.T) {
	w := newWorld(t, "")
	var mu sync.Mutex
	var got []string
	stop, _ := runWith(t, w, w.bob, RunOptions{HumanOnly: true, Notify: func(fragment string) { mu.Lock(); got = append(got, fragment); mu.Unlock() }})
	id := protocol.NewID()
	_, e := w.bob.store.db.Exec(`INSERT INTO inbox(id,sender,ts,kind,body,received_at,state,verified_by) VALUES(?,?,?,?,?,?,?,?)`, id, w.alice.Address, 1, envelope.KindQuestion, "PRIVATE", 1, stateHeld, w.alice.Self().Fingerprint())
	if e != nil {
		t.Fatal(e)
	}
	w.bob.NoteChange()
	eventually(t, "native review change", func() bool { mu.Lock(); defer mu.Unlock(); return len(got) == 1 })
	stop()
	mu.Lock()
	defer mu.Unlock()
	if got[0] != "msg="+id+"&dir=in" {
		t.Fatal(got)
	}
}
