package ui

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/testhub"
)

// liveWorld enrolls alice (admin) and bob on a local Hub and runs bob's
// daemon; the page's Provider is over bob's real store.
func liveWorld(t *testing.T) (alice, bob *client.Agent, live *Live) {
	t.Helper()
	t.Setenv("AGENTNET_NOTIFY", "off")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	dir := filepath.Join(t.TempDir(), "hub")
	testhub.Start(t, dir, "127.0.0.1:0", "")
	alice, err := client.Join(ctx, filepath.Join(t.TempDir(), "alice"), testhub.BootstrapCode(t, dir), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { alice.Close() })
	code, err := alice.Invite(ctx, "bob", time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	bob, err = client.Join(ctx, filepath.Join(t.TempDir(), "bob"), code, "desk")
	if err != nil {
		t.Fatal(err)
	}
	bob.Logf = t.Logf
	run, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { bob.Run(run, client.RunOptions{}); close(done) }()
	t.Cleanup(func() { stop(); <-done; bob.Close() })
	return alice, bob, NewLive(bob)
}

func waitReview(t *testing.T, l *Live) Overview {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		o, err := l.Overview()
		if err != nil {
			t.Fatal(err)
		}
		if len(o.Review) > 0 {
			return o
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("nothing arrived for review")
	return Overview{}
}

// A review notice is a message that only asks for attention: the page shows
// it as needing the person and offers to close it, never to accept or run it.
func TestLiveReviewNoticeCanOnlyBeResolved(t *testing.T) {
	alice, bob, live := liveWorld(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := alice.SendMessage(ctx, client.Outgoing{To: bob.Address, Kind: envelope.KindMessage,
		Status: envelope.StatusReviewNotice, Body: "items wait for you"}); err != nil {
		t.Fatal(err)
	}
	o := waitReview(t, live)
	id := o.Review[0].ID
	if !strings.HasPrefix(o.Review[0].Why, "review notice") {
		t.Fatalf("why %q", o.Review[0].Why)
	}
	th, err := live.Thread(id)
	if err != nil {
		t.Fatal(err)
	}
	m := th.Messages[0]
	if strings.Join(m.Actions, ",") != DoResolve || m.Next != "Needs you" || !strings.Contains(m.StateText, "nothing here runs") {
		t.Fatalf("notice view %+v", m)
	}
	for _, do := range []string{DoAccept, DoAcceptAlways, DoDecline} {
		if _, err := live.Act(Action{Do: do, ID: id}); !errors.Is(err, ErrRefused) {
			t.Fatalf("%s on a notice: %v", do, err)
		}
	}
	if _, err := live.Act(Action{Do: DoResolve, ID: id}); err != nil {
		t.Fatal(err)
	}
	if th, _ := live.Thread(id); th.Messages[0].State != "resolved" || th.Messages[0].Actions != nil {
		t.Fatalf("after resolve %+v", th.Messages[0])
	}
	if o, _ := live.Overview(); len(o.Review) != 0 {
		t.Fatalf("review %+v", o.Review)
	}
	if _, err := live.Act(Action{Do: DoResolve, ID: id}); !errors.Is(err, ErrRefused) {
		t.Fatalf("second resolve: %v", err)
	}
}

// Trusting from the page names the fingerprint the person compared; without
// it, or with another key, nothing is trusted.
func TestLiveTrustNeedsTheComparedKey(t *testing.T) {
	alice, _, live := liveWorld(t)
	if _, err := live.Act(Action{Do: DoTrust, ID: alice.Address}); !errors.Is(err, ErrRefused) {
		t.Fatalf("trust without a key: %v", err)
	}
	if _, err := live.Act(Action{Do: DoTrust, ID: alice.Address, Key: "SHA256:not-the-key"}); !errors.Is(err, ErrRefused) ||
		!strings.Contains(err.Error(), "nothing was trusted") {
		t.Fatalf("trust with another key: %v", err)
	}
	if _, err := live.Act(Action{Do: DoTrust, ID: alice.Address, Key: alice.Self().Fingerprint()}); err != nil {
		t.Fatalf("trust with the compared key: %v", err)
	}
}
