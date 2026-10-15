package client

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func piBridgeFixture(t *testing.T) (*piRPCBridge, *json.Decoder, *bytes.Buffer) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	in, out := io.Pipe()
	output := new(bytes.Buffer)
	b := &piRPCBridge{ctx: ctx, input: out, output: output, ready: make(chan struct{}), pending: map[string]*piSteer{}, seen: map[string]string{}}
	t.Cleanup(func() { cancel(); in.Close(); out.Close() })
	b.event([]byte(`{"type":"agent_start"}`))
	return b, json.NewDecoder(in), output
}
func piEvent(b *piRPCBridge, v any) { raw, _ := json.Marshal(v); b.event(raw) }
func piSteerStart(t *testing.T, b *piRPCBridge, d *json.Decoder) (<-chan string, map[string]any) {
	t.Helper()
	done := make(chan string, 1)
	go func() { done <- b.steer(t.Context(), "correction", "Use English") }()
	var req map[string]any
	if err := d.Decode(&req); err != nil {
		t.Fatal(err)
	}
	if req["type"] != "steer" || req["id"] != "correction" {
		t.Fatal(req)
	}
	return done, req
}
func TestPiRPCConsumptionBeforeCompletion(t *testing.T) {
	b, d, out := piBridgeFixture(t)
	done, req := piSteerStart(t, b, d)
	piEvent(b, map[string]any{"type": "response", "id": req["id"], "command": "steer", "success": true, "data": map[string]string{"disposition": "queued"}})
	select {
	case r := <-done:
		t.Fatal("queued ACK reported application", r)
	default:
	}
	piEvent(b, map[string]any{"type": "message_start", "message": map[string]string{"role": "user", "content": "Different message"}})
	select {
	case r := <-done:
		t.Fatal("unrelated user event accepted correction", r)
	default:
	}
	piEvent(b, map[string]any{"type": "message_start", "message": map[string]string{"role": "user", "content": "Use English"}})
	if r := <-done; r != steerAccepted {
		t.Fatal(r)
	}
	if r := b.steer(t.Context(), "correction", "Use English"); r != steerAccepted {
		t.Fatal("duplicate changed proven result", r)
	}
	if r := b.steer(t.Context(), "correction", "Changed correction"); r != steerUnknown {
		t.Fatal("id reused with changed content", r)
	}
	piEvent(b, map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant", "content": []any{map[string]string{"type": "toolCall", "text": "not a final"}}, "stopReason": "toolUse"}})
	piEvent(b, map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant", "content": []any{map[string]string{"type": "text", "text": "Corrected result"}}, "stopReason": "stop"}})
	if out.Len() != 0 {
		t.Fatal("result exposed before settled")
	}
	b.event([]byte(`{"type":"agent_end"}`))
	if r := b.steer(t.Context(), "late", "Late"); r != steerQueued {
		t.Fatal(r)
	}
	b.event([]byte(`{"type":"agent_settled","aborted":false}`))
	b.finish()
	if strings.TrimSpace(out.String()) != "Corrected result" {
		t.Fatal(out.String())
	}
}
func TestPiRPCUnknownRejectedAndCancelled(t *testing.T) {
	for _, mode := range []string{"rejected", "handled", "ended", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			b, d, _ := piBridgeFixture(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan string, 1)
			go func() { done <- b.steer(ctx, "correction", "Use English") }()
			var req map[string]any
			if err := d.Decode(&req); err != nil {
				t.Fatal(err)
			}
			expected := steerUnknown
			switch mode {
			case "rejected":
				expected = steerUnsupported
				piEvent(b, map[string]any{"type": "response", "id": "correction", "success": false})
			case "handled":
				piEvent(b, map[string]any{"type": "response", "id": "correction", "success": true, "data": map[string]string{"disposition": "handled"}})
			case "ended":
				b.event([]byte(`{"type":"agent_end"}`))
			case "cancelled":
				cancel()
			}
			select {
			case r := <-done:
				if r != expected {
					t.Fatal(r, expected)
				}
			case <-time.After(time.Second):
				t.Fatal("steer did not settle")
			}
			b.event([]byte(`{"type":"message_start","message":{"role":"user","content":"Use English"}}`))
			b.mu.Lock()
			_, accepted := b.seen["correction"]
			b.mu.Unlock()
			if accepted {
				t.Fatal("late event retroactively accepted uncertain input")
			}
		})
	}
}
func TestPiRPCExtensionDialogNeedsHumanAndCancelOrder(t *testing.T) {
	t.Run("native-dialog", func(t *testing.T) {
		b, _, out := piBridgeFixture(t)
		b.event([]byte(`{"type":"extension_ui_request","method":"confirm","id":"approval"}`))
		if !strings.HasPrefix(out.String(), needsHumanMarker) {
			t.Fatal(out.String())
		}
	})
	t.Run("cancel-closes-input", func(t *testing.T) {
		b, d, _ := piBridgeFixture(t)
		b.close()
		var req map[string]any
		if err := d.Decode(&req); err != io.EOF {
			t.Fatalf("cancel input: %v", err)
		}
	})
}
func TestPiRPCFailedStartClosesAndJoinsBlockedInput(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(t.TempDir(), "missing-native-binary"))
	b, err := newPiRPCBridge(ctx, cmd, strings.Repeat("x", 1024*1024), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err == nil {
		t.Fatal("missing binary started")
	}
	done := make(chan struct{})
	go func() { b.close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("failed start leaked owned input")
	}
}
func TestPiRPCCancelClosesAndJoinsBlockedSteer(t *testing.T) {
	b, _, _ := piBridgeFixture(t)
	done := make(chan string, 1)
	go func() { done <- b.steer(t.Context(), "blocked", strings.Repeat("x", 1024*1024)) }()
	for {
		b.mu.Lock()
		pending := len(b.pending) > 0
		b.mu.Unlock()
		if pending {
			break
		}
		select {
		case <-t.Context().Done():
			t.Fatal("no pending write")
		default:
		}
	}
	b.close()
	select {
	case r := <-done:
		if r != steerUnknown {
			t.Fatal(r)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked steer leaked")
	}
}
func TestPiRPCLongToolUsesOwnedContext(t *testing.T) {
	if testing.Short() {
		t.Skip("wait beyond former 30s deadline")
	}
	b, d, _ := piBridgeFixture(t)
	done, req := piSteerStart(t, b, d)
	piEvent(b, map[string]any{"type": "response", "id": req["id"], "success": true, "data": map[string]string{"disposition": "queued"}})
	select {
	case r := <-done:
		t.Fatal("tool wait cut short", r)
	case <-time.After(31 * time.Second):
	}
	piEvent(b, map[string]any{"type": "message_start", "message": map[string]string{"role": "user", "content": "Use English"}})
	if r := <-done; r != steerAccepted {
		t.Fatal(r)
	}
}
