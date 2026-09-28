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
	alice, bob, live, _ = liveWorldHub(t)
	return alice, bob, live
}

func liveWorldHub(t *testing.T) (alice, bob *client.Agent, live *Live, hub *testhub.Proc) {
	t.Helper()
	t.Setenv("AGENTNET_NOTIFY", "off")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	dir := filepath.Join(t.TempDir(), "hub")
	hub = testhub.Start(t, dir, "127.0.0.1:0", "")
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
	return alice, bob, NewLive(bob), hub
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
	if !o.Review[0].Notice || o.Review[0].At.IsZero() {
		t.Fatalf("review item %+v", o.Review[0])
	}
	for _, s := range o.Threads {
		if s.ID == id && (!s.NoticeOnly || s.Notices != 1 || s.Review != 0 || s.Unread != 0) {
			t.Fatalf("thread summary %+v", s)
		}
	}
	if !strings.HasPrefix(o.Review[0].Why, "review notice") {
		t.Fatalf("why %q", o.Review[0].Why)
	}
	th, err := live.Thread(id)
	if err != nil {
		t.Fatal(err)
	}
	m := th.Messages[0]
	if strings.Join(m.Actions, ",") != DoResolve || m.Next != "Needs you" || !strings.Contains(m.StateText, "nothing here can approve") {
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

// waitDirectory waits, without asking the Hub, for bob's pushed directory to
// satisfy ok; each change arrives on the change feed.
func waitDirectory(t *testing.T, l *Live, what string, ok func(Directory) bool) Directory {
	t.Helper()
	deadline := time.After(20 * time.Second)
	for {
		_, changed := l.Changed()
		o, err := l.Overview()
		if err != nil {
			t.Fatal(err)
		}
		if ok(o.Directory) {
			return o.Directory
		}
		select {
		case <-changed:
		case <-deadline:
			t.Fatalf("%s: directory %+v", what, o.Directory)
		}
	}
}

func member(d Directory, addr string) (DirMember, bool) {
	for _, m := range d.Members {
		if m.Address == addr {
			return m, true
		}
	}
	return DirMember{}, false
}

// The page's directory follows the server's pushed member list: someone
// who just joined appears (no history needed), presence follows their
// daemon, a revoked agent disappears, and without the server connection
// presence is unknown rather than stale. This installation is not listed.
func TestLiveDirectoryFollowsTheServer(t *testing.T) {
	alice, bob, live, hub := liveWorldHub(t)
	d := waitDirectory(t, live, "first list", func(d Directory) bool { return d.Status == DirectoryListed && d.Current })
	if _, ok := member(d, bob.Address); ok {
		t.Fatal("this installation is listed")
	}
	if m, ok := member(d, alice.Address); !ok || m.Presence != "offline" {
		t.Fatalf("alice (no daemon): %+v %v", m, ok)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	code, err := alice.Invite(ctx, "vitalii", time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	vitalii, err := client.Join(ctx, filepath.Join(t.TempDir(), "vitalii"), code, "laptop")
	if err != nil {
		t.Fatal(err)
	}
	defer vitalii.Close()
	waitDirectory(t, live, "newcomer listed", func(d Directory) bool { _, ok := member(d, vitalii.Address); return ok })
	if th, _ := live.Overview(); len(th.Threads) != 0 {
		t.Fatal("listing someone created a conversation")
	}

	vitalii.Logf = t.Logf
	run, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { vitalii.Run(run, client.RunOptions{}); close(done) }()
	waitDirectory(t, live, "newcomer online", func(d Directory) bool { m, _ := member(d, vitalii.Address); return m.Presence == "connected" })
	stop()
	<-done
	waitDirectory(t, live, "newcomer away", func(d Directory) bool {
		m, _ := member(d, vitalii.Address)
		return m.Presence == "reconnecting" || m.Presence == "offline"
	})

	if err := alice.Revoke(ctx, vitalii.Address); err != nil {
		t.Fatal(err)
	}
	waitDirectory(t, live, "revoked gone", func(d Directory) bool { _, ok := member(d, vitalii.Address); return !ok })

	hub.Stop()
	d = waitDirectory(t, live, "server gone", func(d Directory) bool { return !d.Current })
	for _, m := range d.Members {
		if m.Presence != "" {
			t.Fatalf("presence shown without the server: %+v", m)
		}
	}
	if d.Status != DirectoryListed || d.At.IsZero() {
		t.Fatalf("after losing the server: %+v", d)
	}
}
