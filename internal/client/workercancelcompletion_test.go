package client

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/lockfile"
)

// A room wait can notice the durable Stop before its worker consumes the
// asynchronous wake and exit with NEEDS-HUMAN. Delay the watcher's review
// notification to reproduce that ordering without a model or a polling daemon.
func TestWorkerCancelWinsCompletedNeedsHumanOutput(t *testing.T) {
	st := installStub(t, "answer")
	script := `#!/bin/sh
cat >/dev/null
touch "$STUB_LOG.started"
while [ ! -f "$STUB_LOG.release" ]; do sleep 0.01; done
printf 'AGENTNET: NEEDS-HUMAN\noriginating run stopped\n'
touch "$STUB_LOG.exited"
`
	if err := os.WriteFile(Harnesses["stub"].bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	w := newWorld(t, "")
	if err := w.bob.Approve(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	agent, err := w.bob.CreateLocalAgent("cancel race", Responder{Harness: "stub", Dir: st.dir, Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	request := namedExecutorRequest(t, w, agent.ID, envelope.KindQuestion, "")
	review := namedExecutorRequest(t, w, agent.ID, envelope.KindTask, "")
	if _, err = w.bob.store.db.Exec(`UPDATE inbox SET state=? WHERE id=?`, stateNeedHuman, review); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	w.bob.notify = func(string, string, []string, func()) error {
		close(entered)
		<-release
		return nil
	}
	// Cancel's normal local daemon-presence check remains in force. There is
	// deliberately no kick listener: the persisted state must win even when
	// the asynchronous notification has not reached the watcher yet.
	unlock, err := lockfile.Acquire(filepath.Join(w.bob.home, "daemon.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	done := make(chan struct{})
	claimed := false
	go func() { claimed = w.bob.runNext(ctx, nil); close(done) }()
	defer func() {
		stop()
		unblock()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("worker cleanup did not finish")
		}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not enter the delayed review notification")
	}
	eventually(t, "harness started", func() bool { _, e := os.Stat(st.log + ".started"); return e == nil })
	if err = w.bob.Cancel(request); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(st.log+".release", nil, 0600); err != nil {
		t.Fatal(err)
	}
	eventually(t, "harness exited", func() bool { _, e := os.Stat(st.log + ".exited"); return e == nil })
	// Keep review blocked after the harness exits, so the worker reaches its
	// completion fence before that watcher can observe the cancellation.
	time.Sleep(100 * time.Millisecond)
	unblock()
	select {
	case <-done:
		if !claimed {
			t.Fatal("worker did not claim the request")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not finish after review was released")
	}
	if got := jobState(t, w.bob, request); got != stateCancelled {
		t.Fatalf("Stop lost to completed harness output: state %s, want %s", got, stateCancelled)
	}
	if _, ok := findReply(w.alice, request); ok {
		t.Fatal("cancelled request emitted a reply")
	}
}

// Stop may also arrive after the worker's last state read and before its
// NEEDS-HUMAN output is committed. The commit, reports and review queue must
// all retain the stored cancellation; daemon interruption stays uncertain.
func TestWorkerCancelAtNeedsHumanCommit(t *testing.T) {
	w := newWorld(t, "")
	for _, tc := range []struct{ initial, finish, want string }{
		{stateCancelReq, stateNeedHuman, stateCancelled},
		{stateRunning, stateNeedHuman, stateNeedHuman},
		{stateCancelReq, stateInterrupt, stateInterrupt},
	} {
		t.Run(tc.initial+"_"+tc.finish, func(t *testing.T) {
			request := namedExecutorRequest(t, w, "", envelope.KindQuestion, "")
			if _, err := w.bob.store.db.Exec(`UPDATE inbox SET state=? WHERE id=?`, tc.initial, request); err != nil {
				t.Fatal(err)
			}
			if err := w.bob.endJob(request, tc.finish, "originating run stopped"); err != nil {
				t.Fatal(err)
			}
			got := jobState(t, w.bob, request)
			if got != tc.want {
				t.Fatalf("committed state %s, want %s", got, tc.want)
			}
			var detail string
			var statusDue int
			if err := w.bob.store.db.QueryRow(`SELECT coalesce(detail,''),status_due FROM inbox WHERE id=?`, request).Scan(&detail, &statusDue); err != nil || statusDue == 0 {
				t.Fatalf("committed status was not scheduled: %d %v", statusDue, err)
			}
			if tc.want == stateCancelled && detail != "cancelled by the recipient" {
				t.Fatalf("cancelled commit retained a decision detail: %q", detail)
			}
			public, _, ok := statusOf(got)
			wantPublic, _, _ := statusOf(tc.want)
			if !ok || public != wantPublic {
				t.Fatalf("committed status %s, want %s", public, wantPublic)
			}
			ids, _, err := w.bob.store.unnotified()
			if err != nil || tc.want == stateCancelled && slices.Contains(ids, request) {
				t.Fatalf("cancelled request entered decision notifications: %v %v", ids, err)
			}
		})
	}
}
