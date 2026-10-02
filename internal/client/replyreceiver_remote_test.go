package client

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestReceiverNormalCapabilityProfileAndUnsupportedPeer(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.bob)
	var profile protocol.Profile
	eventually(t, "normal signed receiver capability", func() bool {
		return w.alice.hub.do(tctx(t), "GET", "/v1/agents/"+w.bob.Address+"/profile", nil, &profile) == nil && profile.Supports(w.bob.Address, w.bob.Self().SignKey, protocol.CapReplyReceiver)
	})
	if !slices.IsSorted(ownCaps) || len(profile.Sessions) != 1 {
		t.Fatalf("normal capabilities/session invalid %v %+v", ownCaps, profile.Sessions)
	}
	for _, raw := range profile.Caps {
		var record protocol.CapsRecord
		if json.Unmarshal(raw, &record) != nil {
			t.Fatal("invalid normal profile capability record")
		}
		count := 0
		for _, name := range record.Caps {
			if name == protocol.CapReplyReceiver {
				count++
			}
		}
		if count != 1 || record.Verify(w.bob.Self().SignKey) != nil {
			t.Fatalf("normal signed profile must advertise rcv1 once: %v", record.Caps)
		}
	}
	if e := w.alice.requireParticipationCaps(tctx(t), w.bob.Self(), protocol.CapReplyReceiver); e != nil {
		t.Fatal(e)
	}
	// An older session remains unsupported even after current local activation.
	old := protocol.CapsRecord{Address: w.bob.Address, Session: profile.Sessions[0], Caps: slices.DeleteFunc(slices.Clone(ownCaps), func(c string) bool { return c == protocol.CapReplyReceiver }), TS: time.Now().Unix() + 100}
	old.Sign(w.bob.id.Sign)
	if e := w.bob.hub.do(tctx(t), "PUT", "/v1/caps", old, nil); e != nil {
		t.Fatal(e)
	}
	if e := w.alice.requireParticipationCaps(tctx(t), w.bob.Self(), protocol.CapReplyReceiver); e == nil {
		t.Fatal("unsupported peer accepted selected receiver capability")
	}
}

func TestReceiverDelegationCannotClaimDefaultJob(t *testing.T) {
	home := t.TempDir()
	s, err := openStore(filepath.Join(home, "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	self, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	phone, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	a := &Agent{Address: "alice/laptop", id: self, store: s, home: home}
	peer := phone.Public("alice/phone")
	if err = s.pin(peer); err != nil {
		t.Fatal(err)
	}
	if _, err = a.GrantTasks(peer.Address); err != nil {
		t.Fatal(err)
	}
	request := envelope.ReceiverRequest{ID: protocol.NewID(), From: peer.Address, FromKey: peer.Fingerprint(), To: "bob/worker", ToKey: peer.Fingerprint(), TS: time.Now().Unix(), Kind: envelope.KindMessage, Body: "original local request"}
	choice := envelope.ReceiverChoice{Kind: "human"}
	route := envelope.ReceiverRoute{Op: "delegate", Host: a.Address, HostKey: a.Self().Fingerprint(), RequestRef: request.ID, DelegationID: protocol.NewID()}
	route.RequestDigest, err = envelope.ReceiverDigest(route, request, choice)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(envelope.ReceiverOperation{V: 1, Request: &request, Receiver: &choice})
	in := envelope.Inner{V: envelope.Version, ID: route.DelegationID, From: peer.Address, To: a.Address, TS: request.TS, Kind: envelope.KindTask, Body: string(body), ReceiverRoute: &route}
	if err = envelope.ValidateReceiverRoute(in); err != nil {
		t.Fatal(err)
	}
	if err = s.addInbox(in, peer.Fingerprint()); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err = s.db.QueryRow(`SELECT receiver_route FROM inbox WHERE id=?`, in.ID).Scan(&stored); err != nil || stored != receiverRouteJSON(&route) {
		t.Fatalf("typed route missing: %q %v", stored, err)
	}
	for _, state := range []string{stateAwaiting, stateAccepted, statePending} {
		if _, err = s.db.Exec(`UPDATE inbox SET state=? WHERE id=?`, state, in.ID); err != nil {
			t.Fatal(err)
		}
		if job, claimed, err := s.claimJob("default-harness"); err != nil || claimed {
			t.Fatalf("setup %s claimed default job %+v/%t: %v", state, job, claimed, err)
		}
	}
	ordinary := envelope.Inner{ID: protocol.NewID(), From: peer.Address, To: a.Address, TS: request.TS, Kind: envelope.KindTask, Body: "independent remote task"}
	if err = s.addInbox(ordinary, peer.Fingerprint()); err != nil {
		t.Fatal(err)
	}
	job, claimed, err := s.claimJob("default-harness")
	if err != nil || !claimed || job.ID != ordinary.ID {
		t.Fatalf("independent exact-key task grant changed: %+v/%t %v", job, claimed, err)
	}
}

func TestReceiverRemotePreparedApprovalReadyFiles(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	phone, awaited, _ := linkPhone(t, w.alice, "phone")
	req := pendingLink(t, w.alice)
	if e := w.alice.DecideLink(tctx(t), req.ID, true); e != nil {
		t.Fatal(e)
	}
	if out := <-awaited; out.err != nil {
		t.Fatal(out.err)
	}
	stopPhone := runAgent(t, phone)
	eventually(t, "receiver capability", func() bool {
		var profile protocol.Profile
		e := w.alice.hub.do(tctx(t), "GET", "/v1/agents/"+w.alice.Address+"/profile", nil, &profile)
		return e == nil && profile.Supports(w.alice.Address, w.alice.Self().SignKey, protocol.CapReplyReceiver)
	})
	path, content := writeFile(t, t.TempDir(), "original.txt", 123)
	receiver := ReplyReceiver{Host: &ReplyReceiverHost{Address: w.alice.Address, Fingerprint: w.alice.Self().Fingerprint()}, Kind: "human"}
	sent, e := phone.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "original from phone", Files: []string{path}, ReplyReceiver: &receiver})
	if e != nil {
		t.Fatal(e)
	}
	if sent.State != stateReceiverWaiting {
		t.Fatalf("original exposed before ready: %+v", sent)
	}
	bindings := receiverBindings(t, phone)
	if len(bindings) != 1 {
		t.Fatalf("origin %+v", bindings)
	}
	origin, e := replyReceiverIn(phone.store.db, bindings[0].ID)
	if e != nil {
		t.Fatal(e)
	}
	setup := origin.remote.Route.DelegationID
	var setupState, setupWhy, setupBody string
	phone.store.db.QueryRow(`SELECT state,coalesce(error,''),body FROM outbox WHERE id=?`, setup).Scan(&setupState, &setupWhy, &setupBody)
	t.Logf("setup state=%s detail=%s original=%s destination=%s", setupState, setupWhy, sent.ID, w.alice.Address)
	var storedRoute string
	w.alice.store.db.QueryRow(`SELECT receiver_route FROM inbox WHERE id=?`, setup).Scan(&storedRoute)
	t.Logf("setup stored route=%s", storedRoute)
	var heldReason string
	w.alice.store.db.QueryRow(`SELECT reason FROM quarantine WHERE id=?`, setup).Scan(&heldReason)
	t.Logf("setup held=%s", heldReason)
	eventually(t, "local delegation approval", func() bool { state, e := w.alice.store.jobState(setup); return e == nil && state == stateAwaiting })
	var n int
	w.bob.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE id=?`, sent.ID).Scan(&n)
	if n != 0 {
		t.Fatal("unapproved original delivered")
	}
	var frozen string
	phone.store.db.QueryRow(`SELECT envelope FROM outbox WHERE id=?`, sent.ID).Scan(&frozen)
	stopPhone()
	if _, e = phone.Cleanup(false); e != nil {
		t.Fatal(e)
	}
	home := phone.home
	phone.Close()
	phone, e = Open(home)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { phone.Close() })
	stopPhone = runAgent(t, phone)
	if e = w.alice.Accept(setup); e != nil {
		t.Fatal(e)
	}
	eventually(t, "ready releases original", func() bool {
		var n int
		w.bob.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE id=?`, sent.ID).Scan(&n)
		return n == 1
	})
	var after string
	phone.store.db.QueryRow(`SELECT envelope FROM outbox WHERE id=?`, sent.ID).Scan(&after)
	if frozen != after {
		t.Fatal("ready resealed or altered original")
	}
	imported, e := replyReceiverIn(w.alice.store.db, setup)
	if e != nil || imported.remote == nil || imported.remote.Request.From != phone.Address || imported.remote.Role != "imported" {
		t.Fatalf("imported %+v %v", imported, e)
	}
	stream, _, e := w.alice.OpenFileFrom(tctx(t), "in", setup, 0)
	if e != nil {
		t.Fatal(e)
	}
	if got := readAll(t, stream); !bytes.Equal(got, content) {
		t.Fatal("delegated original file changed")
	}
	stopPhone() // originating daemon gone; selected laptop retains authorized request and files
	reply, e := w.bob.Reply(tctx(t), sent.ID, "human reply to selected laptop")
	if e != nil {
		t.Fatal(e)
	}
	eventually(t, "exact selected input", func() bool {
		b, e := replyReceiverIn(w.alice.store.db, setup)
		if e != nil {
			return false
		}
		var n int
		w.alice.store.db.QueryRow(`SELECT count(*) FROM reply_receiver_inputs WHERE binding=?`, b.ID).Scan(&n)
		return n == 1
	})
	var selectedID string
	if e = w.alice.store.db.QueryRow(`SELECT inbox_id FROM reply_receiver_inputs WHERE binding=?`, setup).Scan(&selectedID); e != nil {
		t.Fatal(e)
	}
	if selectedID == reply.ID {
		t.Fatal("fanout reused physical envelope id")
	}
	if j, claimed, e := w.alice.store.claimJob("default"); e != nil || claimed {
		t.Fatalf("setup or human selection ran default: %+v %t %v", j, claimed, e)
	}
}
