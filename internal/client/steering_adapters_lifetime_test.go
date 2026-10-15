package client

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

func TestClaudeStreamBlockedSteerCancelJoins(t *testing.T) {
	b, _, _ := claudeAdapterFixture(t)
	// Install the pending write synchronously, then block it in the real pipe.
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	b.mu.Lock()
	p := &piSteer{ctx: ctx, text: strings.Repeat("x", 1<<20), result: make(chan string, 1)}
	b.pending["blocked"] = p
	b.writes.Add(1)
	b.mu.Unlock()
	sent := make(chan struct{})
	go func() {
		defer b.writes.Done()
		defer close(sent)
		_ = b.send(ctx, "blocked", p, map[string]string{"type": "user", "text": p.text})
	}()
	cancel()
	done := make(chan struct{})
	go func() { b.close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("blocked native write not joined")
	}
	<-sent
}
func TestNativeAdapterCanceledReceiptCannotBecomeAccepted(t *testing.T) {
	for _, name := range []string{"claude", "omp"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			p := &piSteer{ctx: ctx, text: "Correction", result: make(chan string, 1)}
			cancel()
			if name == "claude" {
				b, _, _ := claudeAdapterFixture(t)
				b.pending["canceled"] = p
				claudeAdapterEvent(b, claudeAdapterUser("Correction"))
				if _, ok := b.seen["canceled"]; ok {
					t.Fatal("canceled replay accepted")
				}
			} else {
				b, _, _ := piBridgeFixture(t)
				b.omp = true
				b.pending["canceled"] = p
				piEvent(b, map[string]any{"type": "message_start", "messageId": "native", "message": map[string]any{"role": "user", "steering": true, "attribution": "user", "content": "Correction"}})
				if _, ok := b.seen["canceled"]; ok {
					t.Fatal("canceled receipt accepted")
				}
			}
			if r := <-p.result; r != steerUnknown {
				t.Fatal(r)
			}
		})
	}
}
func TestNativeAdapterCanceledOwnerCannotAcceptOrPublish(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	in, out := io.Pipe()
	defer in.Close()
	cancel()
	b := &claudeStreamBridge{ctx: ctx, input: out, output: io.Discard, ready: make(chan struct{}), active: true, session: "owned", pending: map[string]*piSteer{}}
	claudeAdapterEvent(b, map[string]any{"type": "result", "session_id": "owned", "result": "must not publish"})
	if b.answer != "" || !b.done {
		t.Fatal("canceled owner used result")
	}
}
func TestOMPRPCOriginalEndCannotReopenSteering(t *testing.T) {
	b, _, _ := piBridgeFixture(t)
	b.omp = true
	b.event([]byte(`{"type":"agent_end"}`))
	b.event([]byte(`{"type":"agent_start"}`))
	if r := b.steer(t.Context(), "late", "Late"); r != steerQueued {
		t.Fatal("reopened native end", r)
	}
}
func TestNativeAdapterStreamChunking(t *testing.T) {
	b, _, out := claudeAdapterFixture(t)
	row, _ := json.Marshal(map[string]any{"type": "result", "session_id": "owned", "result": "Complete"})
	row = append(row, '\n')
	for _, c := range row {
		if _, e := b.Write([]byte{c}); e != nil {
			t.Fatal(e)
		}
	}
	if out.String() != "Complete\n" {
		t.Fatal(out.String())
	}
}
func TestNativeAdapterReplacedClaimCannotWrite(t *testing.T) {
	for _, name := range []string{"claude", "omp"} {
		t.Run(name, func(t *testing.T) {
			old := &piSteer{text: "old attempt", result: make(chan string, 1)}
			next := &piSteer{text: "new attempt", result: make(chan string, 1)}
			var sink bytes.Buffer
			var send func() error
			var unlock, replace, closeBridge func()
			if name == "claude" {
				b := &claudeStreamBridge{ctx: t.Context(), input: adapterWriteCloser{&sink}, output: io.Discard, ready: make(chan struct{}), active: true, pending: map[string]*piSteer{"same-id": old}}
				b.writeMu.Lock()
				unlock = b.writeMu.Unlock
				send = func() error { return b.send(context.Background(), "same-id", old, map[string]string{"type": "user"}) }
				replace = func() { b.mu.Lock(); b.pending["same-id"] = next; b.mu.Unlock() }
				closeBridge = b.close
			} else {
				b := &piRPCBridge{ctx: t.Context(), input: adapterWriteCloser{&sink}, output: io.Discard, ready: make(chan struct{}), active: true, omp: true, pending: map[string]*piSteer{"same-id": old}}
				b.writeMu.Lock()
				unlock = b.writeMu.Unlock
				send = func() error { return b.send(context.Background(), "same-id", old, map[string]string{"type": "steer"}) }
				replace = func() { b.mu.Lock(); b.pending["same-id"] = next; b.mu.Unlock() }
				closeBridge = b.close
			}
			done := make(chan error, 1)
			go func() { done <- send() }()
			replace()
			unlock()
			if e := <-done; e == nil {
				t.Fatal("old pending claim wrote into replacement")
			}
			closeBridge()
			if sink.Len() != 0 {
				t.Fatal("stale input written")
			}
		})
	}
}
