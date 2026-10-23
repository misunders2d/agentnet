package client

// Codex's stdio app-server owns every worker session and its normal settings.
// An eligible group run also gets the existing run-bound room tool.
import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func (a *Agent) codexRoomEligible(j job, r *Responder, plan sessionPlan) bool {
	if j.Kind != envelope.KindQuestion || !a.codexAgentActionsEligible(j, r, plan) {
		return false
	}
	p, _ := a.Participation(j.PID)
	m, err := a.dmMembers(j.Conv)
	return p.Member && err == nil && m.group != nil
}

func (a *Agent) codexAgentActionsEligible(j job, r *Responder, plan sessionPlan) bool {
	if r.Harness != "codex" || j.Kind != envelope.KindQuestion && j.Kind != envelope.KindTask || j.Receiver != nil || j.PID == "" || plan.ref != nil {
		return false
	}
	p, e := a.Participation(j.PID)
	if e != nil || !p.HostHere || !p.Claimable() || p.Role != "" {
		return false
	}
	return p.Conv == j.Conv
}

func (a *Agent) codexRoomTool(ctx context.Context, cause string, raw json.RawMessage) (string, error) {
	if e := ctx.Err(); e != nil {
		return "", e
	}
	var p struct {
		Action string   `json:"action"`
		Kind   string   `json:"kind"`
		PID    string   `json:"pid"`
		Text   string   `json:"text"`
		ID     string   `json:"id"`
		IDs    []string `json:"ids"`
		Topic  *string  `json:"topic"`
		New    bool     `json:"new"`
		Title  string   `json:"title"`
		Author string   `json:"author"`
		Emoji  string   `json:"emoji"`
		Remove bool     `json:"remove"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if e := dec.Decode(&p); e != nil {
		return "", errors.New("choose ask or wait with its exact identifier")
	}
	if dec.Decode(new(any)) != io.EOF {
		return "", errors.New("choose one room operation")
	}
	if p.Kind != "" && (p.Action != "ask" || p.Kind != envelope.KindQuestion && p.Kind != envelope.KindTask) {
		return "", errors.New("kind must be question or task and is only for ask")
	}
	ref := p.ID
	switch p.Action {
	case "organize", "react", "send":
		if p.PID != "" {
			return "", errors.New("this action belongs to the exact agent of this native run")
		}
		var pid, conv string
		var attempt int
		if e := a.store.db.QueryRow(`SELECT pid,conv,attempts FROM inbox WHERE id=? AND state=? AND replica=0`, cause, stateRunning).Scan(&pid, &conv, &attempt); e != nil {
			return "", errors.New("this native request is no longer running")
		}
		if p.Action == "organize" {
			if p.Topic == nil && !p.New {
				return "", errors.New("choose a destination topic, Main, or a new topic")
			}
			topic := ""
			main := false
			if p.Topic != nil {
				topic = *p.Topic
				main = topic == "" || topic == "main"
				if main {
					topic = ""
				}
			}
			sent, e := a.OrganizeAgentTopic(ctx, cause, attempt, TopicOrganizationRequest{Conv: conv, IDs: p.IDs, Topic: topic, New: p.New, Title: p.Title, Main: main})
			if e != nil {
				return "", e
			}
			result := fmt.Sprintf("topic move %s stored (%s)", sent.LID, sent.State)
			if link, err := a.OrganizationLink(sent); err == nil {
				result += "\nDestination: " + link
			} else {
				result += "\nDestination link unavailable; the move remains stored."
			}
			return result, nil
		}
		if p.Action == "react" {
			sent, e := a.SendAgentReaction(ctx, pid, ControlRef{Conv: conv, ID: p.ID, Fingerprint: p.Author}, p.Emoji, p.Remove)
			if e != nil {
				return "", e
			}
			return fmt.Sprintf("agent reaction %s stored (%s)", sent.ID, sent.State), nil
		}
		topic := ""
		if p.Topic != nil {
			topic = *p.Topic
		}
		sent, e := a.SendAgentMessage(ctx, pid, topic, p.Text)
		if e != nil {
			return "", e
		}
		return fmt.Sprintf("agent message %s stored (%s)", sent.LID, sent.State), nil
	case "ask":
		if !protocol.ValidID(p.PID) || strings.TrimSpace(p.Text) == "" || p.ID != "" {
			return "", errors.New("choose an exact current group agent PID and request text")
		}
		if p.Kind == "" {
			p.Kind = envelope.KindQuestion
		}
		sent, e := a.SendRoomAsk(ctx, cause, p.PID, p.Kind, p.Text)
		if e != nil {
			return "", e
		}
		ref = sent.LID
	case "wait":
		if !protocol.ValidID(ref) || p.PID != "" || p.Text != "" {
			return "", errors.New("choose the exact permitted request ID")
		}
	default:
		return "", errors.New("choose ask, wait, organize, react or send")
	}
	waiting := false
	defer func() {
		if waiting {
			a.noteRoomWait(cause, -1)
		}
	}()
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
		if !waiting {
			a.noteRoomWait(cause, 1)
			waiting = true
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-changed:
		}
	}
}

// nativeStatusPrefix marks text AgentNet itself wrote into a run's output,
// so it is never presented as the agent's own words.
const nativeStatusPrefix = "AgentNet: "

const codexLineMax = 4 << 20

type codexRoomBridge struct {
	ctx                  context.Context
	input                io.WriteCloser
	output               io.Writer
	onActivity           func(nativeActivity)
	onThinking           func(string)
	thinking             nativeThinkingBuffer
	call                 func(context.Context, json.RawMessage) (string, error)
	native               func(context.Context, nativeCallback) (json.RawMessage, error)
	nativeCheck          func(nativeCallback) bool
	nativeComplete       func(nativeCallback)
	threadAvailable      func(string) bool
	nativeSerial         sync.Mutex
	nativeEnd            chan struct{}
	nativePending        map[string]context.CancelFunc
	nativeSeen           map[string]string
	nativePolicy         string
	nativeItems          map[string]json.RawMessage
	writeMu              sync.Mutex
	stateMu              sync.Mutex
	readMu               sync.Mutex
	ready                chan struct{}
	readyOnce            sync.Once
	steerID              int
	steering             map[string]chan codexSteerResult
	model                modelObservation
	closeOnce            sync.Once
	prompt, cwd          string
	resume               string
	ephemeral            bool
	buf                  []byte
	thread, turn, answer string
	calls                map[string]bool
	done                 bool
	// failure is the exact technical reason the run ended without a native
	// outcome; it is reported as a failed run, never as a human decision.
	failure string
}

func newCodexRoomBridge(ctx context.Context, cmd *exec.Cmd, prompt string, output io.Writer, call func(context.Context, json.RawMessage) (string, error), plans ...sessionPlan) (*codexRoomBridge, error) {
	// A real OS stdin pipe lets Cmd.Wait finish on process EOF. An io.Pipe
	// reader would leave exec's stdin copier blocked after an early native exit.
	cmd.Stdin = nil
	out, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	b := &codexRoomBridge{prompt: prompt, cwd: cmd.Dir, ctx: ctx, input: out, output: output, call: call, calls: map[string]bool{}, ready: make(chan struct{}), nativeEnd: make(chan struct{}), steering: map[string]chan codexSteerResult{}}
	b.ephemeral = true
	if len(plans) > 0 && plans[0].ref != nil {
		b.ephemeral = false
		if plans[0].resume {
			b.resume = plans[0].ref.ID
		}
	}
	cmd.Args = []string{cmd.Path, "app-server"}
	cmd.Stdout = b
	go func() {
		b.send(map[string]any{"id": 1, "method": "initialize", "params": map[string]any{"clientInfo": map[string]any{"name": "agentnet", "version": protocol.Version}, "capabilities": map[string]any{"experimentalApi": true}}})
	}()
	// Prompt is retained only for this invocation, never in command arguments.
	go func() { <-ctx.Done(); b.close() }()
	return b, nil
}
func (b *codexRoomBridge) close() {
	b.closeOnce.Do(func() {
		if b.nativeEnd != nil {
			close(b.nativeEnd)
		}
		b.input.Close()
	})
}
func (b *codexRoomBridge) send(v any) {
	b.writeMu.Lock()
	defer b.writeMu.Unlock()
	_ = json.NewEncoder(b.input).Encode(v)
}
func (b *codexRoomBridge) failLocked(why string) {
	if !b.done {
		b.done = true
		b.failure = why
	}
	b.close()
}

// humanLocked ends the run with a genuine native permission or input wait.
func (b *codexRoomBridge) humanLocked(why string) {
	if !b.done {
		b.done = true
		fmt.Fprintln(b.output, needsHumanMarker)
		fmt.Fprintln(b.output, nativeStatusPrefix+why)
	}
	b.close()
}

// failureText is the technical reason the run failed, or "".
func (b *codexRoomBridge) failureText() string {
	b.stateMu.Lock()
	defer b.stateMu.Unlock()
	return b.failure
}

// codexRPCError words a JSON-RPC error response to one of the bridge's own
// requests.
func codexRPCError(id, raw json.RawMessage) string {
	method := map[string]string{"1": "initialize", "2": "thread start", "3": "turn start"}[string(id)]
	if method == "" {
		method = "request " + clip(id)
	}
	var e struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(raw, &e)
	if e.Message == "" {
		e.Message = "no message"
	}
	return fmt.Sprintf("Codex refused %s: %s (code %d)", method, clip([]byte(e.Message)), e.Code)
}
func (b *codexRoomBridge) finish() {
	b.stateMu.Lock()
	defer b.stateMu.Unlock()
	for id, ch := range b.steering {
		ch <- codexSteerResult{outcome: steerUnknown}
		delete(b.steering, id)
	}
	if !b.done {
		if b.thread == "" {
			b.failLocked("Codex exited before starting a session")
		} else {
			b.failLocked("Codex ended before the turn completed")
		}
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
				b.fail("Codex sent a protocol line over the size bound")
				return n, nil
			}
			b.buf = append(b.buf, p...)
			break
		}
		if len(b.buf)+i > codexLineMax {
			b.fail("Codex sent a protocol line over the size bound")
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
		b.failLocked("Codex sent an unreadable protocol line")
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
		b.failLocked(codexRPCError(m.ID, m.Error))
		return
	}
	if m.Method == "" {
		switch string(m.ID) {
		case "1":
			b.send(map[string]any{"method": "initialized"})
			spec := map[string]any{"type": "function", "name": "agentnet_room", "description": "Bound to this admitted native run: ask a question or task of group agents and wait for replies; tasks require recipient consent. Organize only supplied messages on the human request; react to an admitted ordinary canonical mention; send as this exact agent. Current admission, native permissions, removal and cancellation apply.", "inputSchema": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"action"}, "properties": map[string]any{"action": map[string]any{"type": "string", "enum": []string{"ask", "wait", "organize", "react", "send"}}, "kind": map[string]any{"type": "string", "enum": []string{"question", "task"}}, "pid": map[string]string{"type": "string"}, "text": map[string]string{"type": "string"}, "id": map[string]string{"type": "string"}, "ids": map[string]any{"type": "array", "items": map[string]string{"type": "string"}, "maxItems": envelope.MaxTopicMoves}, "topic": map[string]string{"type": "string"}, "new": map[string]string{"type": "boolean"}, "title": map[string]string{"type": "string"}, "author": map[string]string{"type": "string"}, "emoji": map[string]string{"type": "string"}, "remove": map[string]string{"type": "boolean"}}}}
			method, params := "thread/start", map[string]any{"cwd": b.cwd, "ephemeral": b.ephemeral}
			if b.resume != "" {
				method, params = "thread/resume", map[string]any{"cwd": b.cwd, "threadId": b.resume}
			} else if b.call != nil {
				params["dynamicTools"] = []any{spec}
			}
			// Select native automatic review without changing the native sandbox.
			params["approvalPolicy"] = "on-request"
			params["approvalsReviewer"] = "auto_review"
			b.send(map[string]any{"id": 2, "method": method, "params": params})
		case "2":
			var r struct {
				Model          string          `json:"model"`
				Reasoning      json.RawMessage `json:"reasoningEffort"`
				ApprovalPolicy json.RawMessage `json:"approvalPolicy"`
				Thread         struct {
					ID string `json:"id"`
				} `json:"thread"`
			}
			if json.Unmarshal(m.Result, &r) != nil || r.Thread.ID == "" || b.resume != "" && r.Thread.ID != b.resume {
				b.failLocked("Codex returned no usable session")
				return
			}
			b.thread = r.Thread.ID
			if b.threadAvailable != nil && !b.threadAvailable(b.thread) {
				b.humanLocked("The native session is already owned by another active request, or its reservation could not be confirmed. No turn input was sent; check the existing work before explicitly continuing.")
				return
			}
			_ = json.Unmarshal(r.ApprovalPolicy, &b.nativePolicy)
			if protocol.ValidReportedModel(r.Model) {
				b.model = nativeModelObservation(r.Model, r.Reasoning)
			}
			b.send(map[string]any{"id": 3, "method": "turn/start", "params": map[string]any{"threadId": b.thread, "cwd": b.cwd, "input": []any{map[string]any{"type": "text", "text": b.prompt}}}})
		case "3":
			var r struct {
				Turn struct {
					ID string `json:"id"`
				} `json:"turn"`
			}
			if json.Unmarshal(m.Result, &r) != nil || r.Turn.ID == "" || b.turn != "" && b.turn != r.Turn.ID {
				b.failLocked("Codex returned an unexpected turn")
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
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		} `json:"turn"`
		Item struct {
			ID   string `json:"id"`
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"item"`
	}
	if json.Unmarshal(m.Params, &p) != nil {
		return
	}
	switch m.Method {
	case "serverRequest/resolved":
		var resolved struct {
			ThreadID  string          `json:"threadId"`
			RequestID json.RawMessage `json:"requestId"`
		}
		if json.Unmarshal(m.Params, &resolved) == nil && resolved.ThreadID == b.thread {
			key := string(resolved.RequestID)
			if cancel := b.nativePending[key]; cancel != nil {
				delete(b.nativePending, key)
				cancel()
			}
		}
	case "item/started":
		if b.onThinking != nil && b.thread != "" && b.turn != "" && p.ThreadID == b.thread && p.TurnID == b.turn {
			b.thinking = nativeThinkingBuffer{}
			if p.Item.Type == "reasoning" && p.Item.ID != "" {
				b.thinking.start(p.Item.ID, 0)
				emitNativeThinking(b.onThinking, b.onActivity, "")
			}
		}
		if b.onActivity != nil && b.thread != "" && b.turn != "" && p.ThreadID == b.thread && p.TurnID == b.turn {
			var k nativeActivity
			switch p.Item.Type {
			case "commandExecution":
				k = activityCommand
			case "fileChange":
				k = activityEditing
			case "webSearch":
				k = activitySearch
			case "mcpToolCall", "dynamicToolCall":
				k = activityTool
			case "agentMessage":
				k = activityAnswer
			}
			if k != activityUnknown {
				emitNativeActivity(b.onActivity, k)
			}
		}
		if p.ThreadID == b.thread && p.TurnID == b.turn && (p.Item.Type == "commandExecution" || p.Item.Type == "fileChange") {
			var n struct {
				Item json.RawMessage `json:"item"`
			}
			var item struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(m.Params, &n) == nil && json.Unmarshal(n.Item, &item) == nil && item.ID != "" && len(n.Item) <= nativeOperationMax {
				if b.nativeItems == nil {
					b.nativeItems = map[string]json.RawMessage{}
				}
				if len(b.nativeItems) < 32 {
					b.nativeItems[item.ID] = append(json.RawMessage(nil), n.Item...)
				}
			}
		}
	case "item/reasoning/summaryPartAdded", "item/reasoning/summaryTextDelta":
		if b.onThinking == nil || b.thread == "" || b.turn == "" || p.ThreadID != b.thread || p.TurnID != b.turn {
			return
		}
		var event struct {
			ItemID       string          `json:"itemId"`
			SummaryIndex json.RawMessage `json:"summaryIndex"`
			Delta        json.RawMessage `json:"delta"`
		}
		if json.Unmarshal(m.Params, &event) != nil {
			return
		}
		if !b.thinking.open || event.ItemID == "" || event.ItemID != b.thinking.key {
			return
		}
		var index *int64
		if json.Unmarshal(event.SummaryIndex, &index) != nil || index == nil || *index < 0 {
			b.thinking = nativeThinkingBuffer{}
			emitNativeThinking(b.onThinking, b.onActivity, "")
			return
		}
		if m.Method == "item/reasoning/summaryPartAdded" {
			if *index < b.thinking.index || *index == b.thinking.index && b.thinking.started {
				return
			}
			if *index > b.thinking.index {
				b.thinking.finish(b.onThinking, b.onActivity)
			}
			b.thinking.start(event.ItemID, *index)
			return
		}
		var delta string
		if json.Unmarshal(event.Delta, &delta) != nil {
			b.thinking = nativeThinkingBuffer{}
			emitNativeThinking(b.onThinking, b.onActivity, "")
			return
		}
		b.thinking.add(event.ItemID, *index, delta, b.onThinking, b.onActivity)
	case "thread/settings/updated":
		var settings struct {
			ThreadID       string `json:"threadId"`
			ThreadSettings struct {
				Model          string          `json:"model"`
				Reasoning      json.RawMessage `json:"effort"`
				ApprovalPolicy json.RawMessage `json:"approvalPolicy"`
			} `json:"threadSettings"`
		}
		if json.Unmarshal(m.Params, &settings) == nil && settings.ThreadID == b.thread && protocol.ValidReportedModel(settings.ThreadSettings.Model) {
			b.model = nativeModelObservation(settings.ThreadSettings.Model, settings.ThreadSettings.Reasoning)
		}
		if settings.ThreadID == b.thread && len(settings.ThreadSettings.ApprovalPolicy) > 0 {
			_ = json.Unmarshal(settings.ThreadSettings.ApprovalPolicy, &b.nativePolicy)
		}
	case "turn/started":
		if p.ThreadID == b.thread {
			if b.turn != "" && b.turn != p.Turn.ID {
				b.failLocked("Codex started an unexpected turn")
				return
			}
			b.turn = p.Turn.ID
			b.markReady()
		}
	case "item/tool/call":
		if b.call == nil || p.ThreadID != b.thread || b.turn == "" || p.TurnID != b.turn || p.Tool != "agentnet_room" || p.Namespace != nil || p.CallID == "" || len(m.ID) == 0 {
			b.failLocked("Codex sent an unexpected tool call " + strconv.Quote(clip([]byte(p.Tool))))
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
		if p.ThreadID == b.thread && p.TurnID == b.turn && p.Item.Type == "reasoning" && p.Item.ID == b.thinking.key {
			b.thinking.finish(b.onThinking, b.onActivity)
		}
		if b.onActivity != nil && b.thread != "" && b.turn != "" && p.ThreadID == b.thread && p.TurnID == b.turn {
			switch p.Item.Type {
			case "commandExecution", "fileChange", "webSearch", "mcpToolCall", "dynamicToolCall":
				emitNativeActivity(b.onActivity, activityWorking)
			}
		}
		if p.ThreadID == b.thread && p.TurnID == b.turn {
			var n struct {
				Item struct {
					ID string `json:"id"`
				} `json:"item"`
			}
			if json.Unmarshal(m.Params, &n) == nil {
				delete(b.nativeItems, n.Item.ID)
			}
		}
		if p.ThreadID == b.thread && p.TurnID == b.turn && p.Item.Type == "agentMessage" {
			b.answer = p.Item.Text
		}
	case "turn/completed":
		if p.ThreadID == b.thread && p.Turn.ID == b.turn {
			if p.Turn.Status != "completed" {
				why := "Codex ended the turn as " + strconv.Quote(clip([]byte(p.Turn.Status)))
				if p.Turn.Error != nil && p.Turn.Error.Message != "" {
					why += ": " + clip([]byte(p.Turn.Error.Message))
				}
				b.failLocked(why)
				return
			}
			if strings.TrimSpace(b.answer) == "" {
				b.failLocked("Codex completed the turn without an answer")
				return
			}
			b.done = true
			fmt.Fprint(b.output, b.answer)
			b.close()
		}
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval", "item/permissions/requestApproval", "item/tool/requestUserInput", "tool/requestUserInput", "mcpServer/elicitation/request":
		if len(m.ID) > 0 {
			if b.thread == "" || p.ThreadID != b.thread || b.turn == "" || (p.TurnID != b.turn && !(m.Method == "mcpServer/elicitation/request" && p.TurnID == "")) {
				b.failLocked("Codex sent " + m.Method + " for another turn; nothing was approved")
				return
			}
			if b.native != nil {
				b.beginNativeLocked(m.Method, m.ID, m.Params)
				return
			}
			b.done = true
			fmt.Fprintln(b.output, needsHumanMarker)
			fmt.Fprintln(b.output, nativeStatusPrefix+"Your Codex setup requires a native approval or input for this action. This background session cannot collect it. Continue in your native Codex session with the specific permission; retrying unchanged will ask again. AgentNet did not approve or bypass it.")
			var why struct{ Reason, Message string }
			if json.Unmarshal(m.Params, &why) == nil {
				for _, text := range []string{why.Reason, why.Message} {
					if text = strings.TrimSpace(text); text != "" {
						fmt.Fprintln(b.output, clip([]byte(text)))
					}
				}
			}
			b.close()
		}
	default:
		if len(m.ID) > 0 {
			b.failLocked("Codex sent an unsupported request " + strconv.Quote(clip([]byte(m.Method))) + "; nothing was approved")
		} // new approval or unsupported client action: never approve it
	}
}

// Serial publication keeps every distinct native RPC actionable without
// replacing an earlier pending operation. Native events continue to stream.
func (b *codexRoomBridge) beginNativeLocked(method string, id, params json.RawMessage) {
	key := string(id)
	if b.nativeSeen == nil {
		b.nativeSeen = map[string]string{}
		b.nativePending = map[string]context.CancelFunc{}
	}
	var canonical any
	_ = json.Unmarshal(params, &canonical)
	canonicalParams, _ := json.Marshal(canonical)
	identity := method + "\x00" + string(canonicalParams)
	if seen := b.nativeSeen[key]; seen != "" {
		if seen != identity {
			b.failLocked("Codex changed a pending native request")
		}
		return
	}
	var p struct {
		ItemID string `json:"itemId"`
	}
	_ = json.Unmarshal(params, &p)
	callback, e := codexNativeCallback(method, id, params, b.nativeItems[p.ItemID], b.cwd, b.thread, b.turn)
	if e != nil {
		b.failLocked("Codex sent an unreadable native request: " + e.Error())
		return
	}
	if b.nativePolicy == "never" && strings.HasSuffix(method, "/requestApproval") {
		callback.View.Choices = slices.DeleteFunc(callback.View.Choices, func(choice string) bool { return choice == "allow" })
		if len(callback.View.Choices) == 0 {
			callback.View.Choices = []string{"cancel"}
			callback.View.CancelRun = true
		}
		callback.View.Unsupported = "Your Codex setup uses approval policy Never. AgentNet cannot grant this permission. Deny the action or cancel it here."
	}
	b.nativeSeen[key] = identity
	ctx, cancel := context.WithCancel(b.ctx)
	b.nativePending[key] = cancel
	ended := b.nativeEnd
	go func() {
		select {
		case <-ctx.Done():
		case <-ended:
			cancel()
		}
	}()
	go func() {
		defer cancel()
		if b.nativeComplete != nil {
			defer b.nativeComplete(callback)
		}
		b.nativeSerial.Lock()
		defer b.nativeSerial.Unlock()
		if ctx.Err() != nil {
			return
		}
		result, e := b.native(ctx, callback)
		b.stateMu.Lock()
		defer b.stateMu.Unlock()
		if b.done || ctx.Err() != nil || b.nativePending[key] == nil || b.thread != callback.View.Thread || b.turn != callback.View.Turn {
			return
		}
		delete(b.nativePending, key)
		if e != nil || b.nativeCheck != nil && !b.nativeCheck(callback) || b.nativePolicy == "never" && nativePermissionGrant(callback.View.Method, result) {
			b.done = true
			fmt.Fprintln(b.output, needsHumanMarker)
			fmt.Fprintln(b.output, nativeStatusPrefix+"The native request could not be continued with current authority. No approval was sent. Review the existing work before explicitly continuing.")
			b.close()
			return
		}
		b.writeMu.Lock()
		err := json.NewEncoder(b.input).Encode(map[string]any{"id": callback.ID, "result": result})
		b.writeMu.Unlock()
		if err != nil {
			b.failLocked("the native response could not be handed to Codex")
		} // uncertain handover is never retried
	}()
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

func (b *codexRoomBridge) fail(why string) {
	b.stateMu.Lock()
	defer b.stateMu.Unlock()
	b.failLocked(why)
}
func (b *codexRoomBridge) markReady() {
	if b.ready != nil {
		b.readyOnce.Do(func() { close(b.ready) })
	}
}

// Model and effort belong to the same native thread snapshot. Missing or
// invalid effort clears the older value; metadata never changes native settings.
func nativeModelObservation(model string, raw json.RawMessage) modelObservation {
	var reasoning string
	if json.Unmarshal(raw, &reasoning) != nil || !protocol.ValidReportedReasoning(reasoning) {
		reasoning = ""
	}
	return modelObservation{Model: model, Reasoning: reasoning}
}
func (b *codexRoomBridge) modelReport() modelObservation {
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
