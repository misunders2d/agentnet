package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// staleCapsKey marks a request context whose profile reads staleCapsRT
// answers without capability records.
type staleCapsKey struct{}

// staleCapsRT answers a profile read made with a staleCapsKey context as the
// Hub answered it before the device's live session published its record:
// the same profile, without records.
type staleCapsRT struct{ base http.RoundTripper }

func (rt staleCapsRT) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := rt.base.RoundTrip(r)
	if err != nil || r.Context().Value(staleCapsKey{}) == nil || r.Method != "GET" || !strings.HasSuffix(r.URL.Path, "/profile") {
		return resp, err
	}
	var prof protocol.Profile
	err = json.NewDecoder(resp.Body).Decode(&prof)
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	prof.Caps = nil
	data, _ := json.Marshal(prof)
	resp.Body, resp.ContentLength = io.NopCloser(bytes.NewReader(data)), int64(len(data))
	resp.Header.Del("Content-Length")
	return resp, nil
}

// A copy kept waiting goes out once its recipient can read it, also when
// the Hub's news of that came before the copy was stored: the send read the
// profile in the window after the recipient's new session connected and
// before it published its record (here that read is answered without
// records), and the members push announcing the record was spent on a
// release pass that found nothing waiting yet. No Hub event follows; the
// sender's own wake after storing it looks again. (A linked device's
// invite of its person's agent to the just-restarted host stayed waiting
// for good: TestSelfConsentLinkedDevices.)
func TestWaitingCopyLooksAgainOnceStored(t *testing.T) {
	w := newWorld(t, "")
	w.alice.hub.http.Transport = staleCapsRT{w.alice.hub.http.Transport}
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	conv := newDM(t, w.alice, w.bob)
	sent, err := w.alice.SendConv(context.WithValue(tctx(t), staleCapsKey{}, true), conv, ConvOutgoing{Body: "decided on an older read"})
	if err != nil || sent.State != stateConvWaiting || !strings.Contains(sent.Detail, "needs to update AgentNet") {
		t.Fatalf("setup: %+v %v", sent, err)
	}
	eventually(t, "the waiting copy to go out", func() bool {
		return strings.Join(convBodies(t, w.bob, conv), "|") == "in:decided on an older read"
	})
}

// A message kept as waiting is not released to a person who, by the time
// the device can read conversations again, has published a different
// record: the profile read freezes the person and the message stays
// waiting, with its content and reason, and is never delivered.
func TestWaitingNotReleasedToFrozenPerson(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	conv := newDM(t, w.alice, w.bob)
	label, name, _ := protocol.SplitAddress(w.bob.Address)
	var prof protocol.Profile
	if err := w.alice.hub.do(tctx(t), "GET", "/v1/agents/"+label+"/"+name+"/profile", nil, &prof); err != nil || len(prof.Sessions) != 1 {
		t.Fatalf("profile: %+v %v", prof, err)
	}
	publish := func(ts int64, caps ...string) {
		rec := protocol.CapsRecord{Address: w.bob.Address, Session: prof.Sessions[0], Caps: caps, TS: ts}
		rec.Sign(w.bob.id.Sign)
		if err := w.bob.hub.do(tctx(t), "PUT", "/v1/caps", rec, nil); err != nil {
			t.Fatal(err)
		}
	}
	publish(time.Now().Unix() + 100)
	sent, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "waiting confidential DM"})
	if err != nil || sent.State != stateConvWaiting {
		t.Fatalf("setup: %+v %v", sent, err)
	}
	var body, reason string
	w.alice.store.db.QueryRow(`SELECT body, error FROM outbox WHERE id = ?`, sent.ID).Scan(&body, &reason)

	freeze(t, w.alice, w.bob)
	publish(time.Now().Unix()+200, protocol.CapEnv2, protocol.CapPerson)
	feats, err := w.alice.relayFeatures(tctx(t))
	if err != nil {
		t.Fatal(err)
	}
	for range 2 { // a later event changes nothing either
		w.alice.releaseConv(tctx(t), feats)
		if err := w.alice.FlushOutbox(tctx(t)); err != nil {
			t.Fatal(err)
		}
	}
	p, _, err := w.alice.store.personByAddress(w.bob.Address)
	if err != nil || p.info.State != personConflict {
		t.Fatalf("conflict missing: %+v %v", p.info, err)
	}
	var state, body2, reason2 string
	w.alice.store.db.QueryRow(`SELECT state, body, error FROM outbox WHERE id = ?`, sent.ID).Scan(&state, &body2, &reason2)
	if state != stateConvWaiting || body2 != body || reason2 != reason {
		t.Fatalf("frozen peer DM released or changed: state=%s body=%q reason=%q", state, body2, reason2)
	}
	time.Sleep(200 * time.Millisecond)
	if n := inboxCount(t, w.bob, `body = ?`, "waiting confidential DM"); n != 0 {
		t.Fatal("delivered to a frozen person")
	}
}

// A conversation message already queued (the Hub was out of reach) is not
// sent once its person is frozen; other messages are unaffected.
func TestQueuedNotSentToFrozenPerson(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	conv := newDM(t, w.alice, w.bob)
	_, raw := rootOf(t, w.alice, conv)
	r, _ := w.bob.id.Public(w.bob.Address).Recipient()
	in := envelope.Inner{V: envelope.Version2, ID: protocol.NewID(), From: w.alice.Address, To: w.bob.Address, TS: time.Now().Unix(),
		Kind: envelope.KindMessage, Body: "queued before the conflict", Conv: conv, LID: protocol.NewID(), Root: raw, Origin: envelope.OriginUI}
	env, err := envelope.Seal(in, w.alice.id.Sign, r)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.alice.store.addConvOutbox([]outCopy{{env: env, in: in, state: stateQueued}}, envelope.Inner{}, nil, ""); err != nil {
		t.Fatal(err)
	}
	freeze(t, w.alice, w.bob)
	plain, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "a plain message still goes"})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.alice.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the plain message", func() bool { return inboxCount(t, w.bob, `id = ?`, plain.ID) == 1 })
	var state string
	w.alice.store.db.QueryRow(`SELECT state FROM outbox WHERE id = ?`, env.ID).Scan(&state)
	if state != stateQueued || inboxCount(t, w.bob, `id = ?`, env.ID) != 0 {
		t.Fatalf("a queued DM message went to a frozen person: %s", state)
	}
}

// No DM is started with a frozen person.
func TestCreateDMRefusesFreshConflict(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	newDM(t, w.alice, w.bob) // pins bob's person at alice
	freeze(t, w.alice, w.bob)
	if _, err := w.alice.CreateDM(tctx(t), w.bob.Address); !errors.Is(err, errPersonConflict) {
		t.Fatalf("a DM was started with a person whose record changed: %v", err)
	}
	if convs, _ := w.alice.Conversations(); len(convs) != 1 {
		t.Fatalf("%d conversations", len(convs))
	}
}

// Cleanup keeps the files of a conversation message kept as waiting (here
// the recipient's device cannot read conversations yet; the Hub being out
// of reach waits the same way): the spool holds their only encrypted copy,
// uploaded once the message is released. Removing it failed the send for
// good with "attachment is not a completed upload".
func TestCleanupKeepsWaitingConversationFiles(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	conv := newDM(t, w.alice, w.bob)
	label, name, _ := protocol.SplitAddress(w.bob.Address)
	var prof protocol.Profile
	if err := w.alice.hub.do(tctx(t), "GET", "/v1/agents/"+label+"/"+name+"/profile", nil, &prof); err != nil || len(prof.Sessions) != 1 {
		t.Fatalf("profile: %+v %v", prof, err)
	}
	publish := func(ts int64, caps ...string) {
		rec := protocol.CapsRecord{Address: w.bob.Address, Session: prof.Sessions[0], Caps: caps, TS: ts}
		rec.Sign(w.bob.id.Sign)
		if err := w.bob.hub.do(tctx(t), "PUT", "/v1/caps", rec, nil); err != nil {
			t.Fatal(err)
		}
	}
	publish(time.Now().Unix() + 100)
	path, data := writeFile(t, t.TempDir(), "payroll.csv", 5000)
	sent, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "Payroll", Files: []OutgoingFile{{Path: path}}})
	if err != nil || sent.State != stateConvWaiting {
		t.Fatalf("setup: %+v %v", sent, err)
	}
	r, err := w.alice.Cleanup(false)
	if err != nil || r.SpoolFiles != 0 {
		t.Fatalf("cleanup = %+v, %v", r, err)
	}
	if entries, _ := os.ReadDir(filepath.Join(w.alice.home, "spool")); len(entries) != 1 {
		t.Fatalf("the waiting message's spool: %d files", len(entries))
	}
	if n := count(t, w.alice, "uploads"); n != 1 {
		t.Fatalf("the waiting message's uploads: %d rows", n)
	}

	publish(time.Now().Unix()+200, protocol.CapEnv2, protocol.CapPerson)
	feats, err := w.alice.relayFeatures(tctx(t))
	if err != nil {
		t.Fatal(err)
	}
	w.alice.releaseConv(tctx(t), feats)
	if err := w.alice.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob to hold it", func() bool { return inboxCount(t, w.bob, `id = ?`, sent.ID) == 1 })
	got, f, err := w.bob.OpenAttachment(tctx(t), sent.ID, 0)
	if err != nil || f.Name != "payroll.csv" || !bytes.Equal(readAll(t, got), data) {
		t.Fatalf("open: %+v %v", f, err)
	}
}
