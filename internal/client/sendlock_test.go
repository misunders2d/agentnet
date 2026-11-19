package client

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/lockfile"
)

// holdSpool holds a's spool lock, as another process's file work does, until
// the test ends or the returned release.
func holdSpool(t *testing.T, a *Agent) func() {
	t.Helper()
	release, err := lockfile.Wait(a.spoolLockPath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	return release
}

func sendWithin(t *testing.T, a *Agent, conv string, out ConvOutgoing, bound time.Duration) (ConvSent, error) {
	t.Helper()
	type result struct {
		sent ConvSent
		err  error
	}
	done := make(chan result, 1)
	go func() {
		sent, err := a.SendConv(context.Background(), conv, out)
		done <- result{sent, err}
	}()
	select {
	case r := <-done:
		return r.sent, r.err
	case <-time.After(bound):
		t.Fatalf("send %q still waiting after %v", out.Body, bound)
		return ConvSent{}, nil
	}
}

// S6b: a text turn never waits on the spool lock, in a group or as a human
// turn with a guest present; a file turn still takes it, and goes once it is
// free.
func TestTextSendIsNotBlockedByHeldSpoolLock(t *testing.T) {
	t.Run("group", func(t *testing.T) {
		w, _, p, _ := groupTurnsFixture(t)
		release := holdSpool(t, w.alice)
		if _, err := sendWithin(t, w.alice, p.State.Conv, ConvOutgoing{Body: "text while files are prepared"}, 5*time.Second); err != nil {
			t.Fatalf("text group turn waited for the spool lock: %v", err)
		}
		path := filepath.Join(t.TempDir(), "note.txt")
		os.WriteFile(path, []byte("attached"), 0o600)
		done := make(chan error, 1)
		go func() {
			_, err := w.alice.SendConv(context.Background(), p.State.Conv, ConvOutgoing{Body: "with a file", Files: []OutgoingFile{{Path: path}}})
			done <- err
		}()
		select {
		case err := <-done:
			t.Fatalf("a file turn went out without the spool lock: %v", err)
		case <-time.After(500 * time.Millisecond):
		}
		release()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
	t.Run("human turn", func(t *testing.T) {
		w, carol, conv, _, _ := humanWorld(t)
		p, err := w.alice.InviteHuman(tctx(t), conv, carol.Address, nil, "")
		if err != nil {
			t.Fatal(err)
		}
		eventually(t, "human invite", func() bool { return pollState(t, carol, p.PID).State == PartInvited })
		if _, err := carol.AcceptParticipation(tctx(t), p.PID); err != nil {
			t.Fatal(err)
		}
		eventually(t, "inviter observes human acceptance", func() bool { return stateAt(t, w.alice, p.PID).HumanActive() })
		holdSpool(t, carol)
		if _, err := sendWithin(t, carol, conv, ConvOutgoing{Kind: envelope.KindMessage, Body: "guest text while files are prepared"}, 5*time.Second); err != nil {
			t.Fatalf("text human turn waited for the spool lock: %v", err)
		}
		eventually(t, "the guest's turn at both members", func() bool {
			return humanBodyCount(t, w.alice, conv, "guest text while files are prepared") == 1 && humanBodyCount(t, w.bob, conv, "guest text while files are prepared") == 1
		})
	})
}
