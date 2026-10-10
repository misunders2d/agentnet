package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// answerRT answers matching Hub requests itself (a status and a body), as a
// Hub that misbehaves for one path would; others pass through.
type answerRT struct {
	base http.RoundTripper
	mu   *sync.Mutex
	path *string // the path part answered here ("" passes everything)
	code int
	body string
}

func (rt answerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	rt.mu.Lock()
	path := *rt.path
	rt.mu.Unlock()
	if path == "" || r.Method != "GET" || !strings.Contains(r.URL.Path, path) {
		return rt.base.RoundTrip(r)
	}
	return &http.Response{StatusCode: rt.code, Status: http.StatusText(rt.code), Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader(rt.body)), Request: r}, nil
}

// answerHub makes a's Hub answer GET requests whose path contains the part
// set with the returned function with code and body ("" stops it).
func answerHub(a *Agent, code int, body string) func(string) {
	var mu sync.Mutex
	path := ""
	a.hub.http.Transport = answerRT{a.hub.http.Transport, &mu, &path, code, body}
	return func(p string) {
		mu.Lock()
		path = p
		mu.Unlock()
	}
}

// dmToFresh is a DM from bob to carol, a device that has pinned no person
// yet and whose daemon is stopped: turns are handed to it directly, as its
// push stream would.
func dmToFresh(t *testing.T) (w *world, carol *Agent, turn func(string) envelope.Envelope) {
	t.Helper()
	w = newWorld(t, "")
	runAgent(t, w.bob)
	carol = mustJoin(t, filepath.Join(t.TempDir(), "carol"), w.aliceInvites("carol"), "desk")
	stopCarol := runAgent(t, carol)
	persons(t, w.bob, carol)
	conv := newDM(t, w.bob, carol)
	stopCarol()
	if _, pinned, _ := carol.store.personByAddress(w.bob.Address); pinned {
		t.Fatal("carol pinned bob's person before any turn")
	}
	_, root := rootOf(t, w.bob, conv)
	return w, carol, func(body string) envelope.Envelope {
		return craft(t, w.bob, carol, envelope.Inner{Kind: envelope.KindMessage, Body: body, Conv: conv, Root: root, LID: protocol.NewID(), Origin: envelope.OriginUI})
	}
}

// Receiving treats only typed failures as transient: a plain error (a failed
// check, a record this program cannot read) fails the same way again.
func TestTransientIsTyped(t *testing.T) {
	for _, c := range []struct {
		err  error
		want bool
	}{
		{errors.New("invalid topic action"), false},
		{fmt.Errorf("%w: a step", errPersonRecord), false},
		{&HubError{Status: 404}, false},
		{&HubError{Status: 503}, true},
		{&HubError{Status: http.StatusTooManyRequests}, true},
		{&url.Error{Op: "Get", URL: "https://hub", Err: errInjected}, true},
		{fmt.Errorf("asking: %w", context.DeadlineExceeded), true},
		{errors.Join(errPermanent, &HubError{Status: 503}), false},
		{nil, false},
	} {
		if got := transient(c.err); got != c.want {
			t.Errorf("transient(%v) = %v", c.err, got)
		}
	}
}

// RS-1/FLOOD-5: an admission error that is neither a hold nor transient
// (here the Hub's answer for the sender's person is unreadable) holds the
// message for pending proof with a quarantined receipt; it never ends the
// push stream, which would bring the same message back first forever.
func TestAdmissionErrorIsHeldNotStreamEnding(t *testing.T) {
	w, carol, turn := dmToFresh(t)
	answer := answerHub(carol, http.StatusOK, "<html>not the Hub</html>")
	_, name, _ := protocol.SplitAddress(w.bob.Address)
	answer("/" + name + "/profile")
	env := turn("an unreadable profile answer")
	raw, _ := json.Marshal(env)
	healthy, err := readStream(strings.NewReader("event: message\ndata: "+string(raw)+"\n\nevent: ping\ndata: {}\n\n"), nil, func(ev, data string) error { return carol.dispatch(tctx(t), ev, data) })
	if !healthy || err != io.EOF {
		t.Fatalf("the stream ended on an admission error: healthy %v, %v", healthy, err)
	}
	if r := heldReason(t, carol, env.ID); r != reasonProof {
		t.Fatalf("held as %q", r)
	}
	if s, err := carol.store.disposition(env.ID); err != nil || s != protocol.StateQuarantined {
		t.Fatalf("receipt %q %v", s, err)
	}
	// Proof that comes later admits it (the ordinary proof queue).
	answer("")
	carol.retryProof(tctx(t))
	if !inboxHas(t, carol, env.ID) {
		t.Fatalf("not admitted once its proof came: %s", heldReason(t, carol, env.ID))
	}
	if s, _ := carol.store.disposition(env.ID); s != protocol.StateDelivered {
		t.Fatalf("receipt after admission %q", s)
	}
}

// RS-1: a transient failure leaves a conversation message unacknowledged so
// the Hub pushes it again, but only receiveAttempts times in a row; then it
// is set aside for pending proof instead of blocking everything after it.
func TestTransientAdmissionFailureIsSetAside(t *testing.T) {
	w, carol, turn := dmToFresh(t)
	answer := answerHub(carol, http.StatusServiceUnavailable, `{"error":"busy"}`)
	_, name, _ := protocol.SplitAddress(w.bob.Address)
	answer("/" + name + "/profile")
	env := turn("a busy Hub")
	// A stopping daemon's cancelled request is never counted against a message.
	stopping, cancel := context.WithCancel(context.Background())
	cancel()
	for range receiveAttempts + 1 {
		if err := carol.storeReceived(stopping, env); err == nil {
			t.Fatal("a cancelled admission was set aside")
		}
	}
	for i := 1; i < receiveAttempts; i++ {
		if err := carol.storeReceived(tctx(t), env); err == nil || !transient(err) {
			t.Fatalf("attempt %d: %v", i, err)
		}
		if seen, _ := carol.store.seen(env.ID); seen {
			t.Fatalf("attempt %d: set aside early", i)
		}
	}
	if err := carol.storeReceived(tctx(t), env); err != nil {
		t.Fatalf("attempt %d still ends the stream: %v", receiveAttempts, err)
	}
	if r := heldReason(t, carol, env.ID); r != reasonProof {
		t.Fatalf("set aside as %q", r)
	}
	answer("")
	carol.retryProof(tctx(t))
	if !inboxHas(t, carol, env.ID) {
		t.Fatal("a set-aside message was not admitted once the Hub answered")
	}
}

// FLOOD-5: a DM history copy whose topic event this version does not know
// is held as invalid, as the group path holds it; before, its raw error
// ended the stream and the Hub pushed it again first forever.
func TestDMHistoryUnknownTopicActionIsHeld(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	phone := linked(t, w.alice)
	waitNamedAgentCaps(t, phone)
	conv := newDM(t, w.bob, w.alice)
	sendConv(t, w.bob, conv, ConvOutgoing{Body: "m0"})
	eventually(t, "m0 on the phone", func() bool { return len(convBodies(t, phone, conv)) == 1 })
	_, raw := rootOf(t, w.alice, conv)
	item := HistoryItem{V: 1, From: w.bob.Address, FromKey: w.bob.Self().Fingerprint(), ID: protocol.NewID(), LID: protocol.NewID(), TS: time.Now().Unix(),
		Kind: envelope.KindMessage, Body: "future", Origin: envelope.OriginUI, At: time.Now().UnixMilli(),
		Topic: protocol.NewID(), TopicEvent: &envelope.TopicEvent{Action: "archive"}}
	c, err := w.alice.historyCopy(phone.id.Public(phone.Address), conv, raw, item)
	if err != nil {
		t.Fatal(err)
	}
	if err = phone.storeReceived(tctx(t), c.env); err != nil {
		t.Fatalf("a newer topic action ended the stream: %v", err)
	}
	if r := heldReason(t, phone, c.env.ID); r != reasonInvalid {
		t.Fatalf("held as %q", r)
	}
}

// RS-1 (group): a group turn whose admission refreshes every member's
// person record, while one other member is frozen on the reader, waits for
// proof; before, errPersonConflict ended the reader's stream for good.
func TestGroupTurnWithFrozenOtherMemberIsHeld(t *testing.T) {
	w, carol, p, stops := groupTurnsFixture(t)
	stops[w.bob]()
	freeze(t, w.bob, carol)
	sent, err := w.alice.SendConv(tctx(t), p.State.Conv, ConvOutgoing{Body: "while carol is frozen at bob"})
	if err != nil {
		t.Fatal(err)
	}
	var id string
	for _, c := range sent.Copies {
		if c.To == w.bob.Address {
			id = c.ID
		}
	}
	if id == "" {
		t.Fatalf("no copy for bob: %+v", sent.Copies)
	}
	env := groupTurnEnvelope(t, w.alice, id)
	if err := w.bob.storeReceived(tctx(t), env); err != nil {
		t.Fatalf("a frozen other member ended the stream: %v", err)
	}
	if r := heldReason(t, w.bob, env.ID); r != reasonProof {
		t.Fatalf("held as %q", r)
	}
}

// RS-3: new evidence never moves the look at held messages back to the
// first row. With a look started by evidence before every page (as members
// pushes and wakes do), a held message past the first pages is still
// looked at and admitted once its proof is here.
func TestHeldLookReachesRowsPastFirstPage(t *testing.T) {
	w, carol, turn := dmToFresh(t)
	// Messages that stay held, before the one whose proof will come.
	filler := turn("filler")
	raw, _ := json.Marshal(filler)
	for i := range 2*proofPage + 10 {
		if _, err := carol.store.db.Exec(`INSERT INTO quarantine(id, sender, reason, envelope, received_at) VALUES(?, ?, ?, ?, ?)`,
			protocol.NewID(), w.bob.Address, reasonProof, strings.Replace(string(raw), filler.ID, protocol.NewID(), 1), 1000+i); err != nil {
			t.Fatal(err)
		}
	}
	answer := answerHub(carol, http.StatusNotFound, `{"error":"no person"}`)
	_, name, _ := protocol.SplitAddress(w.bob.Address)
	answer("/" + name + "/profile")
	env := turn("held past the first pages")
	if err := carol.storeReceived(tctx(t), env); err != nil || heldReason(t, carol, env.ID) != reasonProof {
		t.Fatalf("not held for proof: %v %q", err, heldReason(t, carol, env.ID))
	}
	answer("")
	for range 6 {
		carol.convWork.due(convRetry) // evidence before every page
		carol.convSync(tctx(t))
		if inboxHas(t, carol, env.ID) {
			return
		}
	}
	t.Fatal("a held message past the first page was never looked at again")
}

// RS-3: the look at held messages keeps its place across a restart, and
// evidence during it adds one more full look once it ends.
func TestHeldLookIsKept(t *testing.T) {
	w := newWorld(t, "")
	a := w.alice
	env := sealTo(t, w.bob, a, envelope.Inner{Kind: envelope.KindMessage, Body: "x"})
	raw, _ := json.Marshal(env)
	for i := range proofPage + 5 {
		if _, err := a.store.db.Exec(`INSERT INTO quarantine(id, sender, reason, envelope, received_at) VALUES(?, ?, ?, ?, ?)`,
			protocol.NewID(), w.bob.Address, reasonProof, string(raw), 1000+i); err != nil {
			t.Fatal(err)
		}
	}
	if !a.retryProof(tctx(t)) {
		t.Fatal("one page of more")
	}
	a.heldEvidence()
	reopened, err := Open(a.home)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopened.convWork.mu.Lock()
	l := reopened.heldLookNow()
	reopened.convWork.mu.Unlock()
	if l.pos() == (heldPos{}) || !l.Again {
		t.Fatalf("a restart lost the look's place: %+v", l)
	}
	if !reopened.retryProof(tctx(t)) {
		t.Fatal("the end of a look with evidence during it did not start another")
	}
	reopened.convWork.mu.Lock()
	l = reopened.heldLookNow()
	reopened.convWork.mu.Unlock()
	if l != (heldLook{}) {
		t.Fatalf("the next look does not start at the first row: %+v", l)
	}
}

// RS-3: a wake the daemon sends itself for its own work (a job ready,
// history copies queued) is no evidence for held messages; a wake from
// another process still is.
func TestDaemonSelfWakeStartsNoHeldLook(t *testing.T) {
	w := newWorld(t, "")
	a := w.alice
	a.statusLive.Store(true) // as while statusLoop runs: a wake does not wake again
	stop, err := a.startWorker(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	a.convWork.take()
	a.store.onJobReady() // the daemon's own work
	eventually(t, "the self-wake served", func() bool { return a.convWork.bits.Load()&convHistory != 0 })
	if a.convWork.take()&convRetry != 0 {
		t.Fatal("the daemon's own wake started another look at held messages")
	}
	cli, err := Open(a.home) // another process on this home, like the CLI
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	cli.store.onJobReady()
	eventually(t, "another process's wake", func() bool { return a.convWork.bits.Load()&convRetry != 0 })
}

// FLOOD-1: a member list that differs only in what is availability (here
// presence, and the version and suspension a newer Hub reports) releases
// waiting copies but neither compares persons nor looks at held messages.
func TestMemberAvailabilityOnlyChangesNoAuthorityWork(t *testing.T) {
	w := newWorld(t, "")
	a := w.bob
	push := func(list string) uint32 {
		t.Helper()
		a.convWork.take()
		a.onMembers([]byte(list))
		return a.convWork.take()
	}
	person := `"person":{"id":"` + strings.Repeat("a", 32) + `","seq":2,"hash":"` + strings.Repeat("b", 64) + `"}`
	if push(`{"members":[{"address":"vitalii/desk","presence":"connected","joined":5,`+person+`}]}`)&(convPersons|convRetry) != convPersons|convRetry {
		t.Fatal("first list did no authority work")
	}
	a.convWork.retried.Store(time.Now().Unix())
	work := push(`{"members":[{"address":"vitalii/desk","presence":"offline","joined":5,"version":"v0.8.17","suspended":true,` + person + `}]}`)
	if work&(convPersons|convRetry) != 0 || work&convRelease == 0 {
		t.Fatalf("availability-only change: %b", work)
	}
	if push(`{"members":[{"address":"vitalii/desk","presence":"offline","joined":5,"agent":true,`+person+`}]}`)&(convPersons|convRetry) != convPersons|convRetry {
		t.Fatal("a new agent hint did no authority work")
	}
}

// FLOOD-6: a conversation history copy never goes back to the own device
// it came from: that device holds the message (it forwarded it here).
func TestHistoryCatchupSkipsRowsFromTheTargetDevice(t *testing.T) {
	w, phone, _, ids := historyCatchupFixture(t, 3)
	for range 4 {
		if _, err := w.alice.historyCatchupPage(tctx(t), phone.Self()); err != nil {
			t.Fatal(err)
		}
	}
	if n := deliverDeviceHistory(t, w.alice, phone, envelope.SubHistory); n < len(ids) {
		t.Fatalf("%d history copies to the phone", n)
	}
	for _, id := range ids {
		var via string
		if err := phone.store.db.QueryRow(`SELECT coalesce(via,'') FROM inbox WHERE id=?`, id).Scan(&via); err != nil || via != w.alice.Address {
			t.Fatalf("%s at the phone via %q: %v", id, via, err)
		}
	}
	if err := phone.reconcileHistory(); err != nil {
		t.Fatal(err)
	}
	for range 4 {
		if _, err := phone.historyCatchupPage(tctx(t), w.alice.Self()); err != nil {
			t.Fatal(err)
		}
	}
	var echoes int
	if err := phone.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE recipient=? AND sub='history'`, w.alice.Address).Scan(&echoes); err != nil || echoes != 0 {
		t.Fatalf("%d history copies back to the device they came from (%v)", echoes, err)
	}
}

// FLOOD-6: a device-history copy never goes back to the own device that
// forwarded it here, also when neither its sender nor its recipient is
// that device (it came there from a third own device).
func TestDeviceHistorySkipsTheForwarder(t *testing.T) {
	w, phone, _, _ := historyCatchupFixture(t, 0)
	stop := runAgent(t, w.alice)
	tablet, awaited, _ := linkPhone(t, w.alice, "tablet")
	request := pendingLink(t, w.alice)
	stop()
	if err := w.alice.DecideLink(tctx(t), request.ID, true); err != nil {
		t.Fatal(err)
	}
	if out := <-awaited; out.err != nil {
		t.Fatal(out.err)
	}
	asked, err := tablet.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindQuestion, Body: "from the tablet"})
	if err != nil {
		t.Fatal(err)
	}
	reply := sealTo(t, w.bob, tablet, envelope.Inner{Kind: envelope.KindAnswer, Body: "to the tablet", ReplyTo: asked.ID})
	if err := tablet.verifyAndStore(tctx(t), reply); err != nil {
		t.Fatal(err)
	}
	forward := func(from, to *Agent) int {
		t.Helper()
		for range 6 {
			if _, err := from.deviceHistoryPage(to.Self()); err != nil {
				t.Fatal(err)
			}
		}
		return deliverDeviceHistory(t, from, to, envelope.SubDeviceHistory)
	}
	if n := forward(tablet, w.alice); n != 2 {
		t.Fatalf("tablet -> alice: %d copies", n)
	}
	if n := forward(w.alice, phone); n != 2 {
		t.Fatalf("alice -> phone: %d copies", n)
	}
	for range 6 {
		if _, err := phone.deviceHistoryPage(w.alice.Self()); err != nil {
			t.Fatal(err)
		}
	}
	var echoes int
	if err := phone.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE recipient=? AND sub=?`, w.alice.Address, envelope.SubDeviceHistory).Scan(&echoes); err != nil || echoes != 0 {
		t.Fatalf("%d device-history copies back to their forwarder (%v)", echoes, err)
	}
}
