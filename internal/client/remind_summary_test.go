package client

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestNativeReminderSummaryManyDueAndRestart(t *testing.T) {
	home := t.TempDir()
	seedFixtureStore(t, home)
	path := filepath.Join(home, "agent.db")
	s, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	a := &Agent{store: s}
	t.Cleanup(func() { _ = a.store.db.Close() })
	if err = a.SetAlertPrefs(AlertPrefs{Preview: NotificationPreviewFull}); err != nil {
		t.Fatal(err)
	}
	var notices []NativeNotification
	a.nativeNotifyContent = func(n NativeNotification) { notices = append(notices, n) }
	now := time.Now()
	tx, err := a.store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for range 257 {
		if _, err = tx.Exec(`INSERT INTO reminders(message,due_at,state,rev,notified_rev,created_at,updated_at) VALUES(?,?,'pending',1,0,?,?)`, protocol.NewID(), now.Add(-time.Minute).Unix(), now.Unix(), now.Unix()); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err = a.remindDue(now); err != nil {
		t.Fatal(err)
	}
	if len(notices) != 1 {
		t.Fatal("many due reminders did not produce one summary", len(notices))
	}
	n := notices[0]
	if n.Category != "reminder-summary" || n.Title != "Reminders" || n.Body != "Reminders are due" || n.Fragment != "" || n.ReadRef != (protocol.ReadRef{}) || len(n.Reminders) != 0 {
		t.Fatal("summary retained a frozen count/membership or lost its trusted category", n)
	}
	if err = a.store.db.Close(); err != nil {
		t.Fatal(err)
	}
	a.store, err = openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if active, err := a.HasAlertedDueReminders(); err != nil || !active {
		t.Fatal("reopen lost current alerted due state", active, err)
	}
	if _, err = a.remindDue(now); err != nil {
		t.Fatal(err)
	}
	if len(notices) != 1 {
		t.Fatal("restart replayed summary notification", len(notices))
	}
	for _, mode := range []NotificationPreview{NotificationPreviewFull, NotificationPreviewSender, NotificationPreviewHidden} {
		if err = a.SetAlertPrefs(AlertPrefs{Preview: mode}); err != nil {
			t.Fatal(err)
		}
		display := a.nativeDisplay("", "", "Reminders", "Reminders are due", false)
		if display.Category != "reminder-summary" || display.Body != "Reminders are due" {
			t.Fatal("privacy masking changed reminder summary meaning", mode, display)
		}
	}
}

func TestAlertedDueReminderSummaryPredicate(t *testing.T) {
	a := &Agent{store: securityStore(t)}
	now := time.Now()
	first, other := protocol.NewID(), protocol.NewID()
	for _, id := range []string{first, other} {
		if _, err := a.store.db.Exec(`INSERT INTO reminders(message,due_at,state,rev,notified_rev,created_at,updated_at) VALUES(?,?,'done',3,3,?,?)`, id, now.Add(-time.Hour).Unix(), now.Unix(), now.Unix()); err != nil {
			t.Fatal(err)
		}
	}
	check := func(label string, want bool) {
		t.Helper()
		got, err := a.HasAlertedDueReminders()
		if err != nil || got != want {
			t.Fatal(label, got, want, err)
		}
	}
	check("terminal", false)
	if _, err := a.store.db.Exec(`UPDATE reminders SET state='pending',due_at=? WHERE message=?`, now.Add(time.Hour).Unix(), first); err != nil {
		t.Fatal(err)
	}
	check("future alerted", false)
	if _, err := a.store.db.Exec(`UPDATE reminders SET due_at=?,notified_rev=2 WHERE message=?`, now.Add(-time.Hour).Unix(), first); err != nil {
		t.Fatal(err)
	}
	check("due unalerted generation", false)
	if _, err := a.store.db.Exec(`UPDATE reminders SET notified_rev=rev WHERE message=?`, first); err != nil {
		t.Fatal(err)
	}
	check("current alerted overdue", true)
	if _, err := a.store.db.Exec(`UPDATE reminders SET state='pending' WHERE message=?`, other); err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.db.Exec(`UPDATE reminders SET state='done' WHERE message=?`, first); err != nil {
		t.Fatal(err)
	}
	check("another alerted overdue intentionally retains summary", true)
	if _, err := a.store.db.Exec(`UPDATE reminders SET state='cancelled' WHERE message=?`, other); err != nil {
		t.Fatal(err)
	}
	check("last due ended", false)
	rows, err := a.store.db.Query("EXPLAIN QUERY PLAN "+alertedDueReminderSQL, now.Unix())
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	indexed := false
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err = rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		indexed = indexed || strings.Contains(detail, "reminders_due")
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !indexed {
		t.Fatal("summary predicate did not reuse existing pending/due index")
	}
}
