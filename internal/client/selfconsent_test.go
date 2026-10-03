package client

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/testhub"
)

// selfConsentWorld: alice and bob with persons, running daemons and a DM;
// alice's default responder is selected, so her default agent runs there.
func selfConsentWorld(t *testing.T) (w *world, conv string, stopAlice func()) {
	t.Helper()
	w = newWorld(t, "")
	stopAlice = runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	conv = newDM(t, w.alice, w.bob)
	if err := w.alice.SetResponder(&Responder{Harness: "claude", Dir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	return w, conv, stopAlice
}

// decisionsBy counts the accepts and declines of pid that at holds from
// the device key fp.
func decisionsBy(t *testing.T, at *Agent, conv, pid, fp string) int {
	t.Helper()
	var n int
	if err := at.store.db.QueryRow(`SELECT count(*) FROM participation_events WHERE conv = ? AND pid = ? AND author = ? AND type IN (?, ?)`,
		conv, pid, fp, protocol.EventAccept, protocol.EventDecline).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// storedInvite stores at host an invite of host's agent into conv, signed
// by author's device after edit; nothing is sent.
func storedInvite(t *testing.T, host, author *Agent, conv string, edit func(*protocol.ParticipationEvent)) string {
	t.Helper()
	by, _, err := author.store.selfPerson(author.Address)
	if err != nil {
		t.Fatal(err)
	}
	h, _, err := host.store.selfPerson(host.Address)
	if err != nil {
		t.Fatal(err)
	}
	ev := protocol.ParticipationEvent{V: 1, Conv: conv, PID: protocol.NewID(), Type: protocol.EventInvite, TS: time.Now().Unix(),
		Author:   protocol.EventAuthor{Person: by.info.Person, Roster: by.info.Roster, Address: author.Address, Fingerprint: by.info.Fingerprint},
		Host:     &protocol.ParticipationHost{Person: h.info.Person, Address: host.Address, Fingerprint: h.info.Fingerprint},
		Audience: protocol.AudienceConversation}
	if edit != nil {
		edit(&ev)
	}
	ev.Sign(author.id.Sign)
	raw, _ := json.Marshal(ev)
	if err := host.store.addParticipationEvent(ev, raw); err != nil {
		t.Fatal(err)
	}
	return ev.PID
}

// linkedVia links a new device name to a's person, approved on a by
// approve, and runs its daemon.
func linkedVia(t *testing.T, a *Agent, name string, approve func(context.Context, string) error) *Agent {
	t.Helper()
	dev, awaited, _ := linkPhone(t, a, name)
	if err := approve(tctx(t), pendingLink(t, a).ID); err != nil {
		t.Fatal(err)
	}
	if out := <-awaited; out.err != nil {
		t.Fatal(out.err)
	}
	runAgent(t, dev)
	return dev
}

// D3: alice inviting her own agent on its own device needs no accept. The
// accept is signed exactly once, with a notice resolve dismisses; retries
// and sweeps sign nothing more; bob, who never self-consents, resolves it
// as an ordinary accept of exactly an accept's shape.
func TestSelfConsentHostInvite(t *testing.T) {
	w, conv, _ := selfConsentWorld(t)
	aliceFP := w.alice.Self().Fingerprint()
	p, err := w.alice.InviteAgent(tctx(t), conv, w.alice.Address, nil, []string{aliceFP}, "")
	if err != nil || p.State != PartActive || !p.Claimable() || p.Decision == "" {
		t.Fatalf("own agent from its host: %+v %v", p, err)
	}
	notices, err := w.alice.SelfConsentNotices()
	if err != nil || len(notices) != 1 || notices[0].PID != p.PID || notices[0].Conv != conv || notices[0].Inviter != w.alice.Address {
		t.Fatalf("notice: %+v %v", notices, err)
	}
	if again, err := w.alice.selfConsent(tctx(t), p.PID); again || err != nil {
		t.Fatalf("a second self-consent: %v %v", again, err)
	}
	w.alice.sweepSelfConsent(tctx(t))
	if retry, err := w.alice.AcceptParticipation(tctx(t), p.PID); err != nil || retry.Decision != p.Decision {
		t.Fatalf("a click after it: %+v %v", retry, err)
	}
	if n := decisionsBy(t, w.alice, conv, p.PID, aliceFP); n != 1 {
		t.Fatalf("%d decisions signed", n)
	}

	eventually(t, "bob to resolve it active", func() bool {
		s := stateAt(t, w.bob, p.PID)
		return s.State == PartActive && s.Decision == p.Decision && s.Held == 0
	})
	var raw string
	if err := w.bob.store.db.QueryRow(`SELECT event FROM participation_events WHERE hash = ?`, p.Decision).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	ev, err := protocol.ParseParticipationEvent([]byte(raw))
	if err != nil || ev.Type != protocol.EventAccept || ev.Prev != p.Invite || ev.Author.Address != w.alice.Address || ev.Author.Fingerprint != aliceFP {
		t.Fatalf("the accept bob holds: %+v %v", ev, err)
	}
	var fields map[string]any
	json.Unmarshal([]byte(raw), &fields)
	if keys := slices.Sorted(maps.Keys(fields)); !slices.Equal(keys, []string{"author", "conv", "pid", "prev", "sig", "ts", "type", "v"}) {
		t.Fatalf("not an ordinary accept: %v", keys)
	}
	if n := decisionsBy(t, w.bob, conv, p.PID, aliceFP); n != 1 {
		t.Fatalf("bob holds %d decisions", n)
	}

	// Dismissing the notice changes nothing else; there is nothing twice.
	if err := w.alice.Resolve(p.PID); err != nil {
		t.Fatal(err)
	}
	if notices, _ = w.alice.SelfConsentNotices(); len(notices) != 0 {
		t.Fatalf("notices after dismissal: %+v", notices)
	}
	if err := w.alice.Resolve(p.PID); err == nil {
		t.Fatal("a notice dismissed twice")
	}
	if s := stateAt(t, w.alice, p.PID); s.State != PartActive {
		t.Fatalf("dismissing the notice ended the agent: %+v", s)
	}
}

// Every condition holds or the invite waits for the click: a stored invite
// differing in one point from one that is accepted is not, and an accept
// is signed once even when consent is tried concurrently.
func TestSelfConsentConditions(t *testing.T) {
	w, conv, _ := selfConsentWorld(t)
	aliceFP, bobFP := w.alice.Self().Fingerprint(), w.bob.Self().Fingerprint()
	named, err := w.alice.CreateLocalAgent("Builder", Responder{Harness: "claude", Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	retired, err := w.alice.CreateLocalAgent("Retired", Responder{Harness: "claude", Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.alice.SetLocalAgentResponder(retired.ID, nil); err != nil {
		t.Fatal(err)
	}
	consent := func(pid string) bool {
		t.Helper()
		ok, err := w.alice.selfConsent(tctx(t), pid)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	for _, c := range []struct {
		name   string
		author *Agent
		edit   func(*protocol.ParticipationEvent)
		want   bool
	}{
		{"own default agent, from its host", w.alice, nil, true},
		{"own key as the task key", w.alice, func(ev *protocol.ParticipationEvent) { ev.TaskKeys = []string{aliceFP} }, true},
		{"enabled named agent", w.alice, func(ev *protocol.ParticipationEvent) { ev.Host.AgentID = named.ID }, true},
		{"another person's invite", w.bob, nil, false},
		{"a human role", w.alice, func(ev *protocol.ParticipationEvent) { ev.Role = protocol.RoleHuman }, false},
		{"a foreign task key", w.alice, func(ev *protocol.ParticipationEvent) { ev.TaskKeys = []string{aliceFP, bobFP} }, false},
		{"a disabled named agent", w.alice, func(ev *protocol.ParticipationEvent) { ev.Host.AgentID = retired.ID }, false},
		{"an unknown named agent", w.alice, func(ev *protocol.ParticipationEvent) { ev.Host.AgentID = protocol.NewID() }, false},
	} {
		pid := storedInvite(t, w.alice, c.author, conv, c.edit)
		if s := stateAt(t, w.alice, pid); s.State != PartInvited || !s.HostHere {
			t.Fatalf("%s: not an invite waiting here: %+v", c.name, s)
		}
		want := 0
		if c.want {
			want = 1
		}
		if got := consent(pid); got != c.want || decisionsBy(t, w.alice, conv, pid, aliceFP) != want {
			t.Fatalf("%s: accepted %v, want %v", c.name, got, c.want)
		}
		if consent(pid) || decisionsBy(t, w.alice, conv, pid, aliceFP) != want {
			t.Fatalf("%s: a second try signed again", c.name)
		}
	}

	// The default agent runs only once a responder is selected.
	if err := w.alice.SetResponder(nil); err != nil {
		t.Fatal(err)
	}
	pid := storedInvite(t, w.alice, w.alice, conv, nil)
	declined := storedInvite(t, w.alice, w.alice, conv, nil)
	if consent(pid) || consent(declined) {
		t.Fatal("accepted for a default agent that does not run here")
	}
	if _, err := w.alice.DeclineParticipation(tctx(t), declined); err != nil {
		t.Fatal(err)
	}
	if err := w.alice.SetResponder(&Responder{Harness: "claude", Dir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}

	// A frozen own person (the client never freezes its own; the check
	// holds anyway) accepts nothing; once current again it does. A
	// participation decided here already, or in conflict, is left alone.
	if _, err := w.alice.store.db.Exec(`UPDATE persons SET state = ? WHERE state = ?`, personConflict, personSelf); err != nil {
		t.Fatal(err)
	}
	frozen := consent(pid)
	if _, err := w.alice.store.db.Exec(`UPDATE persons SET state = ? WHERE state = ?`, personSelf, personConflict); err != nil {
		t.Fatal(err)
	}
	if frozen || !consent(pid) {
		t.Fatalf("frozen person: accepted %v; current again: not accepted", frozen)
	}
	if consent(declined) || decisionsBy(t, w.alice, conv, declined, aliceFP) != 1 {
		t.Fatal("accepted what was declined here")
	}
	forked := storedInvite(t, w.alice, w.alice, conv, nil)
	storedInvite(t, w.alice, w.alice, conv, func(ev *protocol.ParticipationEvent) { ev.PID, ev.Note = forked, "the same id again" })
	if s := stateAt(t, w.alice, forked); s.State != PartConflict || consent(forked) {
		t.Fatalf("a conflicting participation: %+v", s)
	}

	// Concurrent tries (admission, the start sweep, an invite in another
	// process) sign one accept.
	race, ctx := storedInvite(t, w.alice, w.alice, conv, nil), tctx(t)
	var wg sync.WaitGroup
	var mu sync.Mutex
	accepted := 0
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ok, err := w.alice.selfConsent(ctx, race); err == nil && ok {
				mu.Lock()
				accepted++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if s := stateAt(t, w.alice, race); accepted != 1 || decisionsBy(t, w.alice, conv, race, aliceFP) != 1 || s.State != PartActive {
		t.Fatalf("concurrent tries: %d accepted, state %+v", accepted, s)
	}
	// A click racing them signs no second decision either: it ends as a
	// retry of the one stored.
	clicked := storedInvite(t, w.alice, w.alice, conv, nil)
	errs := make(chan error, 4)
	for i := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if i%2 == 0 {
				_, err := w.alice.AcceptParticipation(ctx, clicked)
				errs <- err
				return
			}
			_, err := w.alice.selfConsent(ctx, clicked)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if s := stateAt(t, w.alice, clicked); decisionsBy(t, w.alice, conv, clicked, aliceFP) != 1 || s.State != PartActive {
		t.Fatalf("clicks racing self-consent: state %+v", s)
	}
	// What makes that so, whatever the timing: a decision of this key signed
	// at another time, stored meanwhile, refuses the second in its
	// transaction.
	guarded := storedInvite(t, w.alice, w.alice, conv, nil)
	info := stateAt(t, w.alice, guarded)
	me, _, _ := w.alice.store.selfPerson(w.alice.Address)
	earlier := protocol.ParticipationEvent{V: 1, Conv: conv, PID: guarded, Type: protocol.EventAccept, Prev: info.Invite, TS: time.Now().Unix() - 100,
		Author: protocol.EventAuthor{Person: me.info.Person, Roster: me.info.Roster, Address: w.alice.Address, Fingerprint: aliceFP}}
	earlier.Sign(w.alice.id.Sign)
	raw, _ := json.Marshal(earlier)
	if err := w.alice.store.addParticipationEvent(earlier, raw); err != nil {
		t.Fatal(err)
	}
	if _, err := w.alice.signWith(ctx, info, protocol.EventAccept, info.Invite, undecidedHere(conv, guarded, aliceFP)); !errors.Is(err, errDecidedHere) {
		t.Fatalf("a second decision: %v", err)
	}
	if n := decisionsBy(t, w.alice, conv, guarded, aliceFP); n != 1 {
		t.Fatalf("%d decisions stored", n)
	}
}

// The trust set: a device approved with person approve (ApproveNativeLink)
// is trusted, one approved as a browser is not, and neither is a task key
// of an untrusted device of one's own. A trusted device's invite is
// accepted once on admission, also while the host's daemon was stopped;
// the start sweep accepts one the person trusted later; untrust ends it.
func TestSelfConsentLinkedDevices(t *testing.T) {
	w, conv, stopAlice := selfConsentWorld(t)
	if _, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "a DM linked devices get as history"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob to hold it", func() bool { return len(convBodies(t, w.bob, conv)) == 1 })
	desk := linkedVia(t, w.alice, "desk", w.alice.ApproveNativeLink)
	phone := linkedVia(t, w.alice, "phone", func(ctx context.Context, id string) error { return w.alice.DecideLink(ctx, id, true) })
	trust, err := w.alice.SelfConsentTrust()
	deskDev := TrustedDevice{Address: desk.Address, Fingerprint: desk.Self().Fingerprint()}
	if err != nil || !slices.Equal(trust, []TrustedDevice{{Address: w.alice.Address, Fingerprint: w.alice.Self().Fingerprint()}, deskDev}) {
		t.Fatalf("trust set: %+v %v", trust, err)
	}
	for _, d := range []*Agent{desk, phone} {
		eventually(t, d.Address+" to hold the DM", func() bool { _, _, found, _ := d.store.conversation(conv); return found })
	}
	invite := func(from *Agent, taskKeys ...string) string {
		t.Helper()
		p, err := from.InviteAgent(tctx(t), conv, w.alice.Address, nil, taskKeys, "")
		if err != nil {
			t.Fatal(err)
		}
		return p.PID
	}
	host := w.alice
	waits := func(pid string) {
		t.Helper()
		eventually(t, "the invite at the host", func() bool { return stateAt(t, host, pid).State == PartInvited })
		if ok, err := host.selfConsent(tctx(t), pid); ok || err != nil || stateAt(t, host, pid).State != PartInvited {
			t.Fatalf("invite %s accepted without a click (%v)", pid, err)
		}
	}
	joins := func(pid string) {
		t.Helper()
		eventually(t, "the own agent to join", func() bool { return stateAt(t, host, pid).State == PartActive })
		if n := decisionsBy(t, host, conv, pid, host.Self().Fingerprint()); n != 1 {
			t.Fatalf("%d decisions signed", n)
		}
	}

	fromDesk := invite(desk)
	joins(fromDesk)
	eventually(t, "bob to see it active", func() bool { return stateAt(t, w.bob, fromDesk).State == PartActive })
	fromPhone := invite(phone)
	waits(fromPhone)
	if p, err := host.InviteAgent(tctx(t), conv, host.Address, nil, []string{phone.Self().Fingerprint()}, ""); err != nil || p.State != PartInvited {
		t.Fatalf("an untrusted own device's task key: %+v %v", p, err)
	}

	// The host's daemon is stopped when the desk invites: on restart the
	// invite is admitted and accepted once; nothing accepted before is
	// signed again.
	restart := func() {
		t.Helper()
		stopAlice()
		if err := host.Close(); err != nil {
			t.Fatal(err)
		}
	}
	start := func() {
		t.Helper()
		a, err := Open(host.home)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { a.Close() })
		host, stopAlice = a, runAgent(t, a)
	}
	restart()
	whileStopped := invite(desk)
	start()
	joins(whileStopped)
	joins(fromDesk)

	// Trusted later, the phone's waiting invite is accepted by the sweep at
	// the next start; the desk, untrusted, invites for the click again.
	if err := host.TrustOwnDevice(phone.Address); err != nil {
		t.Fatal(err)
	}
	if s := stateAt(t, host, fromPhone); s.State != PartInvited {
		t.Fatalf("trusting decided by itself: %+v", s)
	}
	restart()
	start()
	joins(fromPhone)
	if err := host.UntrustOwnDevice(desk.Address); err != nil {
		t.Fatal(err)
	}
	waits(invite(desk))
	if err := host.UntrustOwnDevice(host.Address); err == nil {
		t.Fatal("the host untrusted itself")
	}
	if err := host.TrustOwnDevice(w.bob.Address); err == nil {
		t.Fatal("trusted another person's device")
	}
}

// Offline: the host's own invite is accepted at once with no relay, and
// both reach bob, once, when it is back.
func TestSelfConsentOffline(t *testing.T) {
	w, conv, _ := selfConsentWorld(t)
	w.hub.Stop()
	p, err := w.alice.InviteAgent(tctx(t), conv, w.alice.Address, nil, nil, "")
	if err != nil || p.State != PartActive || decisionsBy(t, w.alice, conv, p.PID, w.alice.Self().Fingerprint()) != 1 {
		t.Fatalf("offline invite of the own agent: %+v %v", p, err)
	}
	var kept, handed int
	w.alice.store.db.QueryRow(`SELECT count(*), count(*) FILTER (WHERE state IN (?, ?)) FROM outbox WHERE pid = ? AND recipient = ?`,
		protocol.StateCustody, protocol.StateDelivered, p.PID, w.bob.Address).Scan(&kept, &handed)
	if kept != 2 || handed != 0 {
		t.Fatalf("bob's copies of the invite and accept: %d kept, %d handed over with no relay", kept, handed)
	}
	w.hub = testhub.Start(t, w.hub.Dir, w.hub.Addr, "")
	eventually(t, "bob to see it active", func() bool {
		s := stateAt(t, w.bob, p.PID)
		return s.State == PartActive && s.Decision == p.Decision
	})
	if n := decisionsBy(t, w.bob, conv, p.PID, w.alice.Self().Fingerprint()); n != 1 {
		t.Fatalf("bob holds %d decisions", n)
	}
}

// In a group the own agent joins the same way, with the invite's epoch
// verified; another member's invite of it waits for its owner's accept.
func TestSelfConsentGroup(t *testing.T) {
	w, carol, packet, _ := groupTurnsFixture(t)
	conv := packet.State.Conv
	named, err := w.alice.CreateLocalAgent("Own builder", Responder{Harness: "claude", Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.alice.PublishAgentCatalog(tctx(t)); err != nil {
		t.Fatal(err)
	}
	p, err := w.alice.InviteNamedAgent(tctx(t), conv, w.alice.Address, named.ID, nil, nil, "")
	if err != nil || p.State != PartActive || decisionsBy(t, w.alice, conv, p.PID, w.alice.Self().Fingerprint()) != 1 {
		t.Fatalf("own agent in a group: %+v %v", p, err)
	}
	for _, a := range []*Agent{w.bob, carol} {
		eventually(t, a.Address+" to see it active", func() bool { return stateAt(t, a, p.PID).Claimable() })
	}
	q, err := w.bob.InviteNamedAgent(tctx(t), conv, w.alice.Address, named.ID, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob's invite at alice", func() bool { return stateAt(t, w.alice, q.PID).State == PartInvited })
	if ok, err := w.alice.selfConsent(tctx(t), q.PID); ok || err != nil {
		t.Fatalf("another member's invite accepted without a click (%v)", err)
	}
}
