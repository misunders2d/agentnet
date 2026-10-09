package client

// Codex's stdio app-server owns the model and its normal configuration. This
// adapter adds only a run-bound room tool; sandboxed commands gain no sockets.
import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func (a *Agent) codexRoomEligible(j job, r *Responder, plan sessionPlan) bool {
	if r.Harness != "codex" || j.Kind != envelope.KindQuestion || j.Receiver != nil || j.PID == "" || plan.ref != nil {
		return false
	}
	p, e := a.Participation(j.PID)
	if e != nil || !p.Member || !p.HostHere || !p.Claimable() {
		return false
	}
	m, e := a.dmMembers(j.Conv)
	return e == nil && m.group != nil
}

func (a *Agent) codexRoomTool(ctx context.Context, cause string, raw json.RawMessage) (string, error) {
	if e := ctx.Err(); e != nil {
		return "", e
	}
	var p struct {
		Action string `json:"action"`
		PID    string `json:"pid"`
		Text   string `json:"text"`
		ID     string `json:"id"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if e := dec.Decode(&p); e != nil {
		return "", errors.New("choose ask or wait with its exact identifier")
	}
	if dec.Decode(new(any)) != io.EOF {
		return "", errors.New("choose one room operation")
	}
	ref := p.ID
	switch p.Action {
	case "ask":
		if !protocol.ValidID(p.PID) || strings.TrimSpace(p.Text) == "" || p.ID != "" {
			return "", errors.New("choose an exact current group agent PID and question text")
		}
		sent, e := a.SendRoomAsk(ctx, cause, p.PID, envelope.KindQuestion, p.Text)
		if e != nil {
			return "", e
		}
		ref = sent.LID
	case "wait":
		if !protocol.ValidID(ref) || p.PID != "" || p.Text != "" {
			return "", errors.New("choose the exact permitted request ID")
		}
	default:
		return "", errors.New("room question tool supports only ask and wait; it cannot assign tasks")
	}
	for {
		// Subscribe before reading so a stored answer cannot race the wakeup.
		_, changed := a.Changed()
		reply, e := a.RoomReply(cause, ref)
		if e != nil {
			return "", e
		}
		if reply != nil {
			return fmt.Sprintf("reply %s from agent participation %s\n%s", reply.ReplyTo, reply.PID, reply.Body), nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-changed:
		}
	}
}

const codexRoomUnavailable = "Codex room transport unavailable. Use a Codex version supporting app-server dynamic tools. AgentNet did not change your native permissions. Review completed work before an explicit continuation."

type codexRoomBridge struct {
	ctx                  context.Context
	input                *io.PipeWriter
	output               io.Writer
	call                 func(context.Context, json.RawMessage) (string, error)
	writeMu              sync.Mutex
	stateMu              sync.Mutex
	readMu               sync.Mutex
	ready                chan struct{}
	readyOnce            sync.Once
	steerID              int
	steering             map[string]chan codexSteerResult
	model                string
	closeOnce            sync.Once
	prompt, cwd          string
	buf                  []byte
	thread, turn, answer string
	calls                map[string]bool
	done                 bool
}

func newCodexRoomBridge(ctx context.Context, cmd *exec.Cmd, prompt string, output io.Writer, call func(context.Context, json.RawMessage) (string, error)) *codexRoomBridge {
	in, out := io.Pipe()
	b := &codexRoomBridge{prompt: prompt, cwd: cmd.Dir, ctx: ctx, input: out, output: output, call: call, calls: map[string]bool{}, ready: make(chan struct{}), steering: map[string]chan codexSteerResult{}}
	cmd.Args = []string{cmd.Path, "app-server"}
	cmd.Stdin = in
	cmd.Stdout = b
	go func() {
		b.send(map[string]any{"id": 1, "method": "initialize", "params": map[string]any{"clientInfo": map[string]any{"name": "agentnet", "version": protocol.Version}, "capabilities": map[string]any{"experimentalApi": true}}})
	}()
	// Prompt is retained only for this invocation, never in command arguments.
	go func() { <-ctx.Done(); b.close() }()
	return b
}
func (b *codexRoomBridge) close() { b.closeOnce.Do(func() { b.input.Close() }) }
func (b *codexRoomBridge) send(v any) {
	b.writeMu.Lock()
	defer b.writeMu.Unlock()
	_ = json.NewEncoder(b.input).Encode(v)
}
func (b *codexRoomBridge) failLocked() {
	if !b.done {
		b.done = true
		fmt.Fprintln(b.output, needsHumanMarker)
		fmt.Fprintln(b.output, codexRoomUnavailable)
	}
	b.close()
}
func (b *codexRoomBridge) finish() {
	b.stateMu.Lock()
	defer b.stateMu.Unlock()
	for id, ch := range b.steering {
		ch <- codexSteerResult{outcome: steerUnknown}
		delete(b.steering, id)
	}
	if !b.done {
		b.failLocked()
	}
	b.close()
}
func (b *codexRoomBridge) Write(p []byte) (int, error) {
	b.readMu.Lock()
	defer b.readMu.Unlock()
	n := len(p)
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			if len(b.buf)+len(p) > codexLineMax {
				b.fail()
				return n, nil
			}
			b.buf = append(b.buf, p...)
			break
		}
		if len(b.buf)+i > codexLineMax {
			b.fail()
			return n, nil
		}
		b.buf = append(b.buf, p[:i]...)
		b.event(b.buf)
		b.buf = nil
		p = p[i+1:]
	}
	return n, nil
}
func (b *codexRoomBridge) event(raw []byte) {
	b.stateMu.Lock()
	defer b.stateMu.Unlock()
	var m struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if json.Unmarshal(raw, &m) != nil {
		b.failLocked()
		return
	}
	if ch, ok := b.steering[string(m.ID)]; ok && m.Method == "" {
		result := codexSteerResult{outcome: steerUnknown}
		var rpcErr struct {
			Code int `json:"code"`
		}
		var response struct {
			TurnID string `json:"turnId"`
		}
		if json.Unmarshal(m.Error, &rpcErr) == nil && rpcErr.Code == -32601 {
			result.outcome = steerUnsupported
		} else if (len(m.Error) == 0 || string(m.Error) == "null") && json.Unmarshal(m.Result, &response) == nil && response.TurnID == b.turn {
			result.outcome = steerAccepted
		}
		delete(b.steering, string(m.ID))
		ch <- result
		return
	}
	if b.done || m.Method == "" && strings.HasPrefix(string(m.ID), `"agentnet-steer-`) {
		return
	}
	if len(m.Error) > 0 && string(m.Error) != "null" {
		b.failLocked()
		return
	}
	if m.Method == "" {
		switch string(m.ID) {
		case "1":
			b.send(map[string]any{"method": "initialized"})
			spec := map[string]any{"type": "function", "name": "agentnet_room", "description": "Ask another active agent in this exact group a question by roster PID, or wait for its correlated reply. Bound to this run; grants, removal and cancellation apply. Cannot assign tasks.", "inputSchema": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"action"}, "properties": map[string]any{"action": map[string]any{"type": "string", "enum": []string{"ask", "wait"}}, "pid": map[string]string{"type": "string"}, "text": map[string]string{"type": "string"}, "id": map[string]string{"type": "string"}}}}
			b.send(map[string]any{"id": 2, "method": "thread/start", "params": map[string]any{"cwd": b.cwd, "ephemeral": true, "dynamicTools": []any{spec}}})
		case "2":
			var r struct {
				Model  string `json:"model"`
				Thread struct {
					ID string `json:"id"`
				} `json:"thread"`
			}
			if json.Unmarshal(m.Result, &r) != nil || r.Thread.ID == "" {
				b.failLocked()
				return
			}
			b.thread = r.Thread.ID
			if protocol.ValidReportedModel(r.Model) {
				b.model = r.Model
			}
			b.send(map[string]any{"id": 3, "method": "turn/start", "params": map[string]any{"threadId": b.thread, "cwd": b.cwd, "input": []any{map[string]any{"type": "text", "text": b.prompt}}}})
		case "3":
			var r struct {
				Turn struct {
					ID string `json:"id"`
				} `json:"turn"`
			}
			if json.Unmarshal(m.Result, &r) != nil || r.Turn.ID == "" || b.turn != "" && b.turn != r.Turn.ID {
				b.failLocked()
				return
			}
			b.turn = r.Turn.ID
			b.markReady()
		}
		return
	}
	var p struct {
		ThreadID  string          `json:"threadId"`
		TurnID    string          `json:"turnId"`
		Tool      string          `json:"tool"`
		Namespace *string         `json:"namespace"`
		CallID    string          `json:"callId"`
		Arguments json.RawMessage `json:"arguments"`
		Turn      struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"turn"`
		Item struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"item"`
	}
	if json.Unmarshal(m.Params, &p) != nil {
		return
	}
	switch m.Method {
	case "thread/settings/updated":
		var settings struct {
			ThreadID       string `json:"threadId"`
			ThreadSettings struct {
				Model string `json:"model"`
			} `json:"threadSettings"`
		}
		if json.Unmarshal(m.Params, &settings) == nil && settings.ThreadID == b.thread && protocol.ValidReportedModel(settings.ThreadSettings.Model) {
			b.model = settings.ThreadSettings.Model
		}
	case "turn/started":
		if p.ThreadID == b.thread {
			if b.turn != "" && b.turn != p.Turn.ID {
				b.failLocked()
				return
			}
			b.turn = p.Turn.ID
			b.markReady()
		}
	case "item/tool/call":
		if p.ThreadID != b.thread || b.turn == "" || p.TurnID != b.turn || p.Tool != "agentnet_room" || p.Namespace != nil || p.CallID == "" || len(m.ID) == 0 {
			b.failLocked()
			return
		}
		if b.calls[p.CallID] {
			b.send(map[string]any{"id": m.ID, "result": map[string]any{"success": false, "contentItems": []any{map[string]string{"type": "inputText", "text": "Duplicate tool invocation refused; use wait with the stored request ID."}}}})
			return
		}
		b.calls[p.CallID] = true
		args := append(json.RawMessage(nil), p.Arguments...)
		id := append(json.RawMessage(nil), m.ID...)
		go func() {
			text, e := b.call(b.ctx, args)
			if e != nil {
				text = "AgentNet room request unavailable: " + e.Error() + ". No permission or sandbox change was made."
			}
			b.send(map[string]any{"id": id, "result": map[string]any{"success": e == nil, "contentItems": []any{map[string]string{"type": "inputText", "text": text}}}})
		}()
	case "item/completed":
		if p.ThreadID == b.thread && p.TurnID == b.turn && p.Item.Type == "agentMessage" {
			b.answer = p.Item.Text
		}
	case "turn/completed":
		if p.ThreadID == b.thread && p.Turn.ID == b.turn {
			if p.Turn.Status != "completed" || strings.TrimSpace(b.answer) == "" {
				b.failLocked()
				return
			}
			b.done = true
			fmt.Fprint(b.output, b.answer)
			b.close()
		}
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval", "item/permissions/requestApproval", "item/tool/requestUserInput", "tool/requestUserInput", "mcpServer/elicitation/request":
		if len(m.ID) > 0 {
			if b.thread == "" || p.ThreadID != b.thread || b.turn == "" || (p.TurnID != b.turn && !(m.Method == "mcpServer/elicitation/request" && p.TurnID == "")) {
				b.failLocked()
				return
			}
			b.done = true
			fmt.Fprintln(b.output, needsHumanMarker)
			fmt.Fprintln(b.output, "Your Codex setup requires a native approval or input for this action. This background session cannot collect it. Continue in your native Codex session with the specific permission; retrying unchanged will ask again. AgentNet did not approve or bypass it.")
			b.close()
		}
	default:
		if len(m.ID) > 0 {
			b.failLocked()
		} // new approval or unsupported client action: never approve it
	}
}

// stateMu protects the run identity and every correlated steer response. Only
// the owned app-server receives input; no user session is opened or resumed.
type codexSteerResult struct{ outcome string }

const (
	steerAccepted    = "accepted"
	steerQueued      = "queued"
	steerUnsupported = "unsupported"
	steerUnknown     = "unknown"
)

func (b *codexRoomBridge) fail() { b.stateMu.Lock(); defer b.stateMu.Unlock(); b.failLocked() }
func (b *codexRoomBridge) markReady() {
	if b.ready != nil {
		b.readyOnce.Do(func() { close(b.ready) })
	}
}
func (b *codexRoomBridge) modelReport() string {
	b.stateMu.Lock()
	defer b.stateMu.Unlock()
	return b.model
}
func (b *codexRoomBridge) steer(ctx context.Context, id, text string) string {
	b.stateMu.Lock()
	if b.done || b.thread == "" || b.turn == "" || b.ctx.Err() != nil {
		b.stateMu.Unlock()
		return steerQueued
	}
	if b.steering == nil {
		b.steering = map[string]chan codexSteerResult{}
	}
	b.steerID++
	rpcID := fmt.Sprintf("agentnet-steer-%d", b.steerID)
	keyRaw, _ := json.Marshal(rpcID)
	key := string(keyRaw)
	ch := make(chan codexSteerResult, 1)
	b.steering[key] = ch
	request := map[string]any{"id": rpcID, "method": "turn/steer", "params": map[string]any{"threadId": b.thread, "expectedTurnId": b.turn, "clientUserMessageId": id, "input": []any{map[string]string{"type": "text", "text": text}}}}
	b.stateMu.Unlock()
	// A write failure can follow a partial handover: it proves no rejection.
	go b.send(request)
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case result := <-ch:
		return result.outcome
	case <-ctx.Done():
	case <-timer.C:
	}
	b.stateMu.Lock()
	delete(b.steering, key)
	b.stateMu.Unlock()
	return steerUnknown
}
