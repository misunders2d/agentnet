//go:build linux

package client

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// TestCodexDesktopLive drives the real installed Codex Desktop (MEL-830):
// AGENTNET_LIVE_CODEX_DESKTOP_THREAD names a chat open in a running,
// signed-in Desktop whose Codex home is CODEX_HOME. Bob's own linked
// device (alice's key in bob's signed roster) asks the connected chat
// agent; the driver that runs Desktop clicks Deny on native prompts and
// closes Desktop when this prints LIVE-CLOSE-DESKTOP.
func TestCodexDesktopLive(t *testing.T) {
	thread := os.Getenv("AGENTNET_LIVE_CODEX_DESKTOP_THREAD")
	if thread == "" {
		t.Skip("opt-in: AGENTNET_LIVE_CODEX_DESKTOP_THREAD=<open Codex Desktop thread>")
	}
	w := newWorld(t, "")
	runAgent(t, w.alice)
	label, name, _ := protocol.SplitAddress(w.alice.Address)
	var prof protocol.Profile
	eventually(t, "alice session published", func() bool {
		err := w.bob.hub.do(tctx(t), "GET", "/v1/agents/"+label+"/"+name+"/profile", nil, &prof)
		return err == nil && len(prof.Sessions) == 1
	})
	caps := protocol.CapsRecord{Address: w.alice.Address, Session: prof.Sessions[0], Caps: []string{protocol.CapAgentIdentity}, TS: time.Now().Unix() + 100}
	caps.Sign(w.alice.id.Sign)
	if err := w.alice.hub.do(tctx(t), "PUT", "/v1/caps", caps, nil); err != nil {
		t.Fatal(err)
	}
	if err := w.bob.Approve(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	chatOwnDevice(t, w)
	home, _ := codexHomeDir()
	engine, err := codexDesktopEngine(home)
	if err != nil {
		t.Fatal(err)
	}
	ver, _ := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", engine.PID))
	fmt.Printf("LIVE engine pid=%d exe=%s args=%q\n", engine.PID, engine.Exe, strings.ReplaceAll(string(ver), "\x00", " "))
	agentID, err := w.bob.ConnectCodexChat(thread, "Codex Desktop")
	if err != nil {
		t.Fatal(err)
	}
	ready, err := w.bob.readiness(false)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("LIVE readiness %+v\n", ready)

	ask := func(body string) (string, Message) {
		in := envelope.Inner{ID: protocol.NewID(), From: w.alice.Address, To: w.bob.Address, TS: time.Now().Unix(), Kind: envelope.KindQuestion, Body: body,
			Target: &envelope.Target{Address: w.bob.Address, Fingerprint: w.bob.Self().Fingerprint(), AgentID: agentID}}
		if err := w.alice.store.addOutbox(envelope.Envelope{ID: in.ID, From: in.From, To: in.To}, in, "", nil); err != nil {
			t.Fatal(err)
		}
		if err := w.bob.store.addInbox(in, w.alice.Self().Fingerprint()); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		done := make(chan struct{})
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		go func() { defer close(done); w.bob.runNext(ctx, nil) }()
		select {
		case <-done:
		case <-time.After(5 * time.Minute):
			t.Fatal("no end within 5 minutes")
		}
		row := inboxRow(t, w.bob, in.ID)
		answer := ""
		if row.State == stateAnswered {
			var answerID string
			if err := w.bob.store.db.QueryRow(`SELECT result_id FROM inbox WHERE id=?`, in.ID).Scan(&answerID); err != nil {
				t.Fatal(err)
			}
			env, err := w.bob.store.outboxEnvelope(answerID)
			if err != nil {
				t.Fatal(err)
			}
			if err = w.alice.accept(tctx(t), env); err != nil {
				t.Fatal(err)
			}
			answer = inboxRow(t, w.alice, answerID).Body
		}
		led, _, _ := codexChatRowIn(w.bob.store.db, in.ID)
		fmt.Printf("LIVE request %s state=%s in %s ledger=%s turn=%s detail=%q answer=%q\n", in.ID, row.State, time.Since(start).Round(time.Millisecond), led.State, led.Turn, row.Detail, answer)
		return answer, row
	}
	token := func(p string) string { var b [4]byte; rand.Read(b[:]); return p + hex.EncodeToString(b[:]) }

	t1 := token("AN-LIVE-ONE-")
	if got, _ := ask("Reply with exactly " + t1 + " and nothing else. Do not use any tools."); !strings.Contains(got, t1) {
		t.Fatalf("first answer %q", got)
	}
	t2 := token("AN-LIVE-TWO-")
	if got, _ := ask("Reply with the exact token from your previous reply, then a space, then " + t2 + ", and nothing else. Do not use any tools."); !strings.Contains(got, t1+" "+t2) {
		t.Fatalf("follow-up answer %q", got)
	}
	t3 := token("AN-LIVE-APPROVAL-")
	got, row := ask(t3 + ". This is a native approval test. Call your native file-permission request tool to ask for read-only access to the single nonexistent file /opt/agentnet-approval-probe.txt. Make no shell calls and no file changes. If the request is denied or the tool is unavailable, say so plainly.")
	low := strings.ToLower(got)
	if row.State != stateAnswered || !(strings.Contains(low, "denied") || strings.Contains(low, "declined") || strings.Contains(low, "not granted") || strings.Contains(low, "aborted") || strings.Contains(low, "unavailable") || strings.Contains(low, "no read grant") || strings.Contains(low, "no grant") || strings.Contains(low, "not grant")) {
		t.Fatalf("approval answer %q (%s)", got, row.State)
	}
	if _, err := os.Stat("/opt/agentnet-approval-probe.txt"); err == nil {
		t.Fatal("probe file exists")
	}

	// Desktop restarts and the person does not open this chat again: a
	// request waits in its queue, unstarted, and leaves it at the deadline.
	fmt.Println("LIVE-RESTART-DESKTOP")
	restarted := time.Now().Add(90 * time.Second)
	for {
		if e, err := codexDesktopEngine(home); err == nil && e.PID != engine.PID {
			engine = e
			break
		}
		if time.Now().After(restarted) {
			t.Fatal("Desktop did not restart")
		}
		time.Sleep(300 * time.Millisecond)
	}
	fmt.Printf("LIVE restarted engine pid=%d\n", engine.PID)
	old := codexChatDeadline
	codexChatDeadline = 25 * time.Second
	if _, row = ask("Reply with exactly " + token("AN-LIVE-UNOPENED-") + "."); row.State != stateNotRun || !strings.Contains(row.Detail, "only for a chat it has opened") {
		t.Fatalf("unopened chat request %+v", row)
	}
	codexChatDeadline = old
	var left int
	if err := codexAppServer(tctx(t), engine.server(home), func(call func(string, any, any) error) error {
		var page struct {
			Data []codexQueueEntry `json:"data"`
		}
		err := call("thread/queue/list", map[string]any{"threadId": thread, "limit": 100}, &page)
		left = len(page.Data)
		return err
	}); err != nil || left != 0 {
		t.Fatalf("queue after removal: %d entries, %v", left, err)
	}

	fmt.Println("LIVE-CLOSE-DESKTOP")
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := codexDesktopEngine(home); err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Desktop did not close")
		}
		time.Sleep(200 * time.Millisecond)
	}
	ready, _ = w.bob.readiness(false)
	fmt.Printf("LIVE readiness after close %+v\n", ready)
	if _, row = ask("Reply with exactly " + token("AN-LIVE-CLOSED-") + "."); row.State != stateNotRun || !strings.Contains(row.Detail, "not running") {
		t.Fatalf("closed Desktop request %+v", row)
	}
	fmt.Println("LIVE-DONE")
}
