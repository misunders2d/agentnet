package ui

import (
	"context"
	"database/sql"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/testhub"
)

// A question this device asked its own agent from a terminal session that
// ended before it ran waits for the person with no attempt (MEL-580). Its old
// device thread shows the sent copy: it offers Mark as handled with the
// reason, and no Reply that the never-run request would refuse.
func TestOwnNeverRunRequestClosesFromDeviceThread(t *testing.T) {
	t.Setenv("AGENTNET_NOTIFY", "off")
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	hub, home := t.TempDir(), t.TempDir()
	testhub.Start(t, hub, "127.0.0.1:0", "")
	a, err := client.Join(ctx, home, testhub.BootstrapCode(t, hub), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	stop := runDaemon(t, a)
	sent, err := a.SendMessage(ctx, client.Outgoing{To: a.Address, Kind: envelope.KindQuestion, Body: "Read-only follow-up"})
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(home, "agent.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	waitFor(t, "self request received", func() bool {
		var n int
		return db.QueryRow(`SELECT count(*) FROM inbox WHERE id=? AND replica=0`, sent.ID).Scan(&n) == nil && n == 1
	})
	stop()
	const why = "The terminal session that asked was closed before it handled this. Answer it, run it with your agent, or mark it handled."
	if res, err := db.Exec(`UPDATE inbox SET state='needs_human',detail=?,attempts=0 WHERE id=? AND replica=0`, why, sent.ID); err != nil {
		t.Fatal(err)
	} else if n, _ := res.RowsAffected(); n != 1 {
		t.Fatalf("staged %d received copies", n)
	}
	live := NewLive(a)
	shown := func() Message {
		t.Helper()
		th, err := live.Thread(sent.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range th.Messages {
			if m.ID == sent.ID {
				return m
			}
		}
		t.Fatalf("request not shown: %+v", th.Messages)
		return Message{}
	}
	if m := shown(); m.Dir != "out" || m.Continuation != nil || !slices.Equal(m.Actions, []string{DoResolve}) || m.Detail != why {
		t.Fatalf("never-run own request: dir %s continuation %+v actions %v detail %q", m.Dir, m.Continuation, m.Actions, m.Detail)
	}
	if _, err := live.Act(Action{Do: DoResolve, ID: sent.ID}); err != nil {
		t.Fatal(err)
	}
	if m := shown(); len(m.Actions) != 0 || m.Continuation != nil {
		t.Fatalf("handled request still offers %v %+v", m.Actions, m.Continuation)
	}
}
