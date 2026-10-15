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

// piRPCBridge owns one fresh native process and one initial request. Native
// settings/extensions remain the owner's; no interactive session is attached.
type piRPCBridge struct {
	writes               sync.WaitGroup
	ctx                  context.Context
	input                io.WriteCloser
	output               io.Writer
	onActivity           func(nativeActivity)
	ready                chan struct{}
	readyOnce, closeOnce sync.Once
	mu, writeMu, readMu  sync.Mutex
	buf                  []byte
	active, done, failed bool
	omp, ended           bool
	closed               chan struct{}
	answer               string
	pending              map[string]*piSteer
	seen                 map[string]string
}
type piSteer struct {
	ctx    context.Context
	text   string
	result chan string
}

func newPiRPCBridge(ctx context.Context, cmd *exec.Cmd, prompt string, output io.Writer, harness ...string) (*piRPCBridge, error) {
	cmd.Stdin = nil
	input, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	b := &piRPCBridge{ctx: ctx, input: input, output: output, ready: make(chan struct{}), pending: map[string]*piSteer{}, seen: map[string]string{}, closed: make(chan struct{}), omp: len(harness) > 0 && harness[0] == "omp"}
	// Keep lookup extensions and the native launch setup; RPC replaces only print mode.
	cmd.Args = append(withoutFlags(cmd.Args, "-p"), "--mode", "rpc")
	cmd.Stdout = b
	b.writes.Add(1)
	go func() {
		defer b.writes.Done()
		if b.send(ctx, "", nil, map[string]any{"id": "agentnet-initial", "type": "prompt", "message": prompt}) != nil {
			b.fail(b.nativeName() + " native input failed; review this request before continuing")
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
func (b *piRPCBridge) followupReady() <-chan struct{} { return b.ready }
func (b *piRPCBridge) send(ctx context.Context, id string, pending *piSteer, v any) error {
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
func (b *piRPCBridge) close() {
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
func (b *piRPCBridge) fail(why string) { b.mu.Lock(); defer b.mu.Unlock(); b.failLocked(why) }
func (b *piRPCBridge) failLocked(why string) {
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
func (b *piRPCBridge) endLocked() {
	b.done = true
	b.active = false
	for id, p := range b.pending {
		p.result <- steerUnknown
		delete(b.pending, id)
	}
}
func (b *piRPCBridge) finish() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.done {
		b.failLocked(b.nativeName() + " ended before its request settled; review before continuing")
	}
}
func (b *piRPCBridge) Write(data []byte) (int, error) {
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
func piMessageText(raw json.RawMessage) (role, text, stop string, tool bool) {
	var m struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
		Stop    string          `json:"stopReason"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return
	}
	role, stop = m.Role, m.Stop
	if json.Unmarshal(m.Content, &text) == nil {
		return
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(m.Content, &blocks) != nil {
		return
	}
	for _, v := range blocks {
		if v.Type == "text" {
			text += v.Text
		}
		if v.Type == "toolCall" {
			tool = true
		}
	}
	return
}
func (b *piRPCBridge) event(raw []byte) {
	var e struct {
		Type                  string          `json:"type"`
		ID                    string          `json:"id"`
		Command               string          `json:"command"`
		Success               bool            `json:"success"`
		Method                string          `json:"method"`
		ToolName              json.RawMessage `json:"toolName"`
		AssistantMessageEvent json.RawMessage `json:"assistantMessageEvent"`
		Message               json.RawMessage `json:"message"`
		Data                  struct {
			Disposition string `json:"disposition"`
		} `json:"data"`
		Aborted   bool   `json:"aborted"`
		MessageID string `json:"messageId"`
		Status    string `json:"status"`
		Settled   bool   `json:"sessionSettled"`
	}
	if json.Unmarshal(raw, &e) != nil {
		b.fail(b.nativeName() + " emitted invalid native protocol; no answer was used")
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
	switch e.Type {
	case "tool_execution_start":
		if b.onActivity != nil && b.active {
			var name string
			if json.Unmarshal(e.ToolName, &name) == nil {
				if k := nativeToolActivity(name); k != activityUnknown {
					b.onActivity(k)
				}
			}
		}
	case "tool_execution_end":
		if b.onActivity != nil && b.active {
			b.onActivity(activityWorking)
		}
	case "message_update":
		if b.onActivity != nil && b.active {
			var event struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(e.AssistantMessageEvent, &event) == nil && event.Type == "text_start" {
				b.onActivity(activityAnswer)
			}
		}
	case "agent_start":
		if b.omp && b.ended {
			return
		}
		b.active = true
		b.readyOnce.Do(func() { close(b.ready) })
	case "agent_end":
		if b.omp {
			_ = b.input.Close()
		}
		b.ended = true
		b.active = false
		// A late native queue response never establishes consumption.
		for id, p := range b.pending {
			p.result <- steerUnknown
			delete(b.pending, id)
		}
	case "response":
		if e.ID == "agentnet-initial" && !e.Success {
			b.failLocked(b.nativeName() + " refused the initial request; native setup remains unchanged")
			return
		}
		if p, ok := b.pending[e.ID]; ok {
			if !e.Success {
				p.result <- steerUnsupported
				delete(b.pending, e.ID)
			} else if !b.omp && e.Data.Disposition != "queued" {
				p.result <- steerUnknown
				delete(b.pending, e.ID)
			}
			// queued is only transport acceptance. Wait for the user event.
		}
	case "message_start":
		role, text, _, _ := piMessageText(e.Message)
		var native struct {
			Steering    bool   `json:"steering"`
			Attribution string `json:"attribution"`
		}
		_ = json.Unmarshal(e.Message, &native)
		if role == "user" && b.active && (!b.omp || e.MessageID != "" && native.Steering && native.Attribution == "user") {
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
		}
	case "message_end":
		role, text, stop, tool := piMessageText(e.Message)
		if role == "assistant" && !tool {
			if stop == "error" || stop == "aborted" {
				b.failLocked(b.nativeName() + "'s native provider or permission setup stopped this request; review locally before continuing")
				return
			}
			if len(text) > maxOutput+1 {
				text = text[:maxOutput+1]
			}
			b.answer = text
		}
	case "extension_ui_request":
		switch e.Method {
		case "notify", "setStatus", "setWidget", "setTitle", "set_editor_text", "setWorkingMessage", "setFooter", "setHeader":
		default:
			b.failLocked("A native " + b.nativeName() + " extension needs interactive input; no approval or response was invented")
		}
	case "prompt_result":
		if !b.omp || e.ID != "agentnet-initial" {
			return
		}
		if e.Status != "completed" || !e.Settled {
			b.failLocked("OMP ended without a settled completed prompt")
			return
		}
		b.publishLocked()
	case "agent_settled":
		if b.omp {
			return
		}
		if e.Aborted {
			b.failLocked(b.nativeName() + " was aborted before a completed answer")
			return
		}
		b.publishLocked()
	}
}
func (b *piRPCBridge) publishLocked() {
	if strings.TrimSpace(b.answer) == "" {
		b.failLocked("Native request settled without an assistant answer")
		return
	}
	fmt.Fprintln(b.output, b.answer)
	b.endLocked()
	_ = b.input.Close()
}
func (b *piRPCBridge) steer(ctx context.Context, id, text string) string {
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
		if b.send(ctx, id, p, map[string]string{"id": id, "type": "steer", "message": text}) != nil {
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

func (b *piRPCBridge) nativeName() string {
	if b.omp {
		return "OMP"
	}
	return "Pi"
}
