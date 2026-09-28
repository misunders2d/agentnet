package client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// fakeProgram writes a stand-in for the daemon's program file that reports
// version v, as `agentnet version` does.
func fakeProgram(t *testing.T, v string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("switching a running daemon is not implemented on Windows yet")
	}
	p := filepath.Join(t.TempDir(), "agentnet")
	os.WriteFile(p, []byte(fmt.Sprintf("#!/bin/sh\necho 'agentnet %s (protocol %d)'\n", v, protocol.ProtocolVersion)), 0o700)
	return p
}

// runUntilStop runs a's daemon; res receives Run's result and stop ends it.
func runUntilStop(t *testing.T, a *Agent, opts RunOptions) (res <-chan error, stop func()) {
	t.Helper()
	a.Logf = t.Logf
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan error, 1)
	finished := make(chan struct{})
	go func() { out <- a.Run(ctx, opts); close(finished) }()
	t.Cleanup(func() { cancel(); <-finished })
	return out, cancel
}

func setVersion(t *testing.T, v string) {
	old := protocol.Version
	protocol.Version = v
	t.Cleanup(func() { protocol.Version = old })
}

func activation(t *testing.T, a *Agent) UpdateActivation {
	t.Helper()
	act, ok, err := ReadUpdateActivation(a.home)
	if err != nil || !ok {
		t.Fatalf("no activation recorded (%v)", err)
	}
	return act
}

// A requested switch starts no new job, waits until the running job has
// finished and stored its result, then stops the daemon for the new program.
func TestUpdateSwitchWaitsForTheRunningJob(t *testing.T) {
	st := installStub(t, "slow")
	setVersion(t, "v9.9.8")
	w := newWorld(t, "")
	setResponder(t, w.bob, "stub", st.dir, time.Minute)
	exe := fakeProgram(t, "v9.9.9")
	res, _ := runUntilStop(t, w.bob, RunOptions{Executable: exe})
	runWith(t, w, w.alice, RunOptions{})
	first, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "first", Kind: envelope.KindTask})
	if err != nil {
		t.Fatal(err)
	}
	second, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "second", Kind: envelope.KindTask})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, second.ID, stateAwaiting)
	if err := w.bob.Accept(first.ID); err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, first.ID, stateRunning)
	if err := RequestUpdateSwitch(w.bobHome, UpdateRequest{ID: "u1", Exe: exe, From: "v9.9.8", To: "v9.9.9"}); err != nil {
		t.Fatal(err)
	}
	if err := w.bob.Accept(second.ID); err != nil { // accepted while the switch is pending
		t.Fatal(err)
	}
	var stopped error
	select {
	case stopped = <-res:
	case <-time.After(20 * time.Second):
		t.Fatal("the daemon did not stop for the update")
	}
	var rs *RestartForUpdate
	if !errors.As(stopped, &rs) || rs.Request.ID != "u1" {
		t.Fatalf("Run returned %v", stopped)
	}
	if s, _ := w.bob.store.jobState(first.ID); s != stateAnswered {
		t.Fatalf("the running job ended %q, not answered", s)
	}
	if !resultStored(t, w.bob, first.ID) {
		t.Fatal("the result was not stored before the switch")
	}
	if s, _ := w.bob.store.jobState(second.ID); s != stateAccepted {
		t.Fatalf("a job started while the switch was pending: %q", s)
	}
	if st.count() != 1 {
		t.Fatalf("harness ran %d times", st.count())
	}
}

func resultStored(t *testing.T, a *Agent, replyTo string) bool {
	var n int
	a.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE reply_to = ?`, replyTo).Scan(&n)
	return n == 1
}

// A request for another program file, or one the file does not match,
// changes nothing: the daemon keeps running and jobs keep starting.
func TestUpdateSwitchRefusedKeepsServing(t *testing.T) {
	st := installStub(t, "answer")
	setVersion(t, "v9.9.8")
	w := newWorld(t, "")
	setResponder(t, w.bob, "stub", st.dir, time.Minute)
	exe := fakeProgram(t, "v9.9.7")                              // the file does not hold what the request says
	cannot := func() (bool, string) { return false, "not here" } // only for the v9.9.6 request below
	res, _ := runUntilStop(t, w.bob, RunOptions{Executable: exe, CanSwitch: func() (bool, string) {
		if r, _, _ := readUpdateRequest(w.bobHome); r.To == "v9.9.6" {
			return cannot()
		}
		return true, ""
	}})
	runWith(t, w, w.alice, RunOptions{})
	for _, c := range []struct {
		req    UpdateRequest
		detail string
	}{
		{UpdateRequest{ID: "other", Exe: "/elsewhere/agentnet", To: "v9.9.9"}, "this daemon runs"},
		{UpdateRequest{ID: "mismatch", Exe: exe, To: "v9.9.9"}, "reports"},
		{UpdateRequest{ID: "cannot", Exe: exe, To: "v9.9.6"}, "not here"},
	} {
		if err := RequestUpdateSwitch(w.bobHome, c.req); err != nil {
			t.Fatal(err)
		}
		eventually(t, c.req.ID+" settled", func() bool {
			act, ok, _ := ReadUpdateActivation(w.bobHome)
			return ok && act.ID == c.req.ID
		})
		if act := activation(t, w.bob); act.Result != ActivationNotApplied || !strings.Contains(act.Detail, c.detail) {
			t.Fatalf("%s: %+v", c.req.ID, act)
		}
		if _, err := os.Stat(filepath.Join(w.bobHome, updateRequestFile)); !os.IsNotExist(err) {
			t.Fatalf("%s: request left behind", c.req.ID)
		}
	}
	task, _ := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "still working?", Kind: envelope.KindTask})
	waitState(t, w.bob, task.ID, stateAwaiting)
	w.bob.Accept(task.ID)
	waitState(t, w.bob, task.ID, stateAnswered)
	select {
	case err := <-res:
		t.Fatalf("the daemon stopped: %v", err)
	default:
	}
}

// At start the daemon completes or clears a request once and never begins a
// switch: already on the version, started in the daemon's place but on
// another version, or a daemon that simply started. Nothing loops.
func TestUpdateSettledAtStart(t *testing.T) {
	w := newWorld(t, "")
	exe := fakeProgram(t, "v9.9.9")
	for _, c := range []struct {
		version, env, want string
	}{
		{"v9.9.9", "", ActivationRunning},
		{"v9.9.8", "s2", ActivationFailed},
		{"v9.9.8", "", ActivationNotApplied},
	} {
		setVersion(t, c.version)
		t.Setenv(UpdateRestartEnv, c.env)
		id := "s" + fmt.Sprint(len(c.want))
		if c.env != "" {
			id = c.env
		}
		if err := RequestUpdateSwitch(w.bobHome, UpdateRequest{ID: id, Exe: exe, To: "v9.9.9"}); err != nil {
			t.Fatal(err)
		}
		res, stop := runUntilStop(t, w.bob, RunOptions{Executable: exe})
		eventually(t, id+" settled", func() bool {
			act, ok, _ := ReadUpdateActivation(w.bobHome)
			return ok && act.ID == id
		})
		if act := activation(t, w.bob); act.Result != c.want || act.Running != c.version {
			t.Fatalf("%s/%q: %+v", c.version, c.env, act)
		}
		if _, err := os.Stat(filepath.Join(w.bobHome, updateRequestFile)); !os.IsNotExist(err) {
			t.Fatal("request left behind")
		}
		time.Sleep(300 * time.Millisecond)
		select {
		case err := <-res:
			t.Fatalf("the daemon stopped at start: %v", err)
		default:
		}
		stop()
		if err := <-res; err != nil {
			t.Fatalf("stop: %v", err)
		}
	}
}
