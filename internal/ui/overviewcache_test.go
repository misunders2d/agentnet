package ui

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/sqlitedb/schematest"
)

var (
	overviewCompared atomic.Int64
	overviewDiffered atomic.Int64
	chatBuilds       sync.Map // conv -> *atomic.Int64
)

// Every test of this package that reads a list also compares it with a fresh
// read: the check adds work and disables nothing.
func init() {
	overviewOracle = func(diff string) {
		if diff == "" {
			overviewCompared.Add(1)
			return
		}
		overviewDiffered.Add(1)
		fmt.Fprintln(os.Stderr, "OVERVIEW CACHE:", diff)
	}
	overviewChatBuilt = func(conv string) {
		n, _ := chatBuilds.LoadOrStore(conv, new(atomic.Int64))
		n.(*atomic.Int64).Add(1)
	}
}

func TestMain(m *testing.M) {
	code := m.Run()
	fmt.Fprintf(os.Stderr, "overview cache: %d kept lists equal a fresh read\n", overviewCompared.Load())
	fmt.Fprintf(os.Stderr, "schema image: %d fixture stores started from a copy, not schema proof\n", schematest.Copies())
	if n := overviewDiffered.Load(); n > 0 {
		fmt.Fprintf(os.Stderr, "overview cache: %d kept lists differed from a fresh read\n", n)
		code = 1
	}
	os.Exit(code)
}

func chatBuildCount(conv string) int64 {
	if n, ok := chatBuilds.Load(conv); ok {
		return n.(*atomic.Int64).Load()
	}
	return 0
}

type overviewFloor struct {
	t           *testing.T
	alice, bob  *client.Agent
	live        *Live
	conv, group string
	side        *sql.DB
}

// see waits for the list to show a change, then for a comparison with a fresh
// read made once nothing is changing: the list must equal it.
func (w *overviewFloor) see(what string, shows func(Overview) bool) Overview {
	w.t.Helper()
	var o Overview
	var err error
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if o, err = w.live.Overview(); err == nil && shows(o) {
			break
		}
		if time.Now().After(deadline) {
			raw, _ := json.Marshal(o.DMs)
			w.t.Fatalf("%s: never shown (%v): %s", what, err, raw)
		}
	}
	before := overviewCompared.Load()
	for deadline := time.Now().Add(30 * time.Second); overviewCompared.Load() == before; time.Sleep(20 * time.Millisecond) {
		w.bob.NoteChange()
		if _, err = w.live.Overview(); err != nil {
			w.t.Fatal(err)
		}
		if time.Now().After(deadline) {
			keys, _, _ := w.bob.ViewDirtySince(w.live.viewMark)
			w.t.Fatalf("%s: no quiet comparison; marked since: %v", what, keys)
		}
	}
	if n := overviewDiffered.Load(); n > 0 {
		w.t.Fatalf("%s: %d lists differed from a fresh read", what, n)
	}
	return o
}

func (w *overviewFloor) chat(o Overview, conv string) DMSummary {
	w.t.Helper()
	i := slices.IndexFunc(o.DMs, func(s DMSummary) bool { return s.ID == conv })
	if i < 0 {
		return DMSummary{}
	}
	return o.DMs[i]
}

func (w *overviewFloor) exec(query string, args ...any) {
	w.t.Helper()
	if _, err := w.side.Exec(query, args...); err != nil {
		w.t.Fatalf("%s: %v", query, err)
	}
	w.bob.NoteChange()
}

func shows(o Overview, text string) bool {
	raw, _ := json.Marshal(o)
	return strings.Contains(string(raw), text)
}

// A list built from kept chat summaries equals one read afresh after each kind of
// change, and a change costs a read of only the chat it touched (MEL-748).
func TestOverviewKeptSummariesMatchAFreshRead(t *testing.T) {
	home := filepath.Join(t.TempDir(), "bob")
	alice, bob, live, _ := liveWorldHubAt(t, home)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
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
	side, err := sql.Open("sqlite", "file:"+filepath.Join(home, "agent.db")+"?_pragma=busy_timeout(10000)")
	if err != nil {
		t.Fatal(err)
	}
	defer side.Close()
	w := &overviewFloor{t: t, alice: alice, bob: bob, live: live, conv: conv, group: group, side: side}

	if _, err = alice.SendConv(ctx, conv, client.ConvOutgoing{Body: "dm opening"}); err != nil {
		t.Fatal(err)
	}
	w.see("the first list", func(o Overview) bool { return w.chat(o, conv).Last == "dm opening" })
	if _, err = alice.SendConv(ctx, group, client.ConvOutgoing{Body: "group opening"}); err != nil {
		t.Fatal(err)
	}
	w.see("the group", func(o Overview) bool { return w.chat(o, group).Last == "group opening" })

	// An inbound message reads again only its own chat.
	dmBefore, groupBefore := chatBuildCount(conv), chatBuildCount(group)
	if _, err = alice.SendConv(ctx, conv, client.ConvOutgoing{Body: "inbound one"}); err != nil {
		t.Fatal(err)
	}
	o := w.see("an inbound message", func(o Overview) bool {
		s := w.chat(o, conv)
		return s.Last == "inbound one" && s.Unread == 2
	})
	if chatBuildCount(conv) == dmBefore {
		t.Fatal("the chat that changed was not read")
	}
	if n := chatBuildCount(group) - groupBefore; n != 0 {
		t.Fatalf("a message in one chat read another chat's messages %d times", n)
	}

	// A read mark.
	thread, err := live.DM(conv)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, m := range thread.Messages {
		if m.Dir == "in" {
			ids = append(ids, m.ID)
		}
	}
	if _, err = live.Act(Action{Do: DoRead, IDs: ids}); err != nil {
		t.Fatal(err)
	}
	w.see("a read mark", func(o Overview) bool { return w.chat(o, conv).Unread == 0 })

	// A build that fails after reading the marks: what they named is still read
	// again by the next one, or cleared attention would come back.
	before := chatBuildCount(conv)
	w.exec(`UPDATE conversations SET pinned_at=pinned_at WHERE id=?`, conv)
	live.overviewMu.Lock()
	err = live.viewDirty()
	live.overviewMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	w.see("a failed build's marks", func(Overview) bool { return chatBuildCount(conv) > before })

	// A delivery change: bob's own message, then its receipt.
	sent, err := live.SendDM(DMDraft{Conv: conv, Body: "outbound one"})
	if err != nil {
		t.Fatal(err)
	}
	sent = storedSend(t, live, conv, sent)
	w.see("a sent message", func(o Overview) bool { return w.chat(o, conv).Last == "outbound one" })
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		thread, err = live.DM(conv)
		if err != nil {
			t.Fatal(err)
		}
		i := slices.IndexFunc(thread.Messages, func(m DMMessage) bool { return m.ID == sent.ID })
		if i >= 0 && thread.Messages[i].State != "queued" && thread.Messages[i].State != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no delivery: %+v", thread.Messages)
		}
	}
	w.see("a delivery change", func(Overview) bool { return true })

	// A rename reaches every chat that names the person.
	if _, err = alice.RenamePerson(ctx, "Renamed colleague"); err != nil {
		t.Fatal(err)
	}
	w.see("a rename", func(o Overview) bool { return shows(o, "Renamed colleague") })

	// A device thread's topic: its name and its latest line are kept per peer.
	direct, err := alice.Send(ctx, bob.Address, "a device thread", "")
	if err != nil {
		t.Fatal(err)
	}
	w.see("a device thread", func(o Overview) bool { return shows(o, "a device thread") })
	if _, err = live.ChangeTopic("rename", TopicChange{Peer: alice.Address, ID: direct.ID, Title: "Named device thread"}); err != nil {
		t.Fatal(err)
	}
	w.see("a device topic rename", func(o Overview) bool { return shows(o, "Named device thread") })

	// A chat's topic.
	topic, err := alice.SendConv(ctx, conv, client.ConvOutgoing{Body: "A chat topic", Topic: "new"})
	if err != nil {
		t.Fatal(err)
	}
	w.see("a chat topic message", func(o Overview) bool { return w.chat(o, conv).Unread >= 1 })
	thread, err = live.DM(conv)
	if err != nil {
		t.Fatal(err)
	}
	topicRow := slices.IndexFunc(thread.Messages, func(m DMMessage) bool { return m.LID == topic.LID })
	if topicRow < 0 || thread.Messages[topicRow].Topic == "" {
		t.Fatalf("no topic row: %+v", thread.Messages)
	}
	if _, err = live.ChangeTopic("rename", TopicChange{Conv: conv, ID: thread.Messages[topicRow].Topic, Title: "Named chat topic"}); err != nil {
		t.Fatal(err)
	}
	w.see("a chat topic rename", func(Overview) bool { return true })

	// A continuity notice, as the read that writes it leaves it, and a recovered
	// person, which every list depends on.
	now := time.Now()
	w.exec(`INSERT INTO inbox(id,sender,ts,kind,body,received_at,received_ms,read_at,acked,local,conv,lid,sub) VALUES('continuity/test',?,?,'message','Someone signed in again',?,?,?,1,1,?,'continuity/test','continuity')`,
		bob.Address, now.Unix(), now.Unix(), now.UnixMilli(), now.Unix(), group)
	w.see("a continuity notice", func(o Overview) bool { return w.chat(o, group).Count >= 1 })
	dmBefore, groupBefore = chatBuildCount(conv), chatBuildCount(group)
	w.exec(`INSERT INTO config(k,v) VALUES('continuity-head/nobody','h')`)
	w.see("a recovered person", func(Overview) bool { return chatBuildCount(conv) > dmBefore && chatBuildCount(group) > groupBefore })
	w.exec(`DELETE FROM config WHERE k='continuity-head/nobody'`)
	w.see("a recovery undone", func(Overview) bool { return true })

	// Another process writes, and wakes this one as kick.go does.
	if _, err = alice.SendConv(ctx, conv, client.ConvOutgoing{Body: "inbound two"}); err != nil {
		t.Fatal(err)
	}
	w.see("the second inbound", func(o Overview) bool { return w.chat(o, conv).Last == "inbound two" })
	w.exec(`DELETE FROM skipped_copies`)
	w.exec(`UPDATE inbox SET read_at=? WHERE conv=? AND read_at IS NULL`, now.Unix(), conv)
	w.see("a read from another process", func(o Overview) bool { return w.chat(o, conv).Unread == 0 })

	// History copying: what a whole catch-up writes reads no chat, and its own
	// progress still arrives.
	dmBefore, groupBefore = chatBuildCount(conv), chatBuildCount(group)
	w.exec(`INSERT INTO history_jobs(device,fingerprint,pos,convs_total,state,created_at,updated_at) VALUES(?,'fp','',3,'ended',1,1)`, alice.Address)
	w.exec(`INSERT INTO history_catchup(device,fingerprint,phase,pos,inbox_ceiling,outbox_ceiling,tail,context_done) VALUES(?,'fp','done','',0,0,0,1)`, alice.Address)
	w.exec(`INSERT INTO history_copies(recipient_fp,conv,author,lid,hash,carrier,source_dir,source_id) VALUES('fp',?,'a','l','h','carrier-a','in','i')`, conv)
	w.see("history progress", func(o Overview) bool { return len(o.History) == 1 && o.History[0].Device == alice.Address })
	if n := chatBuildCount(conv) + chatBuildCount(group) - dmBefore - groupBefore; n != 0 {
		t.Fatalf("history copying read chat messages %d times", n)
	}

	// A group's member change.
	person, _, err := bob.Person()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = alice.RenameGroup(ctx, group, "Renamed harbor"); err != nil {
		t.Fatal(err)
	}
	w.see("a group rename", func(o Overview) bool { return shows(o, "Renamed harbor") })
	if _, err = alice.RemoveGroupMember(ctx, group, person.Person); err != nil {
		t.Fatal(err)
	}
	w.see("a member removed", func(o Overview) bool {
		s := w.chat(o, group)
		return s.ID == "" || s.Frozen != "" || len(s.Members) < 2 || s.Role != "member"
	})

	// A new page reading the same store shows the same chats.
	kept, err := live.Overview()
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := NewLive(bob).Overview()
	if err != nil {
		t.Fatal(err)
	}
	x, _ := json.Marshal(kept.DMs)
	y, _ := json.Marshal(fresh.DMs)
	if string(x) != string(y) {
		t.Fatalf("after a restart:\nkept  %s\nfresh %s", x, y)
	}
	_ = o
}
