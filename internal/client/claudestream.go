package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
)

// claudeStreamBridge owns one fresh native process and one initial request. Native
// settings/extensions remain the owner's; no interactive session is attached.
type claudeStreamBridge struct {
	writes               sync.WaitGroup
	ctx                  context.Context
	input                io.WriteCloser
	output               io.Writer
	ready                chan struct{}
	readyOnce, closeOnce sync.Once
	mu, writeMu, readMu  sync.Mutex
	buf                  []byte
	active, done, failed bool
	prompt, session      string
	closed               chan struct{}
	answer               string
	pending              map[string]*piSteer
	seen                 map[string]string
}

func newClaudeStreamBridge(ctx context.Context, cmd *exec.Cmd, prompt string, output io.Writer, expected string) (*claudeStreamBridge, error) {
	cmd.Stdin = nil
	input, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	b := &claudeStreamBridge{ctx: ctx, input: input, output: output, ready: make(chan struct{}), pending: map[string]*piSteer{}, seen: map[string]string{}, closed: make(chan struct{}), prompt: prompt, session: expected}
	for i := 1; i+1 < len(cmd.Args); i++ {
		if cmd.Args[i] == "--output-format" {
			cmd.Args[i+1] = "stream-json"
		}
	}
	cmd.Args = append(cmd.Args, "--input-format", "stream-json", "--verbose", "--replay-user-messages")
	cmd.Stdout = b
	b.writes.Add(1)
	go func() {
		defer b.writes.Done()
		if b.send(ctx, "", nil, map[string]any{"type": "user", "message": map[string]string{"role": "user", "content": prompt}}) != nil {
			b.fail("Claude native input failed; review this request before continuing")
		}
	}()
	go func() {
		select {
		case <-ctx.Done():
			b.close()
		case <-b.closed:
		}
	}()
	return b, nil
}
func (b *claudeStreamBridge) followupReady() <-chan struct{} { return b.ready }
func (b *claudeStreamBridge) send(ctx context.Context, id string, pending *piSteer, v any) error {
	b.writeMu.Lock()
	defer b.writeMu.Unlock()
	b.mu.Lock()
	if b.done || b.ctx.Err() != nil || ctx.Err() != nil || id != "" && (!b.active || b.pending[id] == nil || b.pending[id] != pending) {
		b.mu.Unlock()
		return context.Canceled
	}
	b.mu.Unlock()
	return json.NewEncoder(b.input).Encode(v)
}
func (b *claudeStreamBridge) close() {
	b.closeOnce.Do(func() {
		b.mu.Lock()
		b.endLocked()
		b.mu.Unlock()
		// CommandContext owns native process cancellation. Close the pipe directly:
		// taking writeMu first could deadlock behind a blocked startup/steer write.
		_ = b.input.Close()
		b.writes.Wait()
		if b.closed != nil {
			close(b.closed)
		}
	})
}
func (b *claudeStreamBridge) fail(why string) { b.mu.Lock(); defer b.mu.Unlock(); b.failLocked(why) }
func (b *claudeStreamBridge) failLocked(why string) {
	if b.done && !b.failed {
		return
	}
	if !b.failed {
		b.failed = true
		fmt.Fprintln(b.output, needsHumanMarker)
		fmt.Fprintln(b.output, why)
	}
	b.endLocked()
	_ = b.input.Close()
}
func (b *claudeStreamBridge) endLocked() {
	b.done = true
	b.active = false
	for id, p := range b.pending {
		p.result <- steerUnknown
		delete(b.pending, id)
	}
}
func (b *claudeStreamBridge) finish() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.done {
		b.failLocked("Claude ended before its request settled; review before continuing")
	}
}
func (b *claudeStreamBridge) Write(data []byte) (int, error) {
	b.readMu.Lock()
	defer b.readMu.Unlock()
	for rest := data; len(rest) > 0; {
		i := bytes.IndexByte(rest, '\n')
		n := len(rest)
		if i >= 0 {
			n = i
		}
		if len(b.buf)+n > codexLineMax {
			b.buf = nil
			b.fail("Native event framing exceeded its bound")
			return len(data), nil
		}
		b.buf = append(b.buf, rest[:n]...)
		if i < 0 {
			break
		}
		b.event(bytes.TrimSuffix(b.buf, []byte{'\r'}))
		b.buf = nil
		rest = rest[i+1:]
	}

	return len(data), nil
}

// The same owned stream must replay the exact correlated user input before
// the original result. A write or assistant echo is never consumption evidence.
func (b *claudeStreamBridge) event(raw []byte) {
	var e struct {
		Type    string          `json:"type"`
		Subtype string          `json:"subtype"`
		Session string          `json:"session_id"`
		UUID    string          `json:"uuid"`
		Replay  bool            `json:"isReplay"`
		Parent  json.RawMessage `json:"parent_tool_use_id"`
		Message struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"message"`
		Result  string            `json:"result"`
		IsError bool              `json:"is_error"`
		Denials []json.RawMessage `json:"permission_denials"`
	}
	if json.Unmarshal(raw, &e) != nil {
		b.fail("Claude emitted invalid native protocol; no answer was used")
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.done {
		return
	}
	if b.ctx.Err() != nil {
		b.endLocked()
		_ = b.input.Close()
		return
	}
	if e.Session != "" {
		if b.session != "" && b.session != e.Session {
			b.failLocked("Claude changed the owned native session")
			return
		}
		b.session = e.Session
	}
	switch e.Type {
	case "user":
		var text string
		if b.session == "" || e.Session != b.session || e.UUID == "" || !e.Replay || string(e.Parent) != "null" || e.Message.Role != "user" || json.Unmarshal(e.Message.Content, &text) != nil {
			return
		}
		if !b.active && text == b.prompt {
			b.active = true
			b.readyOnce.Do(func() { close(b.ready) })
			return
		}
		if !b.active {
			return
		}
		for id, p := range b.pending {
			if text == p.text {
				if p.ctx != nil && p.ctx.Err() != nil {
					p.result <- steerUnknown
					delete(b.pending, id)
					continue
				}
				p.result <- steerAccepted
				b.seen[id] = text
				delete(b.pending, id)
			}
		}
	case "result":
		if e.Session == "" || e.Session != b.session || e.IsError || len(e.Denials) > 0 {
			b.failLocked("Claude's native provider or permission setup stopped this request; review locally before continuing")
			return
		}
		if len(e.Result) > maxOutput+1 {
			e.Result = e.Result[:maxOutput+1]
		}
		b.answer = e.Result
		b.publishLocked()
	case "control_request":
		b.failLocked("Claude needs native approval or input; no response or permission override was invented")
	case "system":
		if e.Subtype == "permission_denied" {
			b.failLocked("Claude could not obtain native permission; review locally before continuing")
		}
	}
}
func (b *claudeStreamBridge) publishLocked() {
	if strings.TrimSpace(b.answer) == "" {
		b.failLocked("Native request settled without an assistant answer")
		return
	}
	fmt.Fprintln(b.output, b.answer)
	b.endLocked()
	_ = b.input.Close()
}
func (b *claudeStreamBridge) steer(ctx context.Context, id, text string) string {
	b.mu.Lock()
	if b.done || !b.active || b.ctx.Err() != nil || ctx.Err() != nil || id == "" || text == "" {
		b.mu.Unlock()
		return steerQueued
	}
	if prior, ok := b.seen[id]; ok {
		b.mu.Unlock()
		if prior == text {
			return steerAccepted
		}
		return steerUnknown
	}
	if _, ok := b.pending[id]; ok {
		b.mu.Unlock()
		return steerUnknown
	}
	p := &piSteer{ctx: ctx, text: text, result: make(chan string, 1)}
	b.pending[id] = p
	b.writes.Add(1)
	b.mu.Unlock()
	go func() {
		defer b.writes.Done()
		if b.send(ctx, id, p, map[string]any{"type": "user", "message": map[string]string{"role": "user", "content": text}}) != nil {
			b.mu.Lock()
			if b.pending[id] == p {
				delete(b.pending, id)
				p.result <- steerUnknown
			}
			b.mu.Unlock()
		}
	}()
	select {
	case result := <-p.result:
		return result
	case <-ctx.Done():
	case <-b.ctx.Done():
	}
	b.mu.Lock()
	delete(b.pending, id)
	b.mu.Unlock()
	return steerUnknown
}
