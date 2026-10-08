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

const codexRoomUnavailable = "Codex room transport unavailable. Use a Codex version supporting app-server dynamic tools and retry explicitly after recovery; no question was escalated to a task or given network access."

type codexRoomBridge struct {
	ctx                  context.Context
	input                *io.PipeWriter
	output               io.Writer
	call                 func(context.Context, json.RawMessage) (string, error)
	writeMu              sync.Mutex
	closeOnce            sync.Once
	prompt, cwd          string
	buf                  []byte
	thread, turn, answer string
	calls                map[string]bool
	done                 bool
}

func newCodexRoomBridge(ctx context.Context, cmd *exec.Cmd, prompt string, output io.Writer, call func(context.Context, json.RawMessage) (string, error)) *codexRoomBridge {
	in, out := io.Pipe()
	b := &codexRoomBridge{ctx: ctx, input: out, output: output, call: call, calls: map[string]bool{}}
	cmd.Args = []string{cmd.Path, "app-server", "-c", `approval_policy="never"`}
	cmd.Stdin = in
	cmd.Stdout = b
	go func() {
		b.send(map[string]any{"id": 1, "method": "initialize", "params": map[string]any{"clientInfo": map[string]any{"name": "agentnet", "version": protocol.Version}, "capabilities": map[string]any{"experimentalApi": true}}})
	}()
	// Prompt is retained only for this invocation, never in command arguments.
	b.prompt = prompt
	b.cwd = cmd.Dir
	go func() { <-ctx.Done(); b.close() }()
	return b
}
func (b *codexRoomBridge) close() { b.closeOnce.Do(func() { b.input.Close() }) }
func (b *codexRoomBridge) send(v any) {
	b.writeMu.Lock()
	defer b.writeMu.Unlock()
	_ = json.NewEncoder(b.input).Encode(v)
}
func (b *codexRoomBridge) fail() {
	if !b.done {
		b.done = true
		fmt.Fprintln(b.output, needsHumanMarker)
		fmt.Fprintln(b.output, codexRoomUnavailable)
	}
	b.close()
}
func (b *codexRoomBridge) finish() {
	if !b.done {
		b.fail()
	}
	b.close()
}
func (b *codexRoomBridge) Write(p []byte) (int, error) {
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
	if b.done {
		return
	}
	var m struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if json.Unmarshal(raw, &m) != nil {
		b.fail()
		return
	}
	if len(m.Error) > 0 && string(m.Error) != "null" {
		b.fail()
		return
	}
	if m.Method == "" {
		switch string(m.ID) {
		case "1":
			b.send(map[string]any{"method": "initialized"})
			spec := map[string]any{"type": "function", "name": "agentnet_room", "description": "Ask another active agent in this exact group a question by roster PID, or wait for its correlated reply. Bound to this run; grants, removal and cancellation apply. Cannot assign tasks.", "inputSchema": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"action"}, "properties": map[string]any{"action": map[string]any{"type": "string", "enum": []string{"ask", "wait"}}, "pid": map[string]string{"type": "string"}, "text": map[string]string{"type": "string"}, "id": map[string]string{"type": "string"}}}}
			b.send(map[string]any{"id": 2, "method": "thread/start", "params": map[string]any{"cwd": b.cwd, "ephemeral": true, "sandbox": "read-only", "approvalPolicy": "never", "dynamicTools": []any{spec}}})
		case "2":
			var r struct {
				Thread struct {
					ID string `json:"id"`
				} `json:"thread"`
				Approval string `json:"approvalPolicy"`
				Sandbox  struct {
					Type    string `json:"type"`
					Network bool   `json:"networkAccess"`
				} `json:"sandbox"`
			}
			if json.Unmarshal(m.Result, &r) != nil || r.Thread.ID == "" || r.Approval != "never" || r.Sandbox.Type != "readOnly" || r.Sandbox.Network {
				b.fail()
				return
			}
			b.thread = r.Thread.ID
			b.send(map[string]any{"id": 3, "method": "turn/start", "params": map[string]any{"threadId": b.thread, "cwd": b.cwd, "approvalPolicy": "never", "sandboxPolicy": map[string]any{"type": "readOnly", "networkAccess": false}, "input": []any{map[string]any{"type": "text", "text": b.prompt}}}})
		case "3":
			var r struct {
				Turn struct {
					ID string `json:"id"`
				} `json:"turn"`
			}
			if json.Unmarshal(m.Result, &r) != nil || r.Turn.ID == "" || b.turn != "" && b.turn != r.Turn.ID {
				b.fail()
				return
			}
			b.turn = r.Turn.ID
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
	case "turn/started":
		if p.ThreadID == b.thread {
			if b.turn != "" && b.turn != p.Turn.ID {
				b.fail()
				return
			}
			b.turn = p.Turn.ID
		}
	case "item/tool/call":
		if p.ThreadID != b.thread || b.turn == "" || p.TurnID != b.turn || p.Tool != "agentnet_room" || p.Namespace != nil || p.CallID == "" || len(m.ID) == 0 {
			b.fail()
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
				b.fail()
				return
			}
			b.done = true
			fmt.Fprint(b.output, b.answer)
			b.close()
		}
	default:
		if len(m.ID) > 0 {
			b.fail()
		} // new approval or unsupported client action: never approve it
	}
}
