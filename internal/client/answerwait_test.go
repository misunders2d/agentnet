package client

import (
	"context"
	"errors"
	"net"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/secfile"
)

func TestAwaitRoomReplyUsesLocalWakes(t *testing.T) {
	for _, finish := range []string{"reply", "cancel", "disconnect"} {
		t.Run(finish, func(t *testing.T) {
			w := newWorld(t, "")
			stop, err := listenChanges(w.alice.home, w.alice.Changed)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if stop != nil {
					stop()
				}
			}()
			ctx, cancel := context.WithCancel(tctx(t))
			defer cancel()
			var ready atomic.Bool
			checks := make(chan struct{}, 4)
			done := make(chan error, 1)
			go func() {
				reply, err := w.alice.awaitRoomReply(ctx, func() (*ConvMessage, error) {
					checks <- struct{}{}
					if ready.Load() {
						return &ConvMessage{Body: "verified reply"}, nil
					}
					return nil, nil
				})
				if finish == "reply" && (reply == nil || reply.Body != "verified reply") && err == nil {
					err = errors.New("reply wake omitted its stored reply")
				}
				done <- err
			}()
			// One initial storage check plus the existing socket's initial byte.
			for i := 0; i < 2; i++ {
				select {
				case <-checks:
				case <-ctx.Done():
					t.Fatal("initial local wake missing")
				}
			}
			select {
			case <-checks:
				t.Fatal("room wait rechecked unchanged storage without a wake")
			case <-time.After(350 * time.Millisecond):
			}
			switch finish {
			case "reply":
				ready.Store(true)
				w.alice.NoteChange()
			case "cancel":
				cancel()
			case "disconnect":
				stop()
				stop = nil
			}
			select {
			case err := <-done:
				if finish == "reply" && err != nil || finish == "cancel" && !errors.Is(err, context.Canceled) || finish == "disconnect" && !errors.Is(err, ErrDaemonNotRunning) {
					t.Fatalf("%s: %v", finish, err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("local wake did not finish room wait")
			}
		})
	}
}

// The command that asked waits for the answer and gets it, woken by the
// local daemon (MEL-537).
func TestAwaitReply(t *testing.T) {
	st := installStub(t, "slow")
	w := newWorld(t, "")
	setResponder(t, w.bob, "stub", st.dir, time.Minute)
	if err := w.bob.Approve(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	q, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "what OS?", Kind: envelope.KindQuestion})
	if err != nil {
		t.Fatal(err)
	}
	var statuses []string
	r, err := w.alice.AwaitReply(tctx(t), q.ID, 30*time.Second, func(v ExecView) { statuses = append(statuses, v.State) })
	if err != nil || r.Answer == nil || r.TimedOut || r.Stopped != nil {
		t.Fatalf("wait %+v %v", r, err)
	}
	if r.Answer.Body != "stub answer\n" && r.Answer.Body != "stub answer" || r.Answer.From != w.bob.Address || r.Answer.Kind != envelope.KindAnswer || r.Answer.Status != envelope.StatusDone {
		t.Fatalf("answer %+v", r.Answer)
	}
	// An answer already here is found at once, without waiting for a wake.
	start := time.Now()
	if r, err = w.alice.AwaitReply(tctx(t), q.ID, 30*time.Second, nil); err != nil || r.Answer == nil || time.Since(start) > 5*time.Second {
		t.Fatalf("second wait %+v %v", r, err)
	}
}

// A request that waits for someone's OK ends the wait at once with that
// status: no answer comes soon.
func TestAwaitReplyStopsOnAwaiting(t *testing.T) {
	st := installStub(t, "answer")
	w := newWorld(t, "")
	setResponder(t, w.bob, "stub", st.dir, time.Minute) // alice is not approved there
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	q, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "a task", Kind: envelope.KindTask})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	var told []ExecView
	r, err := w.alice.AwaitReply(tctx(t), q.ID, 60*time.Second, func(v ExecView) { told = append(told, v) })
	if err != nil || r.Stopped == nil || r.Stopped.State != "awaiting" || r.Answer != nil || r.TimedOut {
		// Each stage of the status path, for a failure seen only on CI.
		var state, detail string
		var due int64
		w.bob.store.db.QueryRow(`SELECT state, coalesce(detail,''), status_due FROM inbox WHERE id=?`, q.ID).Scan(&state, &detail, &due)
		t.Logf("host request state=%q detail=%q status_due=%d after %v", state, detail, due, time.Since(start))
		if rows, e := w.bob.store.db.Query(`SELECT id, state, coalesce(error,'') FROM outbox WHERE sub=? AND ref_id=?`, envelope.SubStatus, q.ID); e == nil {
			for rows.Next() {
				var id, st, why string
				if rows.Scan(&id, &st, &why) == nil {
					t.Logf("host status copy %s state=%s error=%q", id, st, why)
				}
			}
			rows.Close()
		}
		var held, kept int
		w.alice.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE sub=? AND ref_id=?`, envelope.SubStatus, q.ID).Scan(&kept)
		w.alice.store.db.QueryRow(`SELECT count(*) FROM quarantine`).Scan(&held)
		t.Logf("requester statuses kept=%d quarantined_messages=%d statuses_told=%+v", kept, held, told)
		t.Fatalf("wait %+v %v", r, err)
	}
	if time.Since(start) > 30*time.Second || len(told) == 0 || told[len(told)-1].State != "awaiting" || told[len(told)-1].Host != w.bob.Address {
		t.Fatalf("statuses %+v after %v", told, time.Since(start))
	}
}

// With no daemon running, nothing receives the answer: the wait says so
// at once and never sleeps in a loop.
func TestAwaitReplyDaemonNotRunning(t *testing.T) {
	w := newWorld(t, "")
	q, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "q", Kind: envelope.KindQuestion})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := w.alice.AwaitReply(tctx(t), q.ID, time.Minute, nil); !errors.Is(err, ErrDaemonNotRunning) || time.Since(start) > 5*time.Second {
		t.Fatalf("wait without a daemon: %v after %v", err, time.Since(start))
	}
	if _, err := w.alice.AwaitReply(tctx(t), "unknown-id", time.Minute, nil); !errors.Is(err, ErrNoMessage) {
		t.Fatalf("unknown request: %v", err)
	}
}

// The change socket writes one byte at once and one per change, nothing
// in between: a reader blocks on it instead of polling.
func TestChangesSocketWakesWithoutPolling(t *testing.T) {
	w := newWorld(t, "")
	stop, err := listenChanges(w.alice.home, w.alice.Changed)
	if err != nil {
		t.Fatal(err)
	}
	// An existing path is checked without changing it: Unix permissions or
	// the Windows DACL, where FileMode's Unix bits do not express access.
	if err := secfile.Touch(changesSockPath(w.alice.home)); err != nil {
		t.Fatalf("socket not owner-only: %v", err)
	}
	c, err := net.Dial("unix", changesSockPath(w.alice.home))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	read := func(within time.Duration) (int, error) {
		c.SetReadDeadline(time.Now().Add(within))
		buf := make([]byte, 16)
		return c.Read(buf)
	}
	if n, err := read(5 * time.Second); n != 1 || err != nil {
		t.Fatalf("first byte %d %v", n, err)
	}
	if n, err := read(300 * time.Millisecond); n != 0 || !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("a byte without a change: %d %v", n, err)
	}
	w.alice.NoteChange()
	if n, err := read(5 * time.Second); n != 1 || err != nil {
		t.Fatalf("wake on change %d %v", n, err)
	}
	stop()
	if _, err := read(5 * time.Second); err == nil {
		t.Fatal("connection open after stop")
	}
	if _, err := os.Stat(changesSockPath(w.alice.home)); !os.IsNotExist(err) {
		t.Fatalf("socket left after stop: %v", err)
	}
}

// The wait ends at its deadline with no answer, and when ctx ends.
func TestAwaitReplyTimesOut(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice) // bob is not running: no answer comes
	q, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "q", Kind: envelope.KindQuestion})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "the daemon listens", func() bool {
		c, err := net.Dial("unix", changesSockPath(w.alice.home))
		if err == nil {
			c.Close()
		}
		return err == nil
	})
	if r, err := w.alice.AwaitReply(tctx(t), q.ID, 300*time.Millisecond, nil); err != nil || !r.TimedOut {
		t.Fatalf("deadline %+v %v", r, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := w.alice.AwaitReply(ctx, q.ID, time.Minute, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled %v", err)
	}
}

// In a conversation, the wait finds the answer the participation's agent
// gave to the request (dm ask-agent).
func TestAwaitReplyConversation(t *testing.T) {
	st := installAgentStub(t)
	w, conv, _, _ := agentWorld(t)
	setResponder(t, w.bob, "agentstub", st.dir, time.Minute)
	pid := participate(t, w, conv, nil, nil)
	q, err := w.alice.AskAgentWithReceiver(tctx(t), pid, envelope.KindQuestion, "why did the deploy fail?", nil)
	if err != nil {
		t.Fatal(err)
	}
	r, err := w.alice.AwaitReply(tctx(t), q.ID, 30*time.Second, nil)
	if err != nil || r.Answer == nil || r.Answer.Kind != envelope.KindAnswer || r.Answer.From != w.bob.Address || r.Answer.Body != "the deploy failed at step 3" {
		t.Fatalf("wait %+v %v", r.Answer, err)
	}
}
