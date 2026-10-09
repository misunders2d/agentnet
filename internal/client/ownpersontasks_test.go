package client

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
)

// A phone's ordinary verified-person grant reaches both existing task paths.
// It never becomes native self-invite consent, changes the harness policy, or
// releases already waiting tasks. The receiving host alone keeps the grant.
func TestOwnPersonTasksDirectAndGroup(t *testing.T) {
	st := installStub(t, "answer")
	w, _, group, stops := groupTurnsFixture(t)
	host := w.alice
	setResponder(t, host, "stub", st.dir, time.Minute)
	phone := linkedVia(t, host, "phone", approveLink(host))
	publishGroupFixtureCaps(t, phone, true)
	eventually(t, "phone current group", func() bool {
		p, e := phone.GroupContext(group.State.Conv)
		return e == nil && p.State.Hash() == group.State.Hash()
	})
	part, err := host.InviteAgent(tctx(t), group.State.Conv, host.Address, nil, nil, "own tasks without task keys")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "phone active agent", func() bool {
		p, e := phone.Participation(part.PID)
		return e == nil && p.Claimable() && len(p.TaskKeys) == 0
	})
	person, _, err := host.Person()
	if err != nil {
		t.Fatal(err)
	}
	send := func(groupTask bool, want string) string {
		t.Helper()
		var id string
		if groupTask {
			r, e := phone.AskAgent(tctx(t), part.PID, envelope.KindTask, "own group task")
			if e != nil {
				t.Fatal(e)
			}
			id = r.ID
		} else {
			r, e := phone.SendMessage(tctx(t), Outgoing{To: host.Address, Kind: envelope.KindTask, Body: "own direct task"})
			if e != nil {
				t.Fatal(e)
			}
			id = r.ID
		}
		waitState(t, host, id, want)
		return id
	}
	waitingDirect, waitingGroup := send(false, stateAwaiting), send(true, stateAwaiting)
	if _, err = host.GrantTasks(person.Person); err != nil {
		t.Fatal(err)
	}
	if native, err := host.NativeTaskPermissions(); err != nil || len(native) != 0 {
		t.Fatalf("person grant became native consent: %v %v", native, err)
	}
	for range 2 {
		send(false, stateAnswered)
		send(true, stateAnswered)
	}
	for _, id := range []string{waitingDirect, waitingGroup} {
		if got := jobState(t, host, id); got != stateAwaiting {
			t.Fatalf("grant released old task: %s", got)
		}
	}
	if gs, err := phone.PersonGrants(); err != nil || len(gs) != 0 {
		t.Fatalf("grant applied on phone: %v %v", gs, err)
	}
	t.Cleanup(func() { host.Close() })
	host = reopen(t, host, stops[host])
	send(false, stateAnswered)
	send(true, stateAnswered)
	if _, err = host.RevokeTasks(person.Person); err != nil {
		t.Fatal(err)
	}
	send(false, stateAwaiting)
	send(true, stateAwaiting)
	if st.count() != 6 {
		t.Fatalf("executions including restart: got %d want 6", st.count())
	}
	log, err := os.ReadFile(st.log)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(log), "args=--task-mode") != 6 || strings.Contains(string(log), "--question-mode") {
		t.Fatal("tasks did not retain the harness's native task arguments")
	}
}

// Separate native consent remains visible and effective when the person grant
// is off. Current membership/key/frozen fences still decide effective status.
func TestOwnPersonTasksNativeConsentAndFences(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	persons(t, w.alice)
	desk := linkedVia(t, w.alice, "desk", w.alice.ApproveNativeLink)
	phone := linkedVia(t, w.alice, "phone", approveLink(w.alice))
	person, _, err := w.alice.Person()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.alice.GrantTasks(person.Person); err != nil {
		t.Fatal(err)
	}
	old := ownPendingTask(t, w.alice, phone, "waiting for configured harness")
	if _, err = w.alice.RevokeTasks(person.Person); err != nil {
		t.Fatal(err)
	}
	if got := jobState(t, w.alice, old); got != stateAwaiting {
		t.Fatalf("revoked pending task: %s", got)
	}
	ownPendingTask(t, w.alice, desk, "separate native consent")
	checkNative := func(want string) {
		t.Helper()
		gs, e := w.alice.NativeTaskPermissions()
		if e != nil || len(gs) != 1 || gs[0].Address != desk.Address || gs[0].Fingerprint != desk.Self().Fingerprint() || gs[0].Status != want {
			t.Fatalf("native consent: %+v %v", gs, e)
		}
	}
	checkNative("active")
	if _, err = w.alice.GrantTasks(person.Person); err != nil {
		t.Fatal(err)
	}
	next, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if err = w.alice.store.setPending(next.Public(desk.Address)); err != nil {
		t.Fatal(err)
	}
	checkNative("inactive: current own-device trust does not hold")
	if ok, err := taskGranted(w.alice.store.db, desk.Address, desk.Self().Fingerprint()); err != nil || ok {
		t.Fatalf("pending key still granted: %v %v", ok, err)
	}
	if err = w.alice.store.pin(next.Public(desk.Address)); err != nil {
		t.Fatal(err)
	}
	checkNative("inactive: current own-device trust does not hold")
	if err = w.alice.RemoveDevice(tctx(t), phone.Address); err != nil {
		t.Fatal(err)
	}
	if ok, err := taskGranted(w.alice.store.db, phone.Address, phone.Self().Fingerprint()); err != nil || ok {
		t.Fatalf("removed phone still granted: %v %v", ok, err)
	}
}
