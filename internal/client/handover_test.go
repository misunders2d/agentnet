package client

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
)

func outboxEnvelope(t *testing.T, a *Agent, id string) envelope.Envelope {
	t.Helper()
	var raw string
	if err := a.store.db.QueryRow(`SELECT envelope FROM outbox WHERE id = ?`, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var env envelope.Envelope
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		t.Fatal(err)
	}
	return env
}

func outboxState(t *testing.T, a *Agent, id string) string {
	t.Helper()
	s, _, _, err := a.store.outboxState(id)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// R1 (root): a queued copy sealed for a device that has since left its
// person never goes, though an admin admitted that device and it stays a
// Hub member; it ends not delivered. A copy to a current device still goes.
func TestQueuedCopyNotHandedToRemovedDevice(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	persons(t, w.alice, w.bob)
	phone := linked(t, w.alice)
	conv := newDM(t, w.bob, w.alice)
	sent, err := w.bob.SendConv(tctx(t), conv, ConvOutgoing{Body: "retained queued copy"})
	if err != nil {
		t.Fatal(err)
	}
	var laptopCopy, phoneCopy string
	for _, c := range sent.Copies {
		switch c.To {
		case w.alice.Address:
			laptopCopy = c.ID
		case phone.Address:
			phoneCopy = c.ID
		}
	}
	for _, id := range []string{laptopCopy, phoneCopy} {
		w.bob.store.db.Exec(`UPDATE outbox SET state = 'queued' WHERE id = ?`, id)
	}
	person, _, _ := w.alice.Person()
	if err := phone.RemoveDevice(tctx(t), w.alice.Address); err != nil {
		t.Fatal(err)
	}
	if _, err := w.bob.refreshPerson(tctx(t), person.Person, false); err != nil {
		t.Fatal(err)
	}
	if ok, err := w.bob.mayDeliver(outboxEnvelope(t, w.bob, laptopCopy)); ok || err != nil {
		t.Fatalf("a copy for the removed laptop may go: %v %v", ok, err)
	}
	if s := outboxState(t, w.bob, laptopCopy); s != stateNotDelivered {
		t.Fatalf("the removed laptop's copy: %s", s)
	}
	if ok, err := w.bob.mayDeliver(outboxEnvelope(t, w.bob, phoneCopy)); !ok || err != nil {
		t.Fatalf("the phone's copy held: %v %v", ok, err)
	}
	// The laptop is still a Hub member, and a plain (v1) message to it
	// still goes: a service's messaging is untouched.
	if res, err := w.bob.SendMessage(tctx(t), Outgoing{To: w.alice.Address, Body: "plain"}); err != nil || res.State == stateFailed {
		t.Fatalf("a plain message to the laptop: %+v %v", res, err)
	}
}

// History and file copies to a device that has left this person never go.
func TestHistoryCopyNotHandedToRemovedDevice(t *testing.T) {
	w := newWorld(t, "")
	stopAlice := runAgent(t, w.alice)
	persons(t, w.alice, w.bob)
	phone := linked(t, w.alice)
	conv := newDM(t, w.bob, w.alice)
	if _, err := w.bob.SendConv(tctx(t), conv, ConvOutgoing{Body: "hello"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the laptop to have the conversation", func() bool { return len(convBodies(t, w.alice, conv)) == 1 })
	stopAlice() // nothing sends the copies queued below but the checks
	_, raw := rootOf(t, w.alice, conv)
	me, _, _ := w.alice.store.selfPerson(w.alice.Address)
	dev, _ := me.device(phone.Address)
	item := HistoryItem{V: 1, From: w.bob.Address, FromKey: w.bob.Self().Fingerprint(), ID: "0123456789abcdef0123456789abcdef", LID: "fedcba9876543210fedcba9876543210", TS: 1, Kind: envelope.KindMessage, Body: "old"}
	hist, err := w.alice.historyCopy(dev, conv, raw, item)
	if err != nil {
		t.Fatal(err)
	}
	file, err := w.alice.fileCarrier(dev, conv, raw, `{"v":1}`)
	if err != nil {
		t.Fatal(err)
	}
	tx, _ := w.alice.store.db.Begin()
	if err := insertCopies(tx, []outCopy{hist, file}); err != nil {
		t.Fatal(err)
	}
	tx.Commit()
	if err := w.alice.RemoveDevice(tctx(t), phone.Address); err != nil {
		t.Fatal(err)
	}
	for _, c := range []outCopy{hist, file} {
		if ok, err := w.alice.mayDeliver(c.env); ok || err != nil {
			t.Fatalf("%s copy to the removed phone may go: %v %v", c.in.Sub, ok, err)
		}
		if s := outboxState(t, w.alice, c.env.ID); s != stateNotDelivered {
			t.Fatalf("%s copy: %s", c.in.Sub, s)
		}
	}
}

// A device that leaves its person while a copy's files upload: that copy
// is not posted (decided again after the upload); the other device's is.
func TestRemovedDuringUpload(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	persons(t, w.alice, w.bob)
	phone := linked(t, w.alice)
	conv := newDM(t, w.bob, w.alice)
	person, _, _ := w.alice.Person()
	path, _ := writeFile(t, t.TempDir(), "report.txt", 1000)
	var once sync.Once
	h := &fileGate{base: w.bob.hub.http.Transport}
	h.after = func() {
		once.Do(func() {
			if err := phone.RemoveDevice(tctx(t), w.alice.Address); err != nil {
				t.Errorf("remove: %v", err)
			}
			if _, err := w.bob.refreshPerson(tctx(t), person.Person, false); err != nil {
				t.Errorf("refresh: %v", err)
			}
		})
	}
	w.bob.hub.http.Transport = h
	sent, err := w.bob.SendConv(tctx(t), conv, ConvOutgoing{Body: "file", Files: []OutgoingFile{{Path: path}}})
	w.bob.hub.http.Transport = h.base
	if err != nil {
		t.Fatal(err)
	}
	states := map[string]string{}
	for _, c := range sent.Copies {
		states[c.To] = outboxState(t, w.bob, c.ID)
	}
	if states[w.alice.Address] != stateNotDelivered || states[phone.Address] == stateNotDelivered || h.messages != 1 {
		t.Fatalf("copies %v, %d posted", states, h.messages)
	}
}

// slowRange holds the first range request of a blob until released, and
// records how many were in flight at once.
type slowRange struct {
	base              http.RoundTripper
	started, release  chan struct{}
	inFlight, maxSeen atomic.Int32
	first             atomic.Bool
}

func (s *slowRange) RoundTrip(r *http.Request) (*http.Response, error) {
	if !strings.HasSuffix(r.URL.Path, "/data") {
		return s.base.RoundTrip(r)
	}
	n := s.inFlight.Add(1)
	defer s.inFlight.Add(-1)
	for m := s.maxSeen.Load(); n > m && !s.maxSeen.CompareAndSwap(m, n); m = s.maxSeen.Load() {
	}
	if s.first.CompareAndSwap(false, true) {
		close(s.started)
		<-s.release
	}
	return s.base.RoundTrip(r)
}

// R2 (root): two fetches of one blob (keeping it in the background, a
// person opening it) never write its partial file at once: the second
// waits for the first, then finds it fetched.
func TestConcurrentFetchSerialized(t *testing.T) {
	w := newWorld(t, "")
	path, data := writeFile(t, t.TempDir(), "big.bin", 3<<20)
	if _, err := w.alice.Send(tctx(t), w.bob.Address, "file", "", path); err != nil {
		t.Fatal(err)
	}
	msg := receive(t, w)
	files, err := w.bob.store.attachments(msg.ID)
	if err != nil || len(files) != 1 {
		t.Fatalf("files %v %v", files, err)
	}
	s := &slowRange{base: w.bob.hub.http.Transport, started: make(chan struct{}), release: make(chan struct{})}
	w.bob.hub.http.Transport = s
	errs := make(chan error, 2)
	go func() { errs <- w.bob.fetchCiphertext(tctx(t), files[0]) }()
	<-s.started
	go func() { errs <- w.bob.fetchCiphertext(tctx(t), files[0]) }()
	time.Sleep(300 * time.Millisecond) // the second, unserialized, would be in flight by now
	close(s.release)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if s.maxSeen.Load() != 1 {
		t.Fatalf("%d range requests in flight at once", s.maxSeen.Load())
	}
	r, _, err := w.bob.OpenAttachment(tctx(t), msg.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(r)
	r.Close()
	if string(got) != string(data) {
		t.Fatal("the fetched file differs")
	}
	if _, err := os.Stat(w.bob.downloadPath(files[0].BlobID) + ".part"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a partial file is left: %v", err)
	}
}
