package core

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func restoreEntry(t *testing.T, n recordedNativeNotice) restoredNotice {
	t.Helper()
	tag, _, err := canonicalNotice(n.workspace, n.fragment)
	if err != nil {
		t.Fatal(err)
	}
	return restoredNotice{n.workspace, tag, n.metadata}
}
func restoreSnapshot(t *testing.T, entries []restoredNotice, known bool) string {
	t.Helper()
	raw, err := json.Marshal(noticeSnapshot{V: 1, Known: &known, Entries: entries})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestPhoneRestoreMetadataBoundaries(t *testing.T) {
	s, _ := fixture(t)
	a := unreadApp(map[string]*Session{client.DefaultWorkspace: s})
	ref := protocol.ReadRef{Fingerprint: s.a.Self().Fingerprint(), LID: protocol.NewID()}
	n := client.NativeNotification{Fragment: "msg=" + ref.LID + "&dir=in", Category: "activity", ReadRef: ref, Title: "private title", Body: "private body"}
	metadata, raw := newNoticeMetadata(client.DefaultWorkspace, n)
	if strings.Contains(raw, "private") || !metadata.valid(client.DefaultWorkspace, metadata.Tag) {
		t.Fatal("metadata contains preview or invalid binding")
	}
	invalid := []restoredNotice{
		{protocol.NewID(), metadata.Tag, raw},
		{client.DefaultWorkspace, metadata.Tag + "&extra=1", raw},
		{client.DefaultWorkspace, metadata.Tag, strings.Replace(raw, `"v":1`, `"v":2`, 1)},
		{client.DefaultWorkspace, metadata.Tag, strings.TrimSuffix(raw, "}") + `,"body":"private"}`},
		{client.DefaultWorkspace, metadata.Tag, strings.TrimSuffix(raw, "}") + `,"reminders":null}`},
		{client.DefaultWorkspace, metadata.Tag, strings.Repeat("x", maxNoticeMetadata+1)},
		{protocol.NewID(), "review&workspace=" + protocol.NewID(), ""},
	}
	for _, bad := range invalid {
		result, err := a.RestoreNotificationsJSON(restoreSnapshot(t, []restoredNotice{bad}, true))
		if err != nil {
			t.Fatal(err)
		}
		var summary struct{ Restored, Unknown int }
		if json.Unmarshal([]byte(result), &summary) != nil || summary.Restored != 0 || summary.Unknown != 1 {
			t.Fatal("invalid restoration was not explicit unknown", result)
		}
	}
	if _, err := a.RestoreNotificationsJSON(restoreSnapshot(t, make([]restoredNotice, maxNoticeRestore+1), true)); err == nil {
		t.Fatal("unbounded restore chunk accepted")
	}
	if _, err := a.RestoreNotificationsJSON(`{"v":1,"known":true,"entries":[{"workspace":"default","tag":"review&workspace=default","metadata":null}]}`); err == nil {
		t.Fatal("malformed null metadata downgraded to legacy")
	}
	result, err := a.RestoreNotificationsJSON(restoreSnapshot(t, nil, false))
	if err != nil || !strings.Contains(result, `"known":false`) {
		t.Fatal("snapshot failure became known empty", result, err)
	}
	n = client.NativeNotification{Fragment: "", Category: "reminder-summary"}
	metadata, raw = newNoticeMetadata(client.DefaultWorkspace, n)
	if metadata.Category != "reminder-summary" || metadata.Ref != nil || len(metadata.Reminders) != 0 || len(raw) > maxNoticeMetadata {
		t.Fatal("generic reminder summary did not retain its bounded meaning")
	}
}

func TestPhoneRestoreNeverReplacesNewerTrackedInstance(t *testing.T) {
	s, _ := fixture(t)
	current, _ := newNoticeMetadata(client.DefaultWorkspace, client.NativeNotification{Fragment: "review", Category: "security"})
	old := phoneNotice{fragment: "review", category: "reminder", instance: protocol.NewID(), reminders: []client.NativeReminder{{Message: protocol.NewID(), Revision: 1}}}
	s.notices = map[string]phoneNotice{"review": {fragment: "review", category: "security", instance: current.Instance}}
	s.importNotices([]phoneNotice{old})
	if got := s.notices["review"]; got.instance != current.Instance || got.category != "security" {
		t.Fatal("startup snapshot replaced newer emitted instance")
	}
}

func TestPhoneRestoreReconcilesBeforeNewDisplay(t *testing.T) {
	s, _ := fixture(t)
	a := unreadApp(map[string]*Session{client.DefaultWorkspace: s})
	native := &mobileNotices{arrived: make(chan recordedNativeNotice, 2), events: make(chan string, 4)}
	a.SetNotifier(native)
	a.phone(client.DefaultWorkspace, s)
	waitMobile(t, "initial cache", func() bool { a.mu.Lock(); defer a.mu.Unlock(); return a.unread[client.DefaultWorkspace].known })
	old := protocol.NewID()
	s.mu.Lock()
	s.restoredNotices = append(s.restoredNotices, phoneNotice{fragment: "review", category: "decision", instance: old})
	s.notifications = append(s.notifications, client.NativeNotification{Fragment: "review", Category: "security"})
	s.mu.Unlock()
	s.signal()
	for _, expected := range []string{"dismiss:" + old, "notify"} {
		select {
		case actual := <-native.events:
			if actual != expected {
				t.Fatalf("startup/native lane order: got %s want %s", actual, expected)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("startup/native lane did not drain")
		}
	}
}

func TestPhoneReminderSummaryManyDueRestore(t *testing.T) {
	t.Setenv("AGENTNET_NOTIFY", "off")
	s, home := fixture(t)
	s.Close()
	db, err := sql.Open("sqlite", filepath.Join(home, "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for range 257 {
		if _, err = tx.Exec(`INSERT INTO reminders(message,due_at,state,rev,notified_rev,created_at,updated_at) VALUES(?,?,'pending',1,1,?,?)`, protocol.NewID(), now.Add(-time.Hour).Unix(), now.Unix(), now.Unix()); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	metadata, raw := newNoticeMetadata(client.DefaultWorkspace, client.NativeNotification{Fragment: "", Category: "reminder-summary"})
	if metadata.Category != "reminder-summary" || metadata.Ref != nil || len(metadata.Reminders) != 0 {
		t.Fatal("summary retained a frozen membership")
	}
	entry := restoredNotice{client.DefaultWorkspace, metadata.Tag, raw}
	var app *App
	t.Cleanup(func() {
		if app != nil {
			app.Close()
		}
	})
	open := func() (*mobileNotices, <-chan uint64) {
		t.Helper()
		var e error
		app, e = OpenApp(home, 0)
		if e != nil {
			t.Fatal(e)
		}
		native := &mobileNotices{arrived: make(chan recordedNativeNotice, 4)}
		app.SetNotifier(native)
		// Warm the very same compact view before observing the event lane.
		if _, e = app.session.unreadOverview(); e != nil {
			t.Fatal(e)
		}
		waitMobile(t, "summary initial cached overview", func() bool { app.mu.Lock(); defer app.mu.Unlock(); return app.unread[client.DefaultWorkspace].known })
		processed := make(chan uint64, 16)
		session := app.session
		session.mu.Lock()
		hook := session.content
		session.content = func(content, generation uint64) { hook(content, generation); processed <- content }
		session.mu.Unlock()
		return native, processed
	}
	await := func(processed <-chan uint64) {
		t.Helper()
		revision, _ := app.session.a.ViewChanges()
		timer := time.NewTimer(3 * time.Second)
		defer timer.Stop()
		for {
			select {
			case content := <-processed:
				if content >= revision.Content {
					return
				}
			case <-timer.C:
				t.Fatal("summary content event did not finish")
			}
		}
	}
	assertQuiet := func(native *mobileNotices, wantDismiss int) {
		t.Helper()
		native.mu.Lock()
		defer native.mu.Unlock()
		if len(native.notices) != 0 || len(native.dismissed) != wantDismiss {
			t.Fatal("restored summary replayed or cleared at wrong current state", len(native.notices), len(native.dismissed), wantDismiss)
		}
	}
	native, processed := open()
	if _, err = app.RestoreNotificationsJSON(restoreSnapshot(t, []restoredNotice{entry}, true)); err != nil {
		t.Fatal(err)
	}
	await(processed)
	assertQuiet(native, 0)
	if _, err = db.Exec(`UPDATE reminders SET state='done'`); err != nil {
		t.Fatal(err)
	}
	other := protocol.NewID()
	if _, err = db.Exec(`INSERT INTO reminders(message,due_at,state,rev,notified_rev,created_at,updated_at) VALUES(?,?,'pending',1,1,?,?)`, other, now.Add(-time.Minute).Unix(), now.Unix(), now.Unix()); err != nil {
		t.Fatal(err)
	}
	app.session.a.NoteChange()
	await(processed)
	assertQuiet(native, 0) // A different currently alerted overdue reminder intentionally retains it.
	app.Close()
	native, processed = open()
	if _, err = app.RestoreNotificationsJSON(restoreSnapshot(t, []restoredNotice{entry}, true)); err != nil {
		t.Fatal(err)
	}
	await(processed)
	assertQuiet(native, 0)
	if _, err = db.Exec(`UPDATE reminders SET state='done' WHERE message=?`, other); err != nil {
		t.Fatal(err)
	}
	for _, remaining := range []struct {
		due     int64
		alerted int
		state   string
	}{
		{now.Add(time.Hour).Unix(), 1, "pending"}, {now.Add(-time.Hour).Unix(), 0, "pending"}, {now.Add(-time.Hour).Unix(), 1, "cancelled"},
	} {
		if _, err = db.Exec(`INSERT INTO reminders(message,due_at,state,rev,notified_rev,created_at,updated_at) VALUES(?,?,?,1,?,?,?)`, protocol.NewID(), remaining.due, remaining.state, remaining.alerted, now.Unix(), now.Unix()); err != nil {
			t.Fatal(err)
		}
	}
	app.session.a.NoteChange()
	await(processed)
	assertQuiet(native, 1)
	if got := native.dismissals(); got[0] != "default:" {
		t.Fatal("summary dismissed another tag", got)
	}
	native.mu.Lock()
	instance := native.dismissedInstances[0]
	native.mu.Unlock()
	if instance != metadata.Instance {
		t.Fatal("summary restoration lost exact emission instance")
	}
}
