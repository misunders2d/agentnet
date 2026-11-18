package ui

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
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
