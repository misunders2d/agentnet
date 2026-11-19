package ui

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
)

// With the scoped-keys step, writes to another chat leave an open chat as it
// was: the page's skip (store.ts scopedRefetch) is proven by checkSkip on each
// read, and every kept read equals a fresh one (dmOracle).
func TestOtherChatWritesLeaveTheOpenChat(t *testing.T) {
	home := filepath.Join(t.TempDir(), "bob")
	alice, bob, live, _ := liveWorldHubAt(t, home)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	for _, a := range []*client.Agent{alice, bob} {
		if _, err := a.CreatePerson(ctx, "Person of "+a.Address); err != nil {
			t.Fatal(err)
		}
	}
	runDaemon(t, alice)
	var conv string
	for deadline := time.Now().Add(20 * time.Second); conv == ""; time.Sleep(50 * time.Millisecond) {
		c, err := alice.CreateDM(ctx, bob.Address)
		if err == nil {
			conv = c
		} else if time.Now().After(deadline) {
			t.Fatalf("no DM: %v", err)
		}
	}
	group := liveTwoMemberGroup(t, alice, bob)
	if _, err := alice.SendConv(ctx, conv, client.ConvOutgoing{Body: "in the open chat"}); err != nil {
		t.Fatal(err)
	}
	waitOpen(t, func() bool { th, err := live.DM(conv); return err == nil && len(th.Messages) > 0 })
	waitOpen(t, func() bool { _, err := live.DM(group); return err == nil })
	side, err := sql.Open("sqlite", "file:"+filepath.Join(home, "agent.db")+"?_pragma=busy_timeout(10000)")
	if err != nil {
		t.Fatal(err)
	}
	defer side.Close()
	if !bob.ViewKeysScoped() {
		if _, err := side.Exec(client.ViewCoverSchema); err != nil {
			t.Fatal(err)
		}
	}
	before, differed := skipsChecked.Load(), overviewDiffered.Load()
	for i := range 6 {
		if _, err := live.DM(conv); err != nil { // the open chat, as last seen
			t.Fatal(err)
		}
		switch i % 3 {
		case 0: // a message arrives in the other chat
			n := len(mustDM(t, live, group).Messages)
			if _, err := alice.SendConv(ctx, group, client.ConvOutgoing{Body: fmt.Sprint("elsewhere ", i)}); err != nil {
				t.Fatal(err)
			}
			waitOpen(t, func() bool { return len(mustDM(t, live, group).Messages) > n })
		case 1: // a table only chat views read, in the other chat
			if _, err := side.Exec(`INSERT INTO alert_mutes(conv) VALUES(?)`, group); err != nil {
				t.Fatal(err)
			}
		case 2:
			if _, err := side.Exec(`DELETE FROM alert_mutes WHERE conv=?`, group); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := live.DM(conv); err != nil {
			t.Fatal(err)
		}
	}
	if overviewDiffered.Load() != differed {
		t.Fatal("a kept view differed (see OVERVIEW CACHE above)")
	}
	if n := skipsChecked.Load() - before; n < 3 {
		t.Fatalf("only %d of 6 other-chat writes proven skippable", n)
	} else {
		t.Logf("%d of 6 other-chat writes proven skippable", n)
	}
}

func mustDM(t *testing.T, live *Live, conv string) DMThread {
	t.Helper()
	th, err := live.DM(conv)
	if err != nil {
		t.Fatal(err)
	}
	return th
}

// With the scoped-keys step, the open chat's read mark changes only its read
// state: the list and the chat after it read no message again (TDLib
// read_history_inbox), the page is still told (a scoped change), every kept
// read equals a fresh one (dmOracle, overviewOracle), and a read stored by
// anything else still reads the chat again.
func TestReadMarkKeepsTheOpenChat(t *testing.T) {
	home := filepath.Join(t.TempDir(), "bob")
	alice, bob, live, _ := liveWorldHubAt(t, home)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	for _, a := range []*client.Agent{alice, bob} {
		if _, err := a.CreatePerson(ctx, "Person of "+a.Address); err != nil {
			t.Fatal(err)
		}
	}
	runDaemon(t, alice)
	var conv string
	for deadline := time.Now().Add(20 * time.Second); conv == ""; time.Sleep(50 * time.Millisecond) {
		c, err := alice.CreateDM(ctx, bob.Address)
		if err == nil {
			conv = c
		} else if time.Now().After(deadline) {
			t.Fatalf("no DM: %v", err)
		}
	}
	for i := range 3 {
		if _, err := alice.SendConv(ctx, conv, client.ConvOutgoing{Body: fmt.Sprint("unread ", i)}); err != nil {
			t.Fatal(err)
		}
	}
	unread := func(th DMThread) (ids []string) {
		for _, m := range th.Messages {
			if m.Unread {
				ids = append(ids, m.ID)
			}
		}
		return ids
	}
	// Bob's copy of the DM arrives with the first message; until then there is
	// no conversation to read.
	waitOpen(t, func() bool {
		th, err := live.DM(conv)
		return err == nil && len(unread(th)) == 3
	})
	side, err := sql.Open("sqlite", "file:"+filepath.Join(home, "agent.db")+"?_pragma=busy_timeout(10000)")
	if err != nil {
		t.Fatal(err)
	}
	defer side.Close()
	if !bob.ViewKeysScoped() {
		if _, err := side.Exec(client.ViewCoverSchema); err != nil {
			t.Fatal(err)
		}
	}
	var built atomic.Int64
	dmProjected = func(c string) {
		if c == conv {
			built.Add(1)
		}
	}
	defer func() { dmProjected = nil }()
	differed := overviewDiffered.Load()
	ids := unread(mustDM(t, live, conv))
	if len(ids) != 3 {
		t.Fatalf("%d unread before the read mark, want 3", len(ids))
	}
	// The host's own background work (team sync, a wake from another process)
	// may make an unscoped change meanwhile, and then the chat rightly reads
	// again: wait for quiet, and try again on such a change.
	quiet := func() {
		last, _ := bob.ViewChanges()
		for stable := time.Now(); time.Since(stable) < 300*time.Millisecond; time.Sleep(20 * time.Millisecond) {
			if v, _ := bob.ViewChanges(); v.Unscoped != last.Unscoped {
				last, stable = v, time.Now()
			}
		}
	}
	var failure string
	for range 5 {
		if failure != "" {
			if _, err := side.Exec(`UPDATE inbox SET read_at=NULL WHERE id IN (?,?,?)`, ids[0], ids[1], ids[2]); err != nil {
				t.Fatal(err)
			}
		}
		quiet()
		if got := unread(mustDM(t, live, conv)); len(got) != 3 {
			t.Fatalf("%d unread before the read mark, want 3", len(got))
		}
		v0, _ := bob.ViewChanges()
		n0 := built.Load()
		if _, err := live.Act(Action{Do: DoRead, IDs: ids}); err != nil {
			t.Fatal(err)
		}
		v, _ := bob.ViewChanges()
		if v.Content == v0.Content {
			t.Fatal("the read mark told the page nothing")
		}
		if _, err := live.Overview(); err != nil {
			t.Fatal(err)
		}
		if got := unread(mustDM(t, live, conv)); len(got) != 0 {
			t.Fatalf("still unread after the read mark: %v", got)
		}
		v1, _ := bob.ViewChanges()
		switch n := built.Load() - n0; {
		case v.Unscoped != v0.Unscoped:
			failure = "the read mark was an unscoped change: every kept chat reads again"
		case n != 0 && v1.Unscoped == v0.Unscoped:
			t.Fatalf("the read mark read the chat %d more times", n)
		case n != 0:
			failure = fmt.Sprintf("an unscoped change read the chat %d more times", n)
		default:
			failure = ""
		}
		if failure == "" {
			break
		}
		t.Log(failure + "; again")
	}
	if failure != "" {
		t.Fatal(failure)
	}
	// A read state change no read mark of this host made: the chat reads again.
	if _, err := side.Exec(`UPDATE inbox SET read_at=NULL WHERE id=?`, ids[0]); err != nil {
		t.Fatal(err)
	}
	n0 := built.Load()
	if got := unread(mustDM(t, live, conv)); len(got) != 1 || got[0] != ids[0] {
		t.Fatalf("unread after another write: %v, want [%s]", got, ids[0])
	}
	if built.Load() == n0 {
		t.Fatal("another write left the chat as kept")
	}
	if overviewDiffered.Load() != differed {
		t.Fatal("a kept view differed (see OVERVIEW CACHE above)")
	}
}
