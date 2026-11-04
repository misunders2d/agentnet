package ui

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/testhub"
)

func queuedRowsOf(t *testing.T, l *Live, conv, lid string) (rows []DMMessage) {
	t.Helper()
	thread, err := l.DM(conv)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range thread.Messages {
		if m.LID == lid {
			rows = append(rows, m)
		}
	}
	return rows
}

// The incident in MEL-748, end to end through the app's own calls: a chain
// request that hangs must not make the composer wait, cancel the message or show
// a request URL, and the same message sent again stays one message.
func TestLiveSendAcceptsThroughAStalledRequest(t *testing.T) {
	alice, bob, live, hub := liveWorldHub(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	for _, a := range []*client.Agent{alice, bob} {
		if _, err := a.CreatePerson(ctx, "Person of "+a.Address); err != nil {
			t.Fatal(err)
		}
	}
	run, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { alice.Run(run, client.RunOptions{}); close(done) }()
	defer func() { stop(); <-done }()
	var conv string
	for deadline := time.Now().Add(20 * time.Second); conv == ""; time.Sleep(50 * time.Millisecond) {
		c, err := bob.CreateDM(ctx, alice.Address)
		if err == nil {
			conv = c
		} else if time.Now().After(deadline) {
			t.Fatalf("no DM: %v", err)
		}
	}
	release := make(chan struct{})
	var once sync.Once
	var held atomic.Int64
	free := func() {
		once.Do(func() {
			hub.SetHold(nil)
			close(release)
		})
	}
	defer free()
	hub.SetHold(func(r *http.Request) {
		if r.Header.Get(protocol.HeaderAgent) == bob.Address && strings.HasPrefix(r.URL.Path, "/v1/persons/") {
			held.Add(1)
			<-release
		}
	})

	id, started := protocol.NewID(), time.Now()
	sent, err := live.SendDM(DMDraft{ID: id, Conv: conv, Body: "kept through a stall"})
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(started); took >= time.Second {
		t.Fatalf("the composer waited %v for a stalled request", took)
	}
	if sent.State != "queued" || sent.LID != id {
		t.Fatalf("not accepted as queued: %+v", sent)
	}
	rows := queuedRowsOf(t, live, conv, id)
	if len(rows) != 1 || rows[0].QueuedSend == nil || rows[0].Body != "kept through a stall" {
		t.Fatalf("no queued row in the chat: %+v", rows)
	}
	for _, words := range []string{rows[0].StateText, rows[0].Detail} {
		if strings.Contains(words, "/v1/") || strings.Contains(words, "deadline") {
			t.Fatalf("request text in the chat: %q", words)
		}
	}
	// Send again while the first attempt is still out: the same message.
	again, err := live.SendDM(DMDraft{ID: id, Conv: conv, Body: "kept through a stall"})
	if err != nil {
		t.Fatalf("repeat: %v", err)
	}
	if again.LID != id {
		t.Fatalf("repeat became another message: %+v", again)
	}
	if rows = queuedRowsOf(t, live, conv, id); len(rows) != 1 {
		t.Fatalf("repeat made %d rows", len(rows))
	}

	// One that is cancelled by the person never goes out.
	gone := protocol.NewID()
	if _, err = live.SendDM(DMDraft{ID: gone, Conv: conv, Body: "changed my mind"}); err != nil {
		t.Fatal(err)
	}
	if _, err = live.ChangeSchedule(ctx, "discard_send", ScheduleDraft{ID: gone}); err != nil {
		t.Fatal(err)
	}
	if rows = queuedRowsOf(t, live, conv, gone); len(rows) != 0 {
		t.Fatalf("a cancelled message stays in the chat: %+v", rows)
	}

	if rows = queuedRowsOf(t, live, conv, id); len(rows) != 1 || rows[0].QueuedSend == nil {
		t.Fatalf("not waiting on the stall: %+v", rows)
	}
	if held.Load() == 0 {
		t.Fatal("no request ever hit the stall")
	}
	free()
	var stored DMMessage
	for deadline := time.Now().Add(60 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if rows = queuedRowsOf(t, live, conv, id); len(rows) == 1 && rows[0].QueuedSend == nil {
			stored = rows[0]
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("never stored after the stall: %+v", rows)
		}
	}
	if stored.Body != "kept through a stall" {
		t.Fatalf("stored %+v", stored)
	}
	if rows = queuedRowsOf(t, live, conv, gone); len(rows) != 0 {
		t.Fatalf("a cancelled message went out: %+v", rows)
	}
	// What the peer gets is one copy of the one message.
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		msgs, _ := alice.ConversationMessages(conv)
		kept, cancelled := 0, 0
		for _, m := range msgs {
			switch m.Body {
			case "kept through a stall":
				kept++
			case "changed my mind":
				cancelled++
			}
		}
		if cancelled != 0 || kept > 1 {
			t.Fatalf("peer got %d kept and %d cancelled", kept, cancelled)
		}
		if kept == 1 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the peer never got it")
		}
	}
}

func TestSentenceKeepsRequestTextOutAndStaysTrue(t *testing.T) {
	deadline := &url.Error{Op: "Get", URL: "https://hub.example/v1/persons/abc/chain?after=1", Err: context.DeadlineExceeded}
	for _, err := range []error{deadline, context.DeadlineExceeded, errors.Join(errors.New("post"), deadline), &client.HubError{Status: 502, Msg: "bad gateway"}} {
		got := sentence(err)
		if strings.Contains(got, "http") || strings.Contains(got, "deadline") || strings.Contains(got, "/v1/") || strings.Contains(got, "bad gateway") {
			t.Fatalf("%v became %q", err, got)
		}
		if strings.Contains(got, "in time") {
			t.Fatalf("%v claims a timeout it may not have been: %q", err, got)
		}
	}
	if got := sentence(&client.HubError{Status: http.StatusUpgradeRequired, Code: "update_required"}); !strings.Contains(got, "latest AgentNet") {
		t.Fatalf("a device that must update reads %q", got)
	}
	if got := sentence(errors.New("cannot be retried: this person left")); got != "This person left" {
		t.Fatalf("a real reason lost: %q", got)
	}
}

// A send stored between the thread's two reads is in both: one row, the stored one.
func TestQueuedRowStoredMeanwhileIsShownOnce(t *testing.T) {
	at := time.Now()
	rows := []DMMessage{{ID: "copy", LID: "lid", Dir: "out", Body: "stored", At: at}}
	got := withQueuedSends(rows, []client.ScheduledSend{{ID: "lid", Body: "stored", Created: at}, {ID: "other", Body: "waits", Created: at}}, "me")
	if len(got) != 2 || got[0].ID != "copy" || got[1].ID != "other" {
		t.Fatalf("rows: %+v", got)
	}
}

// queuedSendWorld is bob's app with a DM to alice, and a second connection to its
// store for what only a failed attempt or another device would write.
func queuedSendWorld(t *testing.T) (*client.Agent, *Live, *testhub.Proc, string, *sql.DB) {
	t.Helper()
	home := t.TempDir()
	alice, bob, live, hub := liveWorldHubAt(t, home)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	for _, a := range []*client.Agent{alice, bob} {
		if _, err := a.CreatePerson(ctx, "Person of "+a.Address); err != nil {
			t.Fatal(err)
		}
	}
	runDaemon(t, alice)
	var conv string
	for deadline := time.Now().Add(20 * time.Second); conv == ""; time.Sleep(50 * time.Millisecond) {
		c, err := bob.CreateDM(ctx, alice.Address)
		if err == nil {
			conv = c
		} else if time.Now().After(deadline) {
			t.Fatalf("no DM: %v", err)
		}
	}
	side, err := sql.Open("sqlite", "file:"+filepath.Join(home, "agent.db")+"?_pragma=busy_timeout(10000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { side.Close() })
	return bob, live, hub, conv, side
}

// Discard must never first approve the message again: retrying it read the
// current audience as approved and started its sender, which could win.
func TestDiscardingAStoppedMessageDoesNotRetryIt(t *testing.T) {
	bob, live, hub, conv, side := queuedSendWorld(t)
	release := make(chan struct{})
	defer close(release)
	hub.SetHold(func(r *http.Request) {
		if r.Header.Get(protocol.HeaderAgent) == bob.Address && strings.HasPrefix(r.URL.Path, "/v1/persons/") {
			<-release
		}
	})
	defer hub.SetHold(nil)
	id := protocol.NewID()
	if _, err := live.SendDM(DMDraft{ID: id, Conv: conv, Body: "not after all"}); err != nil {
		t.Fatal(err)
	}
	if _, err := side.Exec(`UPDATE scheduled_sends SET state='needs_review',detail='Stopped.' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	stopped, _, err := bob.ScheduledSend(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = live.ChangeSchedule(context.Background(), "discard_send", ScheduleDraft{ID: id}); err != nil {
		t.Fatal(err)
	}
	gone, _, err := bob.ScheduledSend(id)
	if err != nil {
		t.Fatal(err)
	}
	if gone.State != client.ScheduledCancelled || gone.Rev != stopped.Rev+1 {
		t.Fatalf("discard did more than discard: revision %d to %d, state %s", stopped.Rev, gone.Rev, gone.State)
	}
}

// A refused message stays where it was sent, with its words, whatever the timing:
// its text was durable before anything could refuse it, and handing it back to
// the composer alone could lose it with a lost answer.
func TestARefusedMessageStaysInItsPlace(t *testing.T) {
	_, live, _, conv, side := queuedSendWorld(t)
	if _, err := side.Exec(`UPDATE persons SET state='conflict' WHERE state<>'self'`); err != nil {
		t.Fatal(err)
	}
	id := protocol.NewID()
	sent, err := live.SendDM(DMDraft{ID: id, Conv: conv, Body: "kept when refused"})
	if err != nil {
		t.Fatalf("the send was refused with no record left: %v", err)
	}
	if sent.LID != id {
		t.Fatalf("became another message: %+v", sent)
	}
	var row DMMessage
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		rows := queuedRowsOf(t, live, conv, id)
		if len(rows) == 1 && rows[0].State == "failed" {
			row = rows[0]
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not stopped in its chat: %+v", rows)
		}
	}
	if row.Body != "kept when refused" || row.QueuedSend == nil || row.StateText == "" {
		t.Fatalf("content or reason lost: %+v", row)
	}
	for _, words := range []string{row.StateText, row.Detail} {
		if strings.Contains(words, "/v1/") || strings.Contains(words, "deadline") {
			t.Fatalf("request text in the chat: %q", words)
		}
	}
	// Send again: the same message, still one row.
	if _, err = live.SendDM(DMDraft{ID: id, Conv: conv, Body: "kept when refused"}); err != nil {
		t.Fatalf("repeat: %v", err)
	}
	if rows := queuedRowsOf(t, live, conv, id); len(rows) != 1 {
		t.Fatalf("repeat made %d rows", len(rows))
	}
}

// storedSend waits for an accepted message to be stored for sending and answers
// with it as stored. SendDM stays 250 ms to hear the first attempt; past that it
// answers with the queued row's id, which the page follows by LID, and a slower
// or race-built run gets that answer too. A test that goes on to act on the stored
// message waits for it here, as the page does; the 250 ms path itself is unchanged.
func storedSend(t *testing.T, l *Live, conv string, sent Sent) Sent {
	t.Helper()
	if sent.LID == "" {
		t.Fatalf("send has no LID: %+v", sent)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		copies, err := l.a.SentCopies(sent.LID)
		if err != nil {
			t.Fatal(err)
		}
		if len(copies) > 0 {
			sent.ID, sent.State = copies[0].ID, copies[0].State
			return sent
		}
		if time.Now().After(deadline) {
			t.Fatalf("message %s in %s was not stored: %+v", sent.LID, conv, queuedRowsOf(t, l, conv, sent.LID))
		}
		time.Sleep(20 * time.Millisecond)
	}
}
