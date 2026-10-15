package client

import (
	"bytes"
	"context"
	"runtime"
	"testing"
	"time"
)

type piFenceInput struct{ bytes.Buffer }

func (*piFenceInput) Close() error { return nil }
func TestPiRPCQueuedWriterRechecksBeforeHandover(t *testing.T) {
	for _, mode := range []string{"correction-cancelled", "native-ended"} {
		t.Run(mode, func(t *testing.T) {
			input := new(piFenceInput)
			b := &piRPCBridge{ctx: t.Context(), input: input, output: new(bytes.Buffer), active: true, pending: map[string]*piSteer{}, seen: map[string]string{}}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			b.writeMu.Lock()
			done := make(chan string, 1)
			go func() { done <- b.steer(ctx, "blocked", "late correction") }()
			deadline := time.Now().Add(time.Second)
			for {
				b.mu.Lock()
				pending := b.pending["blocked"] != nil
				b.mu.Unlock()
				if pending {
					break
				}
				if time.Now().After(deadline) {
					b.writeMu.Unlock()
					t.Fatal("no pending correction")
				}
				runtime.Gosched()
			}
			if mode == "correction-cancelled" {
				cancel()
			} else {
				b.event([]byte(`{"type":"agent_end"}`))
			}
			select {
			case result := <-done:
				if result != steerUnknown {
					t.Fatal(result)
				}
			case <-time.After(time.Second):
				b.writeMu.Unlock()
				t.Fatal("correction did not settle")
			}
			b.writeMu.Unlock()
			b.writes.Wait()
			b.close()
			if input.Len() != 0 {
				t.Fatal("stale queued write reached native input")
			}
		})
	}
}
