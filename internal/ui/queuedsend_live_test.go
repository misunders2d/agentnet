package ui

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
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
