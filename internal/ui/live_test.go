package ui

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/testhub"
)

// writeUIHarnessStub creates a PATH-discoverable stand-in on either platform.
// These provider tests must not launch a harness; bodies retain their failure
// or invocation-marker behavior if that boundary regresses.
func writeUIHarnessStub(t *testing.T, dir, name, unixBody, windowsBody string) {
	t.Helper()
	body := unixBody
	if runtime.GOOS == "windows" {
		name += ".cmd"
		body = "@echo off\r\n" + windowsBody
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
}

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

// The page shows one installation's device history: a DM's messages are not
// device threads or review items, and asking the page's own API for one
// directly is refused plainly, as is a device reply to one. Nothing is sent.
func TestLivePageLeavesOutConversations(t *testing.T) {
	alice, bob, live, _ := liveWorldHub(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	for _, a := range []*client.Agent{alice, bob} {
		if _, err := a.CreatePerson(ctx, "Person of "+a.Address); err != nil {
			t.Fatal(err)
		}
	}
	var conv string
	for deadline := time.Now().Add(20 * time.Second); conv == ""; time.Sleep(50 * time.Millisecond) {
		c, err := alice.CreateDM(ctx, bob.Address)
		if err == nil {
			conv = c
		} else if time.Now().After(deadline) {
			t.Fatalf("no DM: %v", err)
		}
	}
	if _, err := alice.SendConv(ctx, conv, client.ConvOutgoing{Kind: envelope.KindQuestion, Body: "dm question?"}); err != nil {
		t.Fatal(err)
	}
	if _, err := alice.SendMessage(ctx, client.Outgoing{To: bob.Address, Body: "device hello"}); err != nil {
		t.Fatal(err)
	}
	var dm string
	var o Overview
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		msgs, _ := bob.ConversationMessages(conv)
		o, _ = live.Overview()
		if len(msgs) == 1 && len(o.Threads) == 1 {
			dm = msgs[0].ID
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("DM %d, threads %+v", len(msgs), o.Threads)
		}
	}
	if o.Threads[0].Title != "device hello" || len(o.Review) != 0 {
		t.Fatalf("page shows DM content: threads %+v review %+v", o.Threads, o.Review)
	}
	if _, err := live.Thread(dm); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "agentnet dm show") {
		t.Fatalf("thread of a DM message: %v", err)
	}
	if _, err := live.Refresh(dm); !errors.Is(err, ErrRefused) {
		t.Fatalf("refresh of a DM message: %v", err)
	}
	if _, err := live.Send(Draft{To: alice.Address, Body: "device reply", ReplyTo: dm}); !errors.Is(err, ErrRefused) ||
		!strings.Contains(err.Error(), "conversation") {
		t.Fatalf("device reply to a DM message: %v", err)
	}
	if _, err := live.Act(Action{Do: DoAccept, ID: dm}); !errors.Is(err, ErrRefused) {
		t.Fatalf("accept on a DM message: %v", err)
	}
	// Through the page's HTTP API as the browser asks.
	srv := New(live, "127.0.0.1", testToken)
	get := httptest.NewRequest("GET", "/api/thread?id="+dm, nil)
	get.Host = "127.0.0.1"
	get.AddCookie(&http.Cookie{Name: cookieName, Value: testToken})
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, get)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "belongs to a conversation") {
		t.Fatalf("GET /api/thread for a DM message: %d %s", w.Code, w.Body)
	}
}

// Each reason a message is held has its own plain words; conversation holds
// never read as a failed signature.
func TestHoldReasons(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range []string{"key_changed", "proof_pending", "identity_conflict", "conflicting_duplicate", "invalid"} {
		text := holdReason(r, "bob/desk")
		if seen[text] {
			t.Errorf("%s shares its words with another reason: %q", r, text)
		}
		seen[text] = true
		if r != "invalid" && strings.Contains(text, "did not verify") {
			t.Errorf("%s reads as a failed signature: %q", r, text)
		}
	}
}

// runDaemon runs a's daemon until the returned stop (also at cleanup).
func runDaemon(t *testing.T, a *client.Agent) func() {
	t.Helper()
	a.Logf = t.Logf
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.Run(ctx, client.RunOptions{}); close(done) }()
	var once bool
	stop := func() {
		if !once {
			once = true
			cancel()
			<-done
		}
	}
	t.Cleanup(stop)
	return stop
}

// Human DMs on the page, over two real installations: a person exists only
// when set up; someone listed is shown by the name they claim until checked;
// two DMs with the same person stay separate, also after a restart; a DM
// question is held for the person and nothing runs it; DMs never become
// device history.
func TestLiveHumanDMs(t *testing.T) {
	t.Setenv("AGENTNET_NOTIFY", "off")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
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
	bobHome := filepath.Join(t.TempDir(), "bob")
	bob, err := client.Join(ctx, bobHome, code, "desk")
	if err != nil {
		t.Fatal(err)
	}
	runDaemon(t, alice)
	stopBob := runDaemon(t, bob)
	live := NewLive(bob)
	eventually := func(what string, cond func() bool) {
		t.Helper()
		for deadline := time.Now().Add(20 * time.Second); !cond(); time.Sleep(50 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
		}
	}
	overview := func(l *Live) Overview {
		t.Helper()
		o, err := l.Overview()
		if err != nil {
			t.Fatal(err)
		}
		return o
	}

	// Nobody has a person: none is made for anyone.
	if o := overview(live); !o.Persons || o.Person != nil || len(o.People) != 0 || len(o.DMs) != 0 {
		t.Fatalf("before any person: %+v %+v %+v", o.Person, o.People, o.DMs)
	}
	if _, err := alice.CreatePerson(ctx, "Alice"); err != nil {
		t.Fatal(err)
	}
	var listed PersonView
	eventually("alice listed by the name she claims", func() bool {
		for _, p := range overview(live).People {
			if p.Address == alice.Address {
				listed = p
			}
		}
		return listed.Address != ""
	})
	if listed.State != PersonListed || listed.Label != "Alice" || listed.Person != "" {
		t.Fatalf("listed person shown as known: %+v", listed)
	}
	if _, err := live.NewDM(alice.Address); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "person") {
		t.Fatalf("a DM without a person of your own: %v", err)
	}
	me, note, err := live.CreatePerson("Bob")
	if err != nil || me.State != PersonSelf || me.Label != "Bob" || note == "" {
		t.Fatalf("create person: %+v %q %v", me, note, err)
	}
	if _, _, err := live.CreatePerson("Bob again"); !errors.Is(err, ErrRefused) {
		t.Fatalf("a second person: %v", err)
	}

	// Two DMs with Alice, each its own conversation.
	var d1, d2 string
	eventually("a first DM", func() bool { d1, err = live.NewDM(alice.Address); return err == nil })
	if d2, err = live.NewDM(alice.Address); err != nil || d2 == d1 {
		t.Fatalf("second DM %q (first %q): %v", d2, d1, err)
	}
	for conv, body := range map[string]string{d1: "deploy topic", d2: "budget topic"} {
		if s, err := live.SendDM(DMDraft{Conv: conv, Body: body}); err != nil || s.State == "waiting" {
			t.Fatalf("send in %s: %+v %v", conv, s, err)
		}
	}
	eventually("alice to hold both", func() bool {
		m1, _ := alice.ConversationMessages(d1)
		m2, _ := alice.ConversationMessages(d2)
		return len(m1) == 1 && len(m2) == 1
	})
	if _, err := alice.SendConv(ctx, d1, client.ConvOutgoing{Body: "on deploy"}); err != nil {
		t.Fatal(err)
	}
	if _, err := alice.SendConv(ctx, d2, client.ConvOutgoing{Kind: envelope.KindQuestion, Body: "can you check the budget?"}); err != nil {
		t.Fatal(err)
	}
	bodies := func(l *Live, id string) string {
		t.Helper()
		d, err := l.DM(id)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, m := range d.Messages {
			out = append(out, m.Dir+":"+m.Body)
		}
		return strings.Join(out, "|")
	}
	eventually("both replies, each in its own DM", func() bool {
		return bodies(live, d1) == "out:deploy topic|in:on deploy" && bodies(live, d2) == "out:budget topic|in:can you check the budget?"
	})
	eventually("the receipt of a DM message sent from the page", func() bool {
		d, _ := live.DM(d1)
		return d.Messages[0].State == "delivered" && d.Messages[0].StateText == `Delivered to another person, who calls themselves "Alice" (Laptop)` // a person's claimed name and device, never the address
	})
	d, _ := live.DM(d2)
	q := d.Messages[1]
	if q.State != "conv_held" || !strings.Contains(q.StateText, "nothing runs it") || d.Peer.State != PersonPinned || d.Peer.Label != "Alice" {
		t.Fatalf("held question %+v, peer %+v", q, d.Peer)
	}
	time.Sleep(300 * time.Millisecond) // the daemon had its chance to claim anything
	if d, _ = live.DM(d2); d.Messages[1].State != "conv_held" {
		t.Fatalf("the DM question changed state: %+v", d.Messages[1])
	}
	o := overview(live)
	if len(o.DMs) != 2 || o.DMs[0].Peer.Person == "" || o.DMs[0].Peer.Person != o.DMs[1].Peer.Person || len(o.Threads) != 0 || len(o.Review) != 0 {
		t.Fatalf("overview: dms %+v threads %+v review %+v", o.DMs, o.Threads, o.Review)
	}
	people := 0
	for _, p := range o.People {
		if p.Address == alice.Address {
			people++
			if p.State != PersonPinned || p.Person != o.DMs[0].Peer.Person {
				t.Fatalf("alice after a DM: %+v", p)
			}
		}
	}
	if people != 1 {
		t.Fatalf("alice shown %d times: %+v", people, o.People)
	}

	// A restart keeps both DMs apart, and the person as it was set up.
	stopBob()
	bob.Close()
	again, err := client.Open(bobHome)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { again.Close() })
	live2 := NewLive(again)
	o = overview(live2)
	if o.Person == nil || o.Person.Person != me.Person || len(o.DMs) != 2 {
		t.Fatalf("after a restart: person %+v, %d DMs", o.Person, len(o.DMs))
	}
	if bodies(live2, d1) != "out:deploy topic|in:on deploy" || bodies(live2, d2) != "out:budget topic|in:can you check the budget?" {
		t.Fatalf("after a restart: %q / %q", bodies(live2, d1), bodies(live2, d2))
	}
}

// The page recommends an update only to a newer build: a preview newer
// than the stable release, or the same version, recommends nothing.
func TestRecommendedOnlyNewer(t *testing.T) {
	was := protocol.Version
	t.Cleanup(func() { protocol.Version = was })
	for _, c := range []struct {
		running, recommended, want string
		ok                         bool
	}{
		{"v0.3.0", "v0.2.1", "", true},            // installed preview, older stable: no downgrade
		{"v0.3.0", "v0.3.0", "", true},            // the same build
		{"v0.3.0-4-gabcdef1", "v0.3.0", "", true}, // a build after that release
		{"v0.3.0", "v0.3.1", "v0.3.1", true},      // a real update is still shown
		{"v0.2.1", "v0.3.0", "v0.3.0", true},
		{"dev", "v0.3.1", "", true},     // an unknown running version recommends nothing
		{"v0.3.0", "v0.3.1", "", false}, // no recommendation held
	} {
		protocol.Version = c.running
		if got := recommended(protocol.Release{Version: c.recommended}, c.ok); got != c.want {
			t.Errorf("running %s, recommended %s: %q, want %q", c.running, c.recommended, got, c.want)
		}
	}
}

// The page sees and changes the responder through the daemon's own view:
// detection is the daemon's PATH lookup (nothing runs), timeout and context
// files survive an edit of harness or directory, manual handling is a
// choice, and an unsupported harness is refused.
func TestLiveResponderControl(t *testing.T) {
	_, bob, live := liveWorld(t)
	// The daemon's PATH decides what can be chosen: a fake PATH with two
	// stand-in binaries, so the test does not depend on what is installed.
	bin := t.TempDir()
	for _, name := range []string{"claude", "codex"} {
		writeUIHarnessStub(t, bin, name, "#!/bin/sh\nexit 0\n", "exit /b 0\r\n")
	}
	t.Setenv("PATH", bin)
	v, err := live.ResponderStatus()
	if err != nil || v.Chosen || v.Manual || len(v.Harnesses) == 0 || v.Problem == "" {
		t.Fatalf("before choosing: %+v %v", v, err)
	}
	names := map[string]bool{}
	for _, h := range v.Harnesses {
		names[h.Name] = true
	}
	if !names["claude"] || !names["codex"] || !names["pi"] {
		t.Fatalf("harnesses = %+v", v.Harnesses)
	}
	dir := t.TempDir()
	if err := bob.SetResponder(&client.Responder{Harness: "claude", Dir: dir, Timeout: 42 * time.Second}); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	if _, err := live.SetResponder(ResponderChange{Harness: "codex", Dir: other}); err != nil {
		t.Fatal(err)
	}
	v, _ = live.ResponderStatus()
	if !v.Chosen || v.Manual || v.Harness != "codex" || v.Dir != other || v.Timeout != 42 {
		t.Fatalf("after edit (timeout must survive): %+v", v)
	}
	if v.Ready == (v.Problem != "") {
		t.Fatalf("ready and problem disagree: %+v", v)
	}
	for _, h := range v.Harnesses {
		if want := h.Name == "claude" || h.Name == "codex"; h.Found != want {
			t.Fatalf("detection must follow the daemon's PATH: %+v", h)
		}
	}
	if _, err := live.SetResponder(ResponderChange{Harness: "codex"}); err != nil { // keeps the directory
		t.Fatal(err)
	}
	if v, _ = live.ResponderStatus(); v.Dir != other {
		t.Fatalf("directory changed by an edit that named none: %+v", v)
	}
	if _, err := live.SetResponder(ResponderChange{Harness: "gpt-magic", Dir: other}); err == nil {
		t.Fatal("an unsupported harness was accepted")
	}
	// A supported harness the daemon cannot find is refused with the
	// reason, and the previous choice stays.
	if _, err := live.SetResponder(ResponderChange{Harness: "pi", Dir: other}); err == nil || !strings.Contains(err.Error(), "PATH") {
		t.Fatalf("missing binary accepted: %v", err)
	}
	if v, _ = live.ResponderStatus(); v.Harness != "codex" || v.Dir != other || v.Timeout != 42 {
		t.Fatalf("refusal changed the setting: %+v", v)
	}
	if v.Ready != true || v.Problem != "" {
		t.Fatalf("codex on the fake PATH with an existing directory must be ready: %+v", v)
	}
	if _, err := live.SetResponder(ResponderChange{Manual: true}); err != nil {
		t.Fatal(err)
	}
	if v, _ = live.ResponderStatus(); !v.Chosen || !v.Manual || v.Harness != "" {
		t.Fatalf("manual: %+v", v)
	}
}

// The page reacts to, edits and deletes through the daemon provider: the
// view carries the resolved controls and what this device may do; a
// message named by the wrong direction, an unknown id or another's message
// for editing is refused; a word is not a reaction.
func TestLiveMessageControls(t *testing.T) {
	alice, bob, live := liveWorld(t)
	alice.Logf = t.Logf
	run, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { alice.Run(run, client.RunOptions{}); close(done) }() // alice publishes her capabilities
	t.Cleanup(func() { stop(); <-done })
	ctx := context.Background()
	sent, err := alice.Send(ctx, bob.Address, "react to me", "")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	var note string
	for {
		note, err = live.React(ControlAction{ID: sent.ID, Dir: "in", Emoji: "👍"})
		if err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil || !strings.HasPrefix(note, "Reacted") {
		t.Fatalf("react: %q %v", note, err)
	}
	th, err := live.Thread(sent.ID)
	if err != nil {
		t.Fatal(err)
	}
	m := th.Messages[0]
	if len(m.Reactions) != 1 || m.Reactions[0].Emoji != "👍" || !m.Reactions[0].Mine || m.Reactions[0].By[0].ID != bob.Address || strings.Join(m.Can, ",") != "react" {
		t.Fatalf("bob's view of alice's message: %+v", m.Controls)
	}
	if _, err := live.React(ControlAction{ID: sent.ID, Dir: "out", Emoji: "👍"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong direction: %v", err)
	}
	if _, err := live.React(ControlAction{ID: sent.ID, Dir: "in", Emoji: "wow"}); !errors.Is(err, ErrRefused) {
		t.Fatalf("a word as a reaction: %v", err)
	}
	if _, err := live.EditMessage(ControlAction{ID: sent.ID, Dir: "in", Text: "mine now"}); !errors.Is(err, ErrRefused) {
		t.Fatalf("editing another's message: %v", err)
	}
	// Bob's own message: edit and delete from the page.
	reply, err := bob.Send(ctx, alice.Address, "my typo", sent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if note, err := live.EditMessage(ControlAction{ID: reply.ID, Dir: "out", Text: "my fix"}); err != nil || !strings.HasPrefix(note, "Edited") {
		t.Fatalf("edit: %q %v", note, err)
	}
	th, _ = live.Thread(sent.ID)
	if m := th.Messages[1]; !m.Edited || m.Text != "my fix" || m.Body != "my typo" || strings.Join(m.Can, ",") != "react,edit,delete" {
		t.Fatalf("bob's edited message: %+v %+v", m.Body, m.Controls)
	}
	if _, err := live.DeleteMessage(ControlAction{ID: reply.ID, Dir: "out"}); err != nil {
		t.Fatal(err)
	}
	th, _ = live.Thread(sent.ID)
	if m := th.Messages[1]; !m.Deleted || len(m.Can) != 0 {
		t.Fatalf("bob's deleted message: %+v", m.Controls)
	}
	if _, err := live.DeleteMessage(ControlAction{ID: "00000000000000000000000000000000", Dir: "out"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown id: %v", err)
	}
}
