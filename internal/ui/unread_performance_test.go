package ui

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"runtime/pprof"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/sqlitedb"
	"github.com/misunders2d/agentnet/internal/testhub"
)

// Exercise the real projection with fixed unread work and growing irrelevant
// read history. Only three messages cross the owned relay. Their admitted inbox
// rows supply the historical fixture's actual sender, person and conversation
// authority; no daemon runs while historical rows are seeded or measured.
func TestLiveUnreadHistoryScale(t *testing.T) {
	for _, history := range []int{500, 5000} {
		t.Run(fmt.Sprint(history), func(t *testing.T) {
			alice, db, dmID, deviceID, author := unreadHistoryFixture(t)
			seedUnreadHistory(t, db, history, dmID, deviceID)
			alice.NoteChange()
			live := NewLive(alice)
			measure := func(phase string) UnreadPage {
				t.Helper()
				var page UnreadPage
				var err error
				start := time.Now()
				pprof.Do(t.Context(), pprof.Labels("phase", phase, "history", fmt.Sprint(history)), func(context.Context) {
					page, err = live.UnreadPage(UnreadQuery{Limit: 50})
				})
				if err != nil {
					t.Fatal(err)
				}
				t.Logf("history=%d phase=%s unread=%d elapsed=%s", history, phase, page.Total, time.Since(start))
				return page
			}
			check := func(page UnreadPage, ids ...string) {
				t.Helper()
				wanted := map[string]bool{}
				for _, id := range ids {
					wanted[id] = true
				}
				if page.Total != len(wanted) || len(page.Rows) != len(wanted) {
					t.Fatalf("unread %d/%d, want %d", page.Total, len(page.Rows), len(wanted))
				}
				for _, row := range page.Rows {
					if !wanted[row.ID] || row.Author != author || row.LID == "" || row.Key == "" {
						t.Fatalf("unexpected unread identity: kind=%s id=%s", row.Kind, row.ID)
					}
					if row.ID == dmID && row.Kind != "chat" || row.ID == deviceID && row.Kind != "device" {
						t.Fatalf("unread route changed: id=%s kind=%s", row.ID, row.Kind)
					}
					delete(wanted, row.ID)
				}
				if len(wanted) != 0 {
					t.Fatal("missing unread identities")
				}
			}
			first := measure("cold-unread")
			check(first, dmID, deviceID)
			warm := measure("warm-unread")
			check(warm, dmID, deviceID)
			if first.Content != warm.Content {
				t.Fatal("unchanged fixture changed content revision")
			}
			for i := range first.Rows {
				if first.Rows[i].Key != warm.Rows[i].Key {
					t.Fatal("cached projection changed logical identity")
				}
			}
			if err := alice.MarkRead([]string{dmID}); err != nil {
				t.Fatal(err)
			}
			check(measure("read-return"), deviceID)
			if err := alice.MarkUnread(dmID, []string{dmID}); err != nil {
				t.Fatal(err)
			}
			check(measure("new-unread-generation"), dmID, deviceID)
		})
	}
}

// A slow complete overview must not hold Review's first unread result behind
// unrelated cards/identity/decision work. Keep that work blocked throughout the
// request; a spinner or a later result after releasing it would not pass.
func TestLiveUnreadIndependentOfOverviewBuild(t *testing.T) {
	alice, _, dmID, deviceID, _ := unreadHistoryFixture(t)
	live := NewLive(alice)
	live.overviewMu.Lock()
	type result struct {
		page UnreadPage
		err  error
	}
	done := make(chan result, 1)
	go func() {
		page, err := live.UnreadPage(UnreadQuery{})
		done <- result{page, err}
	}()
	select {
	case got := <-done:
		live.overviewMu.Unlock()
		if got.err != nil || got.page.Total != 2 {
			t.Fatalf("independent unread: %d %v", got.page.Total, got.err)
		}
		ids := map[string]bool{}
		for _, r := range got.page.Rows {
			ids[r.ID] = true
		}
		if !ids[dmID] || !ids[deviceID] {
			t.Fatal("blocked overview lost exact unread targets")
		}
	case <-time.After(time.Second):
		live.overviewMu.Unlock()
		<-done
		t.Fatal("unread waited for unrelated full overview work")
	}
}

func unreadHistoryFixture(t *testing.T) (*client.Agent, *sql.DB, string, string, string) {
	t.Helper()
	t.Setenv("AGENTNET_NOTIFY", "off")
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	t.Cleanup(cancel)
	hubDir, home := t.TempDir(), t.TempDir()
	testhub.Start(t, hubDir, "127.0.0.1:0", "")
	alice, err := client.Join(ctx, home, testhub.BootstrapCode(t, hubDir), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { alice.Close() })
	code, err := alice.Invite(ctx, "bob", time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := client.Join(ctx, t.TempDir(), code, "desk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bob.Close() })
	for _, a := range []*client.Agent{alice, bob} {
		if _, err := a.CreatePerson(ctx, a.Address); err != nil {
			t.Fatal(err)
		}
	}
	stopA, stopB := runDaemon(t, alice), runDaemon(t, bob)
	var conv string
	waitFor(t, "owned DM authority", func() bool { conv, err = alice.CreateDM(ctx, bob.Address); return err == nil })
	if _, err := alice.SendConv(ctx, conv, client.ConvOutgoing{Body: "Owned DM initialization"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "owned DM admitted at recipient", goHas(bob, conv, "Owned DM initialization", nil))
	dm, err := bob.SendConv(ctx, conv, client.ConvOutgoing{Body: "Owned unread chat fixture"})
	if err != nil {
		t.Fatal(err)
	}
	direct, err := bob.SendMessage(ctx, client.Outgoing{To: alice.Address, Kind: envelope.KindMessage, Body: "Owned unread device fixture"})
	if err != nil {
		t.Fatal(err)
	}
	dsn, err := sqlitedb.FileURI(filepath.Join(home, "agent.db"), "_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	waitFor(t, "two admitted fixture messages", func() bool {
		var n int
		err := db.QueryRow(`SELECT count(*) FROM inbox WHERE id IN (?,?) AND local=0`, dm.ID, direct.ID).Scan(&n)
		return err == nil && n == 2
	})
	stopA()
	stopB()
	return alice, db, dm.ID, direct.ID, bob.Self().Fingerprint()
}

func seedUnreadHistory(t *testing.T, db *sql.DB, n int, dmID, deviceID string) {
	t.Helper()
	columns, err := db.Query(`PRAGMA table_info(inbox)`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for columns.Next() {
		var cid, notNull, pk int
		var name, kind string
		var def any
		if err := columns.Scan(&cid, &name, &kind, &notNull, &def, &pk); err != nil {
			t.Fatal(err)
		}
		names = append(names, `"`+name+`"`)
	}
	if err := columns.Err(); err != nil {
		t.Fatal(err)
	}
	columns.Close()
	expressions := append([]string(nil), names...)
	for i, name := range names {
		switch name {
		case `"id"`:
			expressions[i] = `?1`
		case `"arrival"`:
			expressions[i] = `NULL`
		case `"lid"`:
			expressions[i] = `CASE WHEN lid IS NULL THEN NULL ELSE ?1 END`
		case `"received_at"`, `"ts"`:
			expressions[i] = `?2`
		case `"received_ms"`:
			expressions[i] = `?2*1000`
		case `"read_at"`:
			expressions[i] = `?2`
		}
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	statement, err := tx.Prepare(`INSERT INTO inbox (` + strings.Join(names, ",") + `) SELECT ` + strings.Join(expressions, ",") + ` FROM inbox WHERE id=?3`)
	if err != nil {
		t.Fatal(err)
	}
	defer statement.Close()
	for i := 0; i < n; i++ {
		source := dmID
		if i%2 != 0 {
			source = deviceID
		}
		result, err := statement.Exec(protocol.NewID(), int64(1700000000+i), source)
		if err != nil {
			t.Fatal(err)
		}
		if count, _ := result.RowsAffected(); count != 1 {
			t.Fatal("missing admitted seed row")
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}
