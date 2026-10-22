package client

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
)

// Each person's ordinarily linked phone can task their own agent directly and
// in a group, without any grant or TaskKeys. Revoking an optional person grant
// does not change that identity-based rule. Native task arguments stay intact.
func TestOwnPersonTasksDirectAndGroup(t *testing.T) {
	st := installStub(t, "answer")
	w, _, group, stops := groupTurnsFixture(t)
	for _, initial := range []*Agent{w.alice, w.bob} {
		host := initial
		setResponder(t, host, "stub", st.dir, time.Minute)
		phone := linkedVia(t, host, "phone", approveLink(host))
		publishGroupFixtureCaps(t, phone, true)
		func() {
			current := false
			defer func() {
				if current {
					return
				}
				p, err := phone.GroupContext(group.State.Conv)
				t.Logf("phone context snapshot host=%s want_seq=%d want_hash=%s got_seq=%d got_hash=%s error=%v", host.Address, group.State.Seq, group.State.Hash(), p.State.Seq, p.State.Hash(), err)
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				for side, a := range map[string]*Agent{"host": host, "phone": phone} {
					var copies, quarantine, archives, history string
					err := a.store.db.QueryRowContext(ctx, `SELECT
 (SELECT json_group_array(json_object('sub',sub,'state',state,'count',n)) FROM
  (SELECT coalesce(sub,'') sub,state,count(*) n FROM outbox WHERE recipient=? AND conv=? GROUP BY sub,state)),
 (SELECT json_group_array(json_object('reason',reason,'count',n)) FROM
  (SELECT reason,count(*) n FROM quarantine GROUP BY reason)),
 (SELECT json_group_array(json_object('retained',retained,'done',done,'error_present',error<>'','position',pos)) FROM history_archive_jobs),
 (SELECT json_group_array(json_object('state',state,'updated_at',updated_at)) FROM history_jobs WHERE device=?)`, phone.Address, group.State.Conv, phone.Address).Scan(&copies, &quarantine, &archives, &history)
					t.Logf("phone context snapshot side=%s copies=%s quarantine=%s archives=%s history=%s upkeep_bits=%d reader_wait=%t error=%v", side, copies, quarantine, archives, history, a.convWork.bits.Load(), a.convWork.readerWait.Load(), err)
				}
			}()
			eventually(t, "phone current group", func() bool {
				p, e := phone.GroupContext(group.State.Conv)
				return e == nil && p.State.Hash() == group.State.Hash()
			})
			current = true
		}()
		part, err := host.InviteAgent(tctx(t), group.State.Conv, host.Address, nil, nil, "own tasks without task keys")
		if err != nil {
			t.Fatal(err)
		}
		eventually(t, "phone active agent", func() bool {
			p, e := phone.Participation(part.PID)
			return e == nil && p.Claimable() && len(p.TaskKeys) == 0
		})
		send := func() {
			t.Helper()
			direct, e := phone.SendMessage(tctx(t), Outgoing{To: host.Address, Kind: envelope.KindTask, Body: "own direct task"})
			if e != nil {
				t.Fatal(e)
			}
			waitState(t, host, direct.ID, stateAnswered)
			shared, e := phone.AskAgent(tctx(t), part.PID, envelope.KindTask, "own group task")
			if e != nil {
				t.Fatal(e)
			}
			waitState(t, host, shared.ID, stateAnswered)
		}
		send()
		person, _, err := host.Person()
		if err != nil {
			t.Fatal(err)
		}
		if _, err = host.GrantTasks(person.Person); err != nil {
			t.Fatal(err)
		}
		if _, err = host.RevokeTasks(person.Person); err != nil {
			t.Fatal(err)
		}
		send()
		host = reopen(t, host, stops[initial])
		t.Cleanup(func() { host.Close() })
		send()
		if gs, e := phone.PersonGrants(); e != nil || len(gs) != 0 {
			t.Fatalf("grant copied to phone: %v %v", gs, e)
		}
	}
	if st.count() != 12 {
		t.Fatalf("executions including restart: got %d want 12", st.count())
	}
	log, err := os.ReadFile(st.log)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(log), "args=--task-mode") != 12 || strings.Contains(string(log), "--question-mode") {
		t.Fatal("native task arguments changed")
	}
}

// Current membership and exact key fences govern all own human devices,
// independently of optional person grants and legacy native trust.
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
	if got := jobState(t, w.alice, old); got != statePending {
		t.Fatalf("revoked pending task: %s", got)
	}
	ownPendingTask(t, w.alice, desk, "separate native consent")
	checkNative := func(want string) {
		t.Helper()
		holds, e := ownDeviceHolds(w.alice.store.db, desk.Address, desk.Self().Fingerprint())
		if e != nil || holds != (want == "active") {
			t.Fatalf("current own authority %v %v want %s", holds, e, want)
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
