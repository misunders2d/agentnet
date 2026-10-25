package core

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/testhub"
	"github.com/misunders2d/agentnet/internal/ui"
)

type recordedNativeNotice struct{ workspace, fragment, title, body, metadata string }
type mobileNotices struct {
	mu                 sync.Mutex
	notices            []recordedNativeNotice
	dismissed          []string
	dismissedInstances []string
	arrived            chan recordedNativeNotice
	events             chan string
}

func (n *mobileNotices) Notify(workspace, fragment, title, body, metadata string) {
	x := recordedNativeNotice{workspace, fragment, title, body, metadata}
	n.mu.Lock()
	n.notices = append(n.notices, x)
	n.mu.Unlock()
	if n.events != nil {
		n.events <- "notify"
	}
	n.arrived <- x
}
func (n *mobileNotices) Dismiss(workspace, fragment, expectedInstance string) {
	n.mu.Lock()
	n.dismissed = append(n.dismissed, workspace+":"+fragment)
	n.dismissedInstances = append(n.dismissedInstances, expectedInstance)
	n.mu.Unlock()
	if n.events != nil {
		n.events <- "dismiss:" + expectedInstance
	}
}

func (n *mobileNotices) dismissals() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.dismissed...)
}

func (n *mobileNotices) dismissedFragment(fragment string) bool {
	for _, target := range n.dismissals() {
		if target == "default:"+fragment {
			return true
		}
	}
	return false
}

// Real signed/encrypted sender -> synthetic relay -> phone App.Run -> Notifier.
// No reflection/direct invocation of notification producers or model execution.
func TestPhoneRealReceiveNotificationPreviewReviewAndReminder(t *testing.T) {
	t.Setenv("AGENTNET_NOTIFY", "off")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	dir := t.TempDir()
	testhub.Start(t, dir, "127.0.0.1:0", "")
	sender, err := client.Join(ctx, t.TempDir(), testhub.BootstrapCode(t, dir), "desktop")
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()
	if _, err = sender.CreatePerson(ctx, "Fixture sender"); err != nil {
		t.Fatal(err)
	}
	invite, err := sender.Invite(ctx, "phone", time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	phone, err := client.Join(ctx, home, invite, "phone")
	if err != nil {
		t.Fatal(err)
	}
	address := phone.Address
	if _, err = phone.CreatePerson(ctx, "Fixture phone"); err != nil {
		t.Fatal(err)
	}
	phone.Close()
	runCtx, stopSender := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); _ = sender.Run(runCtx, client.RunOptions{HumanOnly: true}) }()
	defer func() { stopSender(); <-done }()
	app, err := OpenApp(home, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	notices := &mobileNotices{arrived: make(chan recordedNativeNotice, 16)}
	app.SetNotifier(notices)
	if err = app.session.a.SetAlertPrefs(client.AlertPrefs{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err = app.Start(); err != nil {
		t.Fatal(err)
	}
	waitMobile(t, "phone/sender push streams", func() bool { return app.session.a.MemberView().Current && sender.MemberView().Current })
	take := func(label string) recordedNativeNotice {
		t.Helper()
		select {
		case n := <-notices.arrived:
			return n
		case <-time.After(10 * time.Second):
			t.Fatalf("missing %s notification", label)
			return recordedNativeNotice{}
		}
	}
	if _, err = app.session.a.CreateDM(ctx, sender.Address); err != nil {
		t.Fatal(err)
	}
	sent, err := sender.SendMessage(ctx, client.Outgoing{To: address, Kind: envelope.KindMessage, Body: "Locally decrypted real receive preview"})
	if err != nil {
		t.Fatal(err)
	}
	n := take("message")
	messageNotice := n
	if n.workspace != client.DefaultWorkspace || n.fragment != "msg="+sent.ID+"&dir=in" || n.title != "Fixture sender" || n.body != "Locally decrypted real receive preview" {
		t.Fatalf("message notice: %+v", n)
	}
	provider := app.phone(client.DefaultWorkspace, app.session)
	if _, err = provider.Act(ui.Action{Do: ui.DoRead, IDs: []string{sent.ID}}); err != nil {
		t.Fatal(err)
	}
	waitMobile(t, "exact committed read dismissal", func() bool { return notices.dismissedFragment(n.fragment) })
	notices.mu.Lock()
	readDismissed := append([]string(nil), notices.dismissed...)
	notices.mu.Unlock()
	if len(readDismissed) != 1 || readDismissed[0] != "default:msg="+sent.ID+"&dir=in" {
		t.Fatalf("committed read did not dismiss its exact notification: %v", readDismissed)
	}
	if err = provider.NotifySeen(sent.ID, []string{sent.ID}); err != nil {
		t.Fatal(err)
	}
	notices.mu.Lock()
	dismissed := append([]string(nil), notices.dismissed...)
	notices.mu.Unlock()
	if len(dismissed) != 1 {
		t.Fatalf("exact read dismissal: %v", dismissed)
	}
	task, err := sender.SendMessage(ctx, client.Outgoing{To: address, Kind: envelope.KindTask, Body: "Exact request needs the phone owner's decision"})
	if err != nil {
		t.Fatal(err)
	}
	n = take("review")
	reviewNotice := n
	if n.fragment != "msg="+task.ID+"&dir=in" || !strings.Contains(n.title, "Decision needed") || !strings.Contains(n.body, "Exact request") {
		t.Fatalf("review notice: %+v", n)
	}
	beforeRead, err := app.session.a.PageReview()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = provider.Act(ui.Action{Do: ui.DoRead, IDs: []string{sent.ID}}); err != nil {
		t.Fatal(err)
	}
	notices.mu.Lock()
	if len(notices.dismissed) != 1 {
		t.Errorf("reading another message changed unrelated notification tags: %v", notices.dismissed)
	}
	notices.mu.Unlock()
	afterRead, err := app.session.a.PageReview()
	if err != nil {
		t.Fatal(err)
	}
	if len(beforeRead.Device) != 1 || len(afterRead.Device) != 1 || afterRead.Device[0].ID != task.ID || afterRead.Device[0].State != beforeRead.Device[0].State {
		t.Fatalf("reading another message changed pending work: before=%+v after=%+v", beforeRead, afterRead)
	}
	if _, err = app.session.a.SetReminder(sent.ID, time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	n = take("reminder")
	reminderNotice := n
	if n.fragment != "msg="+sent.ID+"&dir=in" || !strings.Contains(n.title, "Reminder") || n.body != "Locally decrypted real receive preview" {
		t.Fatalf("reminder notice: %+v", n)
	}
	app.Stop()
	if err = app.Start(); err != nil {
		t.Fatal(err)
	}
	waitMobile(t, "restarted phone stream", func() bool { return app.session.a.MemberView().Current })
	notices.mu.Lock()
	count := len(notices.notices)
	notices.mu.Unlock()
	if count != 3 {
		t.Fatalf("already notified work repeated: %d", count)
	}
	if err = provider.DoneReminder(sent.ID); err != nil {
		t.Fatal(err)
	}
	waitMobile(t, "completed reminder exact dismissal", func() bool { return len(notices.dismissals()) == 2 })
	if got := notices.dismissals(); got[1] != "default:"+n.fragment {
		t.Fatalf("reminder dismissed another destination: %v", got)
	}
	if notices.dismissedFragment("msg=" + task.ID + "&dir=in") {
		t.Fatal("reminder completion dismissed pending Review")
	}
	app.Close()
	// Reopen the real disposable home without transport. The OS snapshot is
	// imported after its unchanged cached overview and never reannounced.
	for _, state := range []string{"read", "done", "snooze", "legacy-read", "user-dismissed"} {
		t.Run("restore-"+state, func(t *testing.T) {
			reopened, err := OpenApp(home, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			native := &mobileNotices{arrived: make(chan recordedNativeNotice, 8)}
			reopened.SetNotifier(native)
			waitMobile(t, "initial offline overview", func() bool {
				reopened.mu.Lock()
				defer reopened.mu.Unlock()
				return reopened.unread[client.DefaultWorkspace].known
			})
			previous := reminderNotice
			if state == "read" || state == "legacy-read" {
				previous = messageNotice
			}
			if state == "snooze" {
				if _, err = reopened.session.a.SetReminder(sent.ID, time.Now().Add(time.Hour)); err != nil {
					t.Fatal(err)
				}
			}
			entries := []restoredNotice{restoreEntry(t, previous), restoreEntry(t, reviewNotice)}
			if state == "legacy-read" {
				entries[0].Metadata = ""
			}
			if state == "user-dismissed" {
				entries = nil
			}
			if _, err = reopened.RestoreNotificationsJSON(restoreSnapshot(t, entries, true)); err != nil {
				t.Fatal(err)
			}
			if state != "user-dismissed" {
				waitMobile(t, "restored obsolete "+state+" exact dismissal", func() bool { return len(native.dismissals()) == 1 })
				if got := native.dismissals(); got[0] != "default:"+previous.fragment {
					t.Fatal("restored notice changed destination", got)
				}
				var metadata noticeMetadata
				if err = decodeNoticeJSON(previous.metadata, &metadata); err != nil {
					t.Fatal(err)
				}
				native.mu.Lock()
				actual := native.dismissedInstances[0]
				native.mu.Unlock()
				expected := metadata.Instance
				if state == "legacy-read" {
					expected = ""
				}
				if actual != expected {
					t.Fatal("restored dismissal lost emission instance")
				}
				if native.dismissedFragment(reviewNotice.fragment) {
					t.Fatal("restoration cleared unresolved Review")
				}
			}
			native.mu.Lock()
			announced := len(native.notices)
			native.mu.Unlock()
			if announced != 0 {
				t.Fatal("startup replayed a notification")
			}
		})
	}
	t.Log("Actual isolated core receive -> Notifier with bounded local preview, exact review/reminder/read destinations. Android OS/physical behavior remains separate.")
}

func TestPhoneFailedReadDoesNotDismissNotification(t *testing.T) {
	s, _ := fixture(t)
	notices := &mobileNotices{arrived: make(chan recordedNativeNotice, 1)}
	a := unreadApp(map[string]*Session{client.DefaultWorkspace: s})
	a.SetNotifier(notices)
	p := a.phone(client.DefaultWorkspace, s)
	s.Close() // Real storage failure; the native observer remains attached.
	if _, err := p.Act(ui.Action{Do: ui.DoRead, IDs: []string{"0123456789abcdef0123456789abcdef"}}); err == nil {
		t.Fatal("read on closed storage succeeded")
	}
	if dismissed := notices.dismissals(); len(dismissed) != 0 {
		t.Fatalf("failed read dismissed notifications: %v", dismissed)
	}
}

// Current signed linked-human read/reminder carriers drive the same consumer as
// local actions. All homes and transport belong to this disposable fixture.
func TestPhoneLinkedStateDismissesExactNotices(t *testing.T) {
	t.Setenv("AGENTNET_NOTIFY", "off")
	ctx := t.Context()
	dir := t.TempDir()
	testhub.Start(t, dir, "127.0.0.1:0", "")
	owner, err := client.Join(ctx, t.TempDir(), testhub.BootstrapCode(t, dir), "desktop")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	if _, err = owner.CreatePerson(ctx, "Owner"); err != nil {
		t.Fatal(err)
	}
	run := func(agent *client.Agent) {
		t.Helper()
		runCtx, stop := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() { defer close(done); _ = agent.Run(runCtx, client.RunOptions{HumanOnly: true}) }()
		t.Cleanup(func() { stop(); <-done })
	}
	run(owner)
	offer, err := owner.NewDeviceLink(ctx)
	if err != nil {
		t.Fatal(err)
	}
	app, err := OpenApp(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	notices := &mobileNotices{arrived: make(chan recordedNativeNotice, 32)}
	app.SetNotifier(notices)
	if err = app.Start(); err != nil {
		t.Fatal(err)
	}
	if _, err = app.setup.SetupJoin(ui.SetupJoin{Code: offer.Code}); err != nil {
		t.Fatal(err)
	}
	waitMobile(t, "linked phone approval", func() bool { r, e := owner.PendingLinks(); return e == nil && len(r) == 1 })
	requests, _ := owner.PendingLinks()
	if err = owner.DecideLink(ctx, requests[0].ID, true); err != nil {
		t.Fatal(err)
	}
	waitMobile(t, "verified phone link", func() bool { return app.session.a.LinkState().State == client.LinkLinked })
	invite, err := owner.Invite(ctx, "peer", time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	peer, err := client.Join(ctx, t.TempDir(), invite, "peer")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = peer.Close() })
	if _, err = peer.CreatePerson(ctx, "Peer"); err != nil {
		t.Fatal(err)
	}
	run(peer)
	if err = app.session.a.SetAlertPrefs(client.AlertPrefs{Enabled: true, Preview: client.NotificationPreviewHidden}); err != nil {
		t.Fatal(err)
	}
	conv, err := app.session.a.CreateDM(ctx, peer.Address)
	if err != nil {
		t.Fatal(err)
	}
	take := func() recordedNativeNotice {
		t.Helper()
		select {
		case n := <-notices.arrived:
			return n
		case <-time.After(10 * time.Second):
			t.Fatal("missing native notice")
			return recordedNativeNotice{}
		}
	}
	message := func(agent *client.Agent, body string) client.ConvMessage {
		t.Helper()
		var found client.ConvMessage
		waitMobile(t, "exact original "+body, func() bool {
			rows, e := agent.ConversationMessages(conv)
			for _, row := range rows {
				if row.Body == body {
					found = row
					return e == nil
				}
			}
			return false
		})
		return found
	}
	if _, err = app.session.a.SendConv(ctx, conv, client.ConvOutgoing{Body: "fixture chat context"}); err != nil {
		t.Fatal(err)
	}
	_ = message(peer, "fixture chat context")
	for _, body := range []string{"old notice", "newer unseen notice"} {
		if _, err = peer.SendConv(ctx, conv, client.ConvOutgoing{Body: body}); err != nil {
			t.Fatal(err)
		}
		_ = take()
	}
	oldPhone, newPhone := message(app.session.a, "old notice"), message(app.session.a, "newer unseen notice")
	oldOwner := message(owner, "old notice")
	oldRoute := "msg=" + oldPhone.ID + "&conv=" + conv + "&dir=in"
	newRoute := "msg=" + newPhone.ID + "&conv=" + conv + "&dir=in"
	task, err := peer.SendMessage(ctx, client.Outgoing{To: app.session.a.Address, Kind: envelope.KindTask, Body: "Keep native decision pending"})
	if err != nil {
		t.Fatal(err)
	}
	_ = take()
	provider := app.phone(client.DefaultWorkspace, app.session)
	if _, err = provider.Act(ui.Action{Do: ui.DoRead, IDs: []string{task.ID}}); err != nil {
		t.Fatal(err)
	}
	if err = owner.MarkRead([]string{oldOwner.ID}); err != nil {
		t.Fatal(err)
	}
	waitMobile(t, "linked exact read dismissal", func() bool { return notices.dismissedFragment(oldRoute) })
	if notices.dismissedFragment(newRoute) || notices.dismissedFragment("msg="+task.ID+"&dir=in") {
		t.Fatal("linked read cleared newer unread or native Review")
	}
	// A read with an obsolete deliberate-unread generation succeeds only for
	// ordinary read state. Its native notice must remain visible.
	if err = app.session.a.MarkUnread(newPhone.ID, []string{newPhone.ID}); err != nil {
		t.Fatal(err)
	}
	observed := message(app.session.a, "newer unseen notice").Attention
	if observed == nil {
		t.Fatal("missing deliberate-unread generation")
	}
	if err = app.session.a.MarkUnread(newPhone.ID, []string{newPhone.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err = provider.Act(ui.Action{Do: ui.DoRead, IDs: []string{newPhone.ID}, Attention: map[string]protocol.AttentionGeneration{newPhone.ID: *observed}}); err != nil {
		t.Fatal(err)
	}
	if err = provider.NotifySeen(conv, []string{newPhone.ID}); err != nil {
		t.Fatal(err)
	}
	if !message(app.session.a, "newer unseen notice").Unread || notices.dismissedFragment(newRoute) {
		t.Fatal("stale read/seen cleared newer deliberate-unread generation")
	}
	// The original is already read. Due notice generations now belong only
	// to the personal reminder, independently of the pending native task.
	for _, operation := range []string{"done", "cancel", "snooze"} {
		if _, err = owner.SetReminder(oldOwner.ID, time.Now().Add(1500*time.Millisecond)); err != nil {
			t.Fatal(err)
		}
		reminderNotice := take()
		before := len(notices.dismissals())
		switch operation {
		case "done":
			err = owner.DoneReminder(oldOwner.ID)
		case "cancel":
			err = owner.CancelReminder(oldOwner.ID)
		case "snooze":
			_, err = owner.SetReminder(oldOwner.ID, time.Now().Add(time.Hour))
		}
		if err != nil {
			t.Fatal(err)
		}
		waitMobile(t, "linked "+operation+" exact dismissal", func() bool { return len(notices.dismissals()) > before })
		if got := notices.dismissals(); got[len(got)-1] != "default:"+reminderNotice.fragment {
			t.Fatalf("%s dismissed another notice: %v", operation, got)
		}
	}
	if notices.dismissedFragment(newRoute) || notices.dismissedFragment("msg="+task.ID+"&dir=in") {
		t.Fatal("reminder transition cleared newer unread or native Review")
	}
	if review, e := app.session.a.PageReview(); e != nil || len(review.Device) != 1 || review.Device[0].ID != task.ID {
		t.Fatal("native Review changed", review, e)
	}
}

func TestPhoneNoticeUnreadRequiresCurrentBoundedCache(t *testing.T) {
	s, _ := fixture(t)
	ref := protocol.ReadRef{Fingerprint: s.a.Self().Fingerprint(), LID: protocol.NewID()}
	revision, _ := s.a.ViewChanges()
	if _, err := s.live.UnreadNotificationRefs(revision.Content, []protocol.ReadRef{ref}); err == nil {
		t.Fatal("missing overview index was treated as authoritative empty unread")
	}
	if _, err := s.unreadOverview(); err != nil {
		t.Fatal(err)
	}
	revision, _ = s.a.ViewChanges()
	got, err := s.live.UnreadNotificationRefs(revision.Content, []protocol.ReadRef{ref})
	if err != nil || len(got) != 1 || got[ref] {
		t.Fatal("current empty unread index", got, err)
	}
	s.a.NoteChange()
	if _, err = s.live.UnreadNotificationRefs(revision.Content, []protocol.ReadRef{ref}); err == nil {
		t.Fatal("stale content index could dismiss a newer notice")
	}
	if _, err = s.live.UnreadNotificationRefs(revision.Content, make([]protocol.ReadRef, protocol.MaxNotifySeenIDs+1)); err == nil {
		t.Fatal("oversized notice lookup accepted")
	}
}

// The shared generic Review tag can be replaced by a reminder. Ending that
// reminder must retain a pending aggregate decision, then clear once resolved.
func TestReviewAggregateReminderKeepsPendingDecision(t *testing.T) {
	s, _ := fixture(t)
	app := unreadApp(map[string]*Session{client.DefaultWorkspace: s})
	notices := &mobileNotices{arrived: make(chan recordedNativeNotice, 1)}
	app.SetNotifier(notices)
	s.trackNotice(client.DefaultWorkspace, client.NativeNotification{Fragment: "review", Category: "decision"})
	s.trackNotice(client.DefaultWorkspace, client.NativeNotification{Fragment: "review", Category: "reminder", Reminders: []client.NativeReminder{{Message: protocol.NewID(), Revision: 1}}})
	revision, _ := s.a.ViewChanges()
	app.refreshNotices(client.DefaultWorkspace, s, revision.Content, s.contentGeneration, ui.Overview{Review: []ui.ReviewItem{{ID: protocol.NewID()}}})
	if got := notices.dismissals(); len(got) != 0 {
		t.Fatalf("ended reminder cleared aggregate tag despite pending decision: %v", got)
	}
	app.refreshNotices(client.DefaultWorkspace, s, revision.Content, s.contentGeneration, ui.Overview{})
	if got := notices.dismissals(); len(got) != 1 || got[0] != "default:review" {
		t.Fatalf("resolved aggregate decision retained obsolete reminder tag: %v", got)
	}
}
