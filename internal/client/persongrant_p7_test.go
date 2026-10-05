package client

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func p7PinStep(t *testing.T, at *Agent, r protocol.PersonRoster) {
	t.Helper()
	raw, _ := json.Marshal(r)
	if _, e := at.store.pinChain(r.Person, [][]byte{raw}, at.Self(), false); e != nil {
		t.Fatal(e)
	}
}
func p7Person(t *testing.T) (*world, protocol.PersonRoster) {
	t.Helper()
	w := newWorld(t, "")
	persons(t, w.alice, w.bob)
	p, _, e := w.alice.store.selfPerson(w.alice.Address)
	if e != nil {
		t.Fatal(e)
	}
	if e = w.bob.store.pin(w.alice.Self()); e != nil {
		t.Fatal(e)
	}
	p7PinStep(t, w.bob, p.roster)
	return w, p.roster
}
func p7Added(t *testing.T, w *world, prev protocol.PersonRoster, address string, human bool) (protocol.PersonRoster, *identity.Identity) {
	t.Helper()
	id, e := identity.Generate()
	if e != nil {
		t.Fatal(e)
	}
	d := id.Public(address)
	r := protocol.PersonRoster{Person: prev.Person, Label: prev.Label, Seq: prev.Seq + 1, Prev: prev.Hash(), Devices: append(append([]identity.Public{}, prev.Devices...), d), HumanKeys: prev.Humans(), By: w.alice.Self().Fingerprint()}
	if human {
		r.HumanKeys = append(r.HumanKeys, d.Fingerprint())
	}
	r.Join = ed25519.Sign(id.Sign, protocol.JoinBytes(r.Person, r.Seq, r.Prev, d))
	r.Sign(w.alice.id.Sign)
	if _, e = r.VerifyNext(prev); e != nil {
		t.Fatal(e)
	}
	p7PinStep(t, w.bob, r)
	if e = w.bob.store.pin(d); e != nil {
		t.Fatal(e)
	}
	return r, id
}
func p7Stored(t *testing.T, at *Agent, from, fp, kind, want string) string {
	t.Helper()
	in := taskFrom(from, at.Address)
	in.Kind = kind
	if e := at.store.addInbox(in, fp); e != nil {
		t.Fatal(e)
	}
	if got, _ := at.store.jobState(in.ID); got != want {
		t.Fatalf("%s from %s: got %s want %s", kind, from, got, want)
	}
	return in.ID
}

func TestP7PersonGrantsCurrentFutureAndRemoval(t *testing.T) {
	w, r := p7Person(t)
	p7Stored(t, w.bob, w.alice.Address, w.alice.Self().Fingerprint(), envelope.KindQuestion, stateHeld)
	if e := w.bob.Approve(r.Person); e != nil {
		t.Fatal(e)
	}
	if _, e := w.bob.GrantTasks(r.Person); e != nil {
		t.Fatal(e)
	}
	p7Stored(t, w.bob, w.alice.Address, w.alice.Self().Fingerprint(), envelope.KindQuestion, statePending)
	r, phone := p7Added(t, w, r, "alice/phone", true)
	fp := phone.Public("alice/phone").Fingerprint()
	q := p7Stored(t, w.bob, "alice/phone", fp, envelope.KindQuestion, statePending)
	task := p7Stored(t, w.bob, "alice/phone", fp, envelope.KindTask, statePending)
	// Authority remains one local person decision; roster updates copy no grants.
	gs, e := w.bob.PersonGrants()
	if e != nil || len(gs) != 1 || !gs[0].Questions || !gs[0].Tasks {
		t.Fatalf("grants %v %v", gs, e)
	}
	r2 := protocol.PersonRoster{Person: r.Person, Label: r.Label, Seq: r.Seq + 1, Prev: r.Hash(), Devices: []identity.Public{w.alice.Self()}, HumanKeys: []string{w.alice.Self().Fingerprint()}, By: w.alice.Self().Fingerprint()}
	r2.Sign(w.alice.id.Sign)
	p7PinStep(t, w.bob, r2)
	if s, _ := w.bob.store.jobState(q); s != stateHeld {
		t.Fatalf("removed question %s", s)
	}
	if s, _ := w.bob.store.jobState(task); s != stateAwaiting {
		t.Fatalf("removed task %s", s)
	}
	p7Stored(t, w.bob, "alice/phone", fp, envelope.KindTask, stateAwaiting)
	if e = w.bob.Unapprove(r.Person); e != nil {
		t.Fatal(e)
	}
	if _, e = w.bob.RevokeTasks(r.Person); e != nil {
		t.Fatal(e)
	}
	p7Stored(t, w.bob, w.alice.Address, w.alice.Self().Fingerprint(), envelope.KindQuestion, stateHeld)
	p7Stored(t, w.bob, w.alice.Address, w.alice.Self().Fingerprint(), envelope.KindTask, stateAwaiting)
}

func TestP7KeysConflictsAndClaimFence(t *testing.T) {
	t.Run("changed key", func(t *testing.T) {
		w, r := p7Person(t)
		w.bob.Approve(r.Person)
		w.bob.GrantTasks(r.Person)
		old := p7Stored(t, w.bob, w.alice.Address, w.alice.Self().Fingerprint(), envelope.KindQuestion, statePending)
		next, _ := identity.Generate()
		pub := next.Public(w.alice.Address)
		if e := w.bob.store.setPending(pub); e != nil {
			t.Fatal(e)
		}
		if s, _ := w.bob.store.jobState(old); s != stateHeld {
			t.Fatalf("pending change %s", s)
		}
		p7Stored(t, w.bob, w.alice.Address, pub.Fingerprint(), envelope.KindTask, stateAwaiting)
		if e := w.bob.store.pin(pub); e != nil {
			t.Fatal(e)
		}
		p7Stored(t, w.bob, w.alice.Address, pub.Fingerprint(), envelope.KindQuestion, stateHeld) // trust alone cannot change roster
	})
	t.Run("person conflict", func(t *testing.T) {
		w, r := p7Person(t)
		w.bob.Approve(r.Person)
		w.bob.GrantTasks(r.Person)
		// Advanced device decisions cannot bypass a frozen verified person.
		w.bob.Approve(w.alice.Address)
		w.bob.GrantTasks(w.alice.Address)
		q := p7Stored(t, w.bob, w.alice.Address, w.alice.Self().Fingerprint(), envelope.KindQuestion, statePending)
		task := p7Stored(t, w.bob, w.alice.Address, w.alice.Self().Fingerprint(), envelope.KindTask, statePending)
		freeze(t, w.bob, w.alice)
		gs, e := w.bob.TaskGrants()
		if e != nil || len(gs) != 1 || gs[0].Status != "inactive: person frozen" {
			t.Fatalf("frozen advanced grant view: %+v %v", gs, e)
		}
		if s, _ := w.bob.store.jobState(q); s != stateHeld {
			t.Fatalf("frozen question %s", s)
		}
		if _, _, e := w.bob.AcceptAlways(task); !errors.Is(e, errPersonConflict) {
			t.Fatalf("frozen always %v", e)
		}
		if e := w.bob.Unapprove(r.Person); e != nil {
			t.Fatal(e)
		}
		if _, e := w.bob.RevokeTasks(r.Person); e != nil {
			t.Fatal(e)
		}
	})
	t.Run("claim rechecks current roster", func(t *testing.T) {
		w, r := p7Person(t)
		w.bob.GrantTasks(r.Person)
		id := p7Stored(t, w.bob, w.alice.Address, w.alice.Self().Fingerprint(), envelope.KindTask, statePending)
		// Simulate a removal landing between receipt and claim; the SQL gate must
		// defend even before the normal source-transition demotion runs.
		if _, e := w.bob.store.db.Exec(`DELETE FROM person_devices WHERE address=?`, w.alice.Address); e != nil {
			t.Fatal(e)
		}
		if j, ok, e := w.bob.store.claimJob("stub"); e != nil || ok {
			t.Fatalf("claim removed %v %v %v", j, ok, e)
		}
		if s, _ := w.bob.store.jobState(id); s == stateRunning {
			t.Fatal("removed key ran")
		}
	})
}

func TestP7AcceptAlwaysPersonAndExplicitDevice(t *testing.T) {
	w, r := p7Person(t)
	st := installStub(t, "answer")
	setResponder(t, w.bob, "stub", st.dir, 0)
	task := p7Stored(t, w.bob, w.alice.Address, w.alice.Self().Fingerprint(), envelope.KindTask, stateAwaiting)
	who, fp, e := w.bob.AcceptAlways(task)
	if e != nil || who != r.Person || fp != w.alice.Self().Fingerprint() {
		t.Fatalf("always %s %s %v", who, fp, e)
	}
	_, phone := p7Added(t, w, r, "alice/phone", false)
	p7Stored(t, w.bob, "alice/phone", phone.Public("alice/phone").Fingerprint(), envelope.KindTask, statePending)
	// Separate advanced device decisions remain local and narrow.
	w.bob.RevokeTasks(r.Person)
	if _, e = w.bob.GrantTasks(w.alice.Address); e != nil {
		t.Fatal(e)
	}
	p7Stored(t, w.bob, w.alice.Address, w.alice.Self().Fingerprint(), envelope.KindTask, statePending)
	p7Stored(t, w.bob, "alice/phone", phone.Public("alice/phone").Fingerprint(), envelope.KindTask, stateAwaiting)
}

func TestP7GrantNamesAndRestart(t *testing.T) {
	w, r := p7Person(t)
	// A name is the person's own claim: even a unique one picks no one, and
	// the refusal names the IDs to choose from.
	if e := w.bob.Approve(r.Label); e == nil || !strings.Contains(e.Error(), r.Person) {
		t.Fatalf("a unique name picked a grant target: %v", e)
	}
	if _, e := w.bob.GrantTasks(r.Label); e == nil {
		t.Fatal("a unique name picked a task grant target")
	}
	w.bob.Approve(r.Person)
	w.bob.GrantTasks(r.Person)
	other, _ := identity.Generate()
	r2 := protocol.PersonRoster{Person: protocol.NewID(), Label: r.Label, Devices: []identity.Public{other.Public("other/desk")}}
	r2.Sign(other.Sign)
	p7PinStep(t, w.bob, r2)
	if e := w.bob.Approve(r.Label); e == nil {
		t.Fatal("ambiguous name approved")
	}
	if e := w.bob.Approve(r.Person); e != nil {
		t.Fatal(e)
	}
	home := w.bob.home
	w.bob.Close()
	reopened, e := Open(home)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	gs, e := reopened.PersonGrants()
	if e != nil || len(gs) != 1 {
		t.Fatalf("restart grants %v %v", gs, e)
	}
	p7Stored(t, reopened, w.alice.Address, w.alice.Self().Fingerprint(), envelope.KindTask, statePending)
}

func TestP7HumanDeviceAndAgentHostLink(t *testing.T) {
	w := newWorld(t, "")
	persons(t, w.alice, w.bob)
	runAgent(t, w.alice)
	host, awaited, _ := linkPhone(t, w.alice, "worker")
	req := pendingLink(t, w.alice)
	if e := w.alice.ApproveAgentLink(tctx(t), req.ID); e != nil {
		t.Fatal(e)
	}
	out := <-awaited
	if out.err != nil || out.s.State != LinkLinked {
		t.Fatalf("host link %+v", out)
	}
	p, _, _ := host.store.selfPerson(host.Address)
	if p.roster.Human(host.Self().Fingerprint()) {
		t.Fatal("host received human authority")
	}
	if _, e := host.NewDeviceLink(tctx(t)); e == nil {
		t.Fatal("agent host made a link")
	}
	// A regular human phone receives enrollment authority through the human's
	// signed local choice, independent of whether it advertises agent1 later.
	phone, waitPhone, _ := linkPhone(t, w.alice, "phone")
	req = pendingLink(t, w.alice)
	if e := w.alice.DecideLink(tctx(t), req.ID, true); e != nil {
		t.Fatal(e)
	}
	out = <-waitPhone
	if out.err != nil {
		t.Fatal(out.err)
	}
	p, _, _ = phone.store.selfPerson(phone.Address)
	if !p.roster.Human(phone.Self().Fingerprint()) {
		t.Fatal("human phone lacks authority")
	}
	if _, e := phone.NewDeviceLink(tctx(t)); e != nil {
		t.Fatal(e)
	}
}

func TestP7FutureDeviceRealDelivery(t *testing.T) {
	w, r := p7Person(t)
	st := installStub(t, "P7 actual isolated reply")
	setResponder(t, w.bob, "stub", st.dir, 0)
	if e := w.bob.Approve(r.Person); e != nil {
		t.Fatal(e)
	}
	if _, e := w.bob.GrantTasks(r.Person); e != nil {
		t.Fatal(e)
	}
	runAgent(t, w.alice)
	runWith(t, w, w.bob, RunOptions{})
	phone, waitPhone, _ := linkPhone(t, w.alice, "new-phone")
	req := pendingLink(t, w.alice)
	if e := w.alice.DecideLink(tctx(t), req.ID, true); e != nil {
		t.Fatal(e)
	}
	if out := <-waitPhone; out.err != nil {
		t.Fatal(out.err)
	}
	runAgent(t, phone)
	for _, kind := range []string{envelope.KindQuestion, envelope.KindTask} {
		sent, e := phone.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: kind, Body: "P7 new phone"})
		if e != nil {
			t.Fatal(e)
		}
		waitState(t, w.bob, sent.ID, stateAnswered)
	}
	if st.count() != 2 {
		t.Fatalf("actual runs %d", st.count())
	}
}

func TestP7RunningPersonOriginEndsAtRemoval(t *testing.T) {
	w, r := p7Person(t)
	w.bob.GrantTasks(r.Person)
	id := p7Stored(t, w.bob, w.alice.Address, w.alice.Self().Fingerprint(), envelope.KindTask, statePending)
	j, ok, e := w.bob.store.claimJob("stub")
	if e != nil || !ok || j.ID != id || j.PermissionPerson != r.Person {
		t.Fatalf("claim %+v %v %v", j, ok, e)
	}
	if why := w.bob.personGrantStop(j); why != "" {
		t.Fatal(why)
	}
	if _, e = w.bob.store.db.Exec(`DELETE FROM person_devices WHERE address=?`, w.alice.Address); e != nil {
		t.Fatal(e)
	}
	if why := w.bob.personGrantStop(j); why == "" {
		t.Fatal("removed running origin allowed")
	}
}

func TestP7IndependentPersonAndDeviceGrants(t *testing.T) {
	w, r := p7Person(t)
	if _, e := w.bob.GrantTasks(r.Person); e != nil {
		t.Fatal(e)
	}
	if _, e := w.bob.GrantTasks(w.alice.Address); e != nil {
		t.Fatal(e)
	}
	id := p7Stored(t, w.bob, w.alice.Address, w.alice.Self().Fingerprint(), envelope.KindTask, statePending)
	if _, e := w.bob.RevokeTasks(w.alice.Address); e != nil {
		t.Fatal(e)
	}
	if s, _ := w.bob.store.jobState(id); s != statePending {
		t.Fatalf("separate person grant lost: %s", s)
	}
	if _, e := w.bob.RevokeTasks(r.Person); e != nil {
		t.Fatal(e)
	}
	if s, _ := w.bob.store.jobState(id); s != stateAwaiting {
		t.Fatalf("last grant not revoked: %s", s)
	}
}

func TestP7RunningHarnessStopsOnDeviceRemoval(t *testing.T) {
	w, r := p7Person(t)
	r, _ = p7Added(t, w, r, "alice/phone", true)
	st := installStub(t, "sleep")
	setResponder(t, w.bob, "stub", st.dir, time.Minute)
	if _, e := w.bob.GrantTasks(r.Person); e != nil {
		t.Fatal(e)
	}
	id := p7Stored(t, w.bob, w.alice.Address, w.alice.Self().Fingerprint(), envelope.KindTask, statePending)
	runWith(t, w, w.bob, RunOptions{})
	waitState(t, w.bob, id, stateRunning)
	eventually(t, "P7 actual harness started", func() bool { _, e := os.Stat(st.log + ".child"); return e == nil })
	removed := protocol.PersonRoster{Person: r.Person, Label: r.Label, Seq: r.Seq + 1, Prev: r.Hash(), Devices: []identity.Public{r.Devices[1]}, HumanKeys: []string{r.Devices[1].Fingerprint()}, By: w.alice.Self().Fingerprint()}
	removed.Sign(w.alice.id.Sign)
	p7PinStep(t, w.bob, removed)
	waitState(t, w.bob, id, stateNotDelivered)
	if st.count() != 1 {
		t.Fatalf("harness ran %d times", st.count())
	}
	if _, ok := findReply(w.alice, id); ok {
		t.Fatal("removed device received an answer")
	}
}
