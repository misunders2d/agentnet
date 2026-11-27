package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// Antigravity's Agent Client Protocol server (`agy acp`), checked live
// against agy 1.5.18 on November 27, 2026 (one probe, recorded in
// docs/revival/M4.md): JSON-RPC 2.0, one message per line on stdio.
// initialize, then session/new (or session/resume of the saved
// conversation: its id is the same conversations/<id> that --conversation
// continues), then session/prompt. Only ACP can steer agy: a prompt sent
// while a turn runs joins that turn. agy then answers the earlier
// session/prompt at once with a bare end_turn and ends the turn with the
// newer prompt's reply, so that handover is the native acceptance of the
// correction and the answer is the newest prompt's.
//
// Permission requests that agy's own rules do not settle reach the client.
// AgentNet never allows anything on the owner's behalf: it rejects once and
// the request needs the owner, exactly as print mode's auto-denial. Any other
// request agy sends (a question) is refused and needs the owner too.
// The stream-json transport (agystream.go) stays for an agy without acp.
type agyACPBridge struct {
	writes                           sync.WaitGroup
	ctx                              context.Context
	input                            io.WriteCloser
	onActivity                       func(nativeActivity)
	ready, settled, closed           chan struct{}
	readyOnce, settleOnce, closeOnce sync.Once
	mu, writeMu, readMu              sync.Mutex
	writeTurn                        *sync.Cond // on writeMu: messages go out in the order they were made
	madeWrites, sentWrites           uint64     // under mu, under writeMu
	buf                              []byte

	dir, prompt string
	session     string // the conversation: expected (resume, warm) or assigned by session/new
	resume      bool   // continue session with session/resume
	attached    bool   // a warm process that already serves session
	next        int64  // JSON-RPC ids of this turn
	initID      int64
	sessionID   int64
	prompts     []int64             // this turn's unanswered session/prompt ids, oldest first
	steers      map[int64]*acpSteer // corrections not yet taken, by prompt id
	seen        map[string]string   // corrections agy took: id → text

	active, done, complete     bool
	run, answer, stop          string
	denied, waiting, auth, bad bool
	failure                    string
	answering                  bool
}

type acpSteer struct {
	id, text string
	result   chan string
}

// agyACPSupported reports whether the selected agy launch has the ACP
// server: `agy acp --help` on the same launcher and environment. A refusal
// is remembered for that exact program file; a timeout is not.
var agyACPProbe sync.Map

func agyACPSupported(ctx context.Context, cmd *exec.Cmd) bool {
	i := slices.Index(cmd.Args, "--input-format")
	if i < 1 {
		return false
	}
	info, err := os.Stat(cmd.Path)
	if err != nil {
		return false
	}
	prefix := slices.Clone(cmd.Args[1:i])
	key := fmt.Sprintf("%s\x00%d\x00%d\x00%s", cmd.Path, info.Size(), info.ModTime().UnixNano(), strings.Join(prefix, "\x00"))
	if v, ok := agyACPProbe.Load(key); ok {
		return v.(bool)
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	probe := exec.CommandContext(probeCtx, cmd.Path, append(prefix, "acp", "--help")...)
	probe.Dir, probe.Env = cmd.Dir, cmd.Env
	probe.WaitDelay = 100 * time.Millisecond
	var out limitedBuffer
	out.max = 64 << 10
	probe.Stdout, probe.Stderr = &out, &out
	ownProcessGroup(probe)
	defer stopGroup(probe)
	runErr := probe.Run()
	if probeCtx.Err() != nil || ctx.Err() != nil {
		return false
	}
	ok := runErr == nil && !out.truncated && strings.Contains(out.String(), "Agent Client Protocol") && strings.Contains(out.String(), "--stdio")
	agyACPProbe.Store(key, ok)
	return ok
}

// prepAgyACPLaunch selects `agy acp` on the native launch. Directories the
// run adds stay --add-dir; the conversation is chosen in the protocol.
func prepAgyACPLaunch(cmd *exec.Cmd) {
	i := slices.Index(cmd.Args, "--input-format")
	if i < 1 {
		return
	}
	args := append(slices.Clone(cmd.Args[:i]), "acp")
	for rest := cmd.Args[i:]; len(rest) > 0; rest = rest[1:] {
		switch rest[0] {
		case "--input-format", "--output-format", "--conversation":
			if len(rest) > 1 {
				rest = rest[1:]
			}
			continue
		}
		args = append(args, rest[0])
	}
	cmd.Args = args
}

func newAgyACPBridge(ctx context.Context, cmd *exec.Cmd, prompt, session string, resume bool) (*agyACPBridge, error) {
	cmd.Stdin = nil
	input, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	b := attachAgyACPBridge(ctx, input, cmd.Dir, prompt, session, resume, false, 0)
	cmd.Stdout = b
	b.begin()
	return b, nil
}

// attachAgyACPBridge decodes one request over input; begin sends it. A warm
// worker (warm.go) attaches a new bridge per turn to its running process,
// which already serves session (attached); turn keeps its ids apart.
func attachAgyACPBridge(ctx context.Context, input io.WriteCloser, dir, prompt, session string, resume, attached bool, turn int) *agyACPBridge {
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs // ACP takes an absolute working directory
	}
	b := &agyACPBridge{ctx: ctx, input: input, dir: dir, prompt: prompt, session: session, resume: resume, attached: attached,
		next: int64(turn) << 20, ready: make(chan struct{}), settled: make(chan struct{}), closed: make(chan struct{}),
		steers: map[int64]*acpSteer{}, seen: map[string]string{}}
	b.writeTurn = sync.NewCond(&b.writeMu)
	return b
}

func (b *agyACPBridge) begin() {
	b.mu.Lock()
	if b.attached {
		b.promptLocked()
	} else {
		b.initID = b.idLocked()
		b.writeLocked(acpCall(b.initID, "initialize", map[string]any{
			"protocolVersion":    1,
			"clientCapabilities": map[string]any{"fs": map[string]bool{"readTextFile": false, "writeTextFile": false}, "terminal": false},
			"clientInfo":         map[string]string{"name": "agentnet", "version": nativeClientVersion()},
		}), "Antigravity did not accept the protocol start")
	}
	b.mu.Unlock()
	go func() {
		select {
		case <-b.ctx.Done():
			b.close()
		case <-b.closed:
		}
	}()
}

// agyACPBeforeWrite is a test hook: it runs before each message is written.
var agyACPBeforeWrite = func(any) {}

func acpCall(id int64, method string, params any) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}
}

func (b *agyACPBridge) idLocked() int64 { b.next++; return b.next }

// writeLocked sends v without holding the bridge: agy may answer before the
// write returns. Writes leave in the order they were made, so agy reads a
// correction after the request it corrects. A write that fails ends the
// request with why.
func (b *agyACPBridge) writeLocked(v any, why string) {
	if b.done {
		return
	}
	turn := b.madeWrites
	b.madeWrites++
	b.writes.Add(1)
	go func() {
		defer b.writes.Done()
		agyACPBeforeWrite(v)
		if b.send(turn, v) != nil && why != "" {
			b.mu.Lock()
			b.failLocked(why)
			b.mu.Unlock()
		}
	}()
}

func (b *agyACPBridge) send(turn uint64, v any) error {
	b.writeMu.Lock()
	defer b.writeMu.Unlock()
	for b.sentWrites != turn {
		b.writeTurn.Wait()
	}
	defer func() {
		b.sentWrites++
		b.writeTurn.Broadcast()
	}()
	if b.ctx.Err() != nil {
		return b.ctx.Err()
	}
	return json.NewEncoder(b.input).Encode(v)
}

func (b *agyACPBridge) promptLocked() {
	id := b.idLocked()
	b.prompts = append(b.prompts, id)
	b.active = true
	b.readyOnce.Do(func() { close(b.ready) })
	b.writeLocked(acpCall(id, "session/prompt", map[string]any{"sessionId": b.session, "prompt": []map[string]string{{"type": "text", "text": b.prompt}}}),
		"Antigravity did not accept the request input")
}

func (b *agyACPBridge) settledCh() <-chan struct{}     { return b.settled }
func (b *agyACPBridge) followupReady() <-chan struct{} { return b.ready }

// steer sends a correction into the running turn. It is taken only when agy
// hands the turn over to it; a refused prompt stays queued as a follow-up.
func (b *agyACPBridge) steer(ctx context.Context, id, text string) string {
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
	for _, s := range b.steers {
		if s.id == id {
			b.mu.Unlock()
			return steerUnknown
		}
	}
	s := &acpSteer{id: id, text: text, result: make(chan string, 1)}
	rpc := b.idLocked()
	b.steers[rpc] = s
	b.prompts = append(b.prompts, rpc) // before the write: the handover can come first
	turn := b.madeWrites
	b.madeWrites++
	b.writes.Add(1)
	b.mu.Unlock()
	call := acpCall(rpc, "session/prompt", map[string]any{"sessionId": b.session, "prompt": []map[string]string{{"type": "text", "text": text}}})
	go func() {
		defer b.writes.Done()
		agyACPBeforeWrite(call)
		if b.send(turn, call) != nil {
			b.mu.Lock()
			if b.steers[rpc] == s {
				delete(b.steers, rpc)
				b.dropPromptLocked(rpc)
				s.result <- steerUnknown
			}
			b.mu.Unlock()
		}
	}()
	select {
	case result := <-s.result:
		return result
	case <-ctx.Done():
	case <-b.ctx.Done():
	}
	b.mu.Lock()
	if b.steers[rpc] == s {
		delete(b.steers, rpc)
	}
	b.mu.Unlock()
	return steerUnknown
}

func (b *agyACPBridge) dropPromptLocked(id int64) {
	b.prompts = slices.DeleteFunc(b.prompts, func(p int64) bool { return p == id })
}

func (b *agyACPBridge) close() {
	b.closeOnce.Do(func() {
		b.mu.Lock()
		b.endLocked()
		b.mu.Unlock()
		_ = b.input.Close() // not behind writeMu: a blocked write must not hold it
		b.writes.Wait()
		close(b.closed)
	})
}

// finish ends a request whose process ended before its turn did.
func (b *agyACPBridge) finish() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.endLocked()
}

func (b *agyACPBridge) failLocked(why string) {
	if b.done {
		return
	}
	if b.failure == "" {
		b.failure = why
	}
	b.endLocked()
	_ = b.input.Close()
}

func (b *agyACPBridge) endLocked() {
	b.done, b.active = true, false
	b.settleOnce.Do(func() { close(b.settled) })
	for rpc, s := range b.steers {
		s.result <- steerUnknown
		delete(b.steers, rpc)
	}
}

func (b *agyACPBridge) Write(data []byte) (int, error) {
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
			b.mu.Lock()
			b.bad = true
			b.failLocked("")
			b.mu.Unlock()
			return len(data), nil
		}
		b.buf = append(b.buf, rest[:n]...)
		if i < 0 {
			break
		}
		if line := bytes.TrimSpace(b.buf); len(line) > 0 {
			b.event(line)
		}
		b.buf = nil
		rest = rest[i+1:]
	}
	return len(data), nil
}

type acpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (b *agyACPBridge) event(raw []byte) {
	var m struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
		Result json.RawMessage `json:"result"`
		Error  *acpError       `json:"error"`
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.done {
		return
	}
	if json.Unmarshal(raw, &m) != nil {
		b.bad = true
		b.failLocked("")
		return
	}
	hasID := len(m.ID) > 0 && !bytes.Equal(m.ID, []byte("null"))
	switch {
	case m.Method != "" && hasID:
		b.requestLocked(m.ID, m.Method, m.Params)
	case m.Method == "session/update":
		b.updateLocked(m.Params)
	case m.Method != "":
		// Other notifications carry no answer.
	case hasID:
		var id int64
		if json.Unmarshal(m.ID, &id) != nil {
			return // not one of this turn's requests
		}
		b.responseLocked(id, m.Result, m.Error)
	}
}

// requestLocked answers what agy asks of its client. Nothing is allowed on
// the owner's behalf: one permission is rejected once (never "always"), and
// any other question is refused; either way the request needs the owner.
func (b *agyACPBridge) requestLocked(id json.RawMessage, method string, params json.RawMessage) {
	reply := map[string]any{"jsonrpc": "2.0", "id": id}
	if method == "session/request_permission" {
		var p struct {
			Options []struct {
				OptionID string `json:"optionId"`
				Kind     string `json:"kind"`
			} `json:"options"`
		}
		_ = json.Unmarshal(params, &p)
		outcome := map[string]string{"outcome": "cancelled"}
		for _, o := range p.Options {
			if o.Kind == "reject_once" && o.OptionID != "" {
				outcome = map[string]string{"outcome": "selected", "optionId": o.OptionID}
				break
			}
		}
		b.denied = true
		reply["result"] = map[string]any{"outcome": outcome}
	} else {
		b.waiting = true
		reply["error"] = acpError{Code: -32601, Message: "AgentNet does not answer this; it needs the owner in their native setup"}
	}
	b.writeLocked(reply, "")
}

func (b *agyACPBridge) updateLocked(raw json.RawMessage) {
	var p struct {
		Session string `json:"sessionId"`
		Update  struct {
			Kind    string `json:"sessionUpdate"`
			Tool    string `json:"kind"`
			Content struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"update"`
	}
	if json.Unmarshal(raw, &p) != nil || !b.active || p.Session != b.session {
		return // a resumed conversation's replay, or not this conversation
	}
	switch p.Update.Kind {
	case "agent_message_chunk":
		if p.Update.Content.Type == "text" {
			if len(b.run) <= maxOutput {
				b.run += p.Update.Content.Text
				b.run = b.run[:min(len(b.run), maxOutput+1)] // the worker marks it cut off
			}
			if !b.answering {
				b.answering = true
				emitNativeActivity(b.onActivity, activityAnswer)
			}
		}
	case "tool_call":
		// Text before a tool call is commentary; the answer is the last text.
		if strings.TrimSpace(b.run) != "" {
			b.answer = b.run
		}
		b.run, b.answering = "", false
		emitNativeActivity(b.onActivity, acpToolActivity(p.Update.Tool))
	}
}

func acpToolActivity(kind string) nativeActivity {
	switch kind {
	case "read":
		return activityReading
	case "edit", "delete", "move":
		return activityEditing
	case "execute":
		return activityCommand
	case "search", "fetch":
		return activitySearch
	}
	return activityTool
}

func (b *agyACPBridge) responseLocked(id int64, result json.RawMessage, e *acpError) {
	switch {
	case id == b.initID && b.initID != 0:
		b.initID = 0
		if b.errorLocked(e, "Antigravity refused the protocol start") {
			return
		}
		var r struct {
			Version int `json:"protocolVersion"`
			Caps    struct {
				Load    bool `json:"loadSession"`
				Session struct {
					Resume *struct{} `json:"resume"`
				} `json:"sessionCapabilities"`
			} `json:"agentCapabilities"`
		}
		if json.Unmarshal(result, &r) != nil || r.Version != 1 {
			b.failLocked("Antigravity does not speak ACP protocol version 1")
			return
		}
		b.sessionID = b.idLocked()
		params := map[string]any{"cwd": b.dir, "mcpServers": []any{}}
		method := "session/new"
		if b.resume {
			params["sessionId"] = b.session
			switch {
			case r.Caps.Session.Resume != nil:
				method = "session/resume"
			case r.Caps.Load:
				method = "session/load" // its replay arrives before the prompt and is not read
			default:
				b.failLocked("Antigravity cannot continue its saved conversation " + b.session + " over ACP")
				return
			}
		}
		b.writeLocked(acpCall(b.sessionID, method, params), "Antigravity did not accept the conversation start")
	case id == b.sessionID && b.sessionID != 0:
		b.sessionID = 0
		why := "Antigravity could not start a conversation"
		if b.resume {
			why = "Antigravity could not continue its conversation " + b.session
		}
		if b.errorLocked(e, why) {
			return
		}
		var r struct {
			Session string `json:"sessionId"`
		}
		_ = json.Unmarshal(result, &r)
		switch {
		case b.resume && r.Session != "" && r.Session != b.session:
			b.bad = true
			b.failLocked("")
			return
		case !b.resume:
			if !validAgyConversation(r.Session) || b.session != "" && r.Session != b.session {
				b.bad = true
				b.failLocked("")
				return
			}
			b.session = r.Session
		}
		b.promptLocked()
	case slices.Contains(b.prompts, id):
		b.promptResponseLocked(id, result, e)
	}
}

// errorLocked ends the request on a refused setup step: sign-in is the
// owner's, anything else is a technical failure with agy's reason.
func (b *agyACPBridge) errorLocked(e *acpError, why string) bool {
	if e == nil {
		return false
	}
	msg := strings.ToLower(e.Message)
	if strings.Contains(msg, "authentication required") || strings.Contains(msg, "unauthenticated") || strings.Contains(msg, "not authenticated") {
		b.auth = true
		b.endLocked()
		_ = b.input.Close()
		return true
	}
	if m := clip([]byte(e.Message)); m != "" {
		why += ": " + m
	}
	b.failLocked(why)
	return true
}

func (b *agyACPBridge) promptResponseLocked(id int64, result json.RawMessage, e *acpError) {
	if s := b.steers[id]; s != nil && e != nil {
		// agy refused the correction (it may be waiting on a permission
		// answer): the turn goes on, the correction stays queued.
		delete(b.steers, id)
		b.dropPromptLocked(id)
		s.result <- steerUnsupported
		return
	}
	if e != nil {
		if b.errorLocked(e, "Antigravity refused the request") {
			return
		}
	}
	var r struct {
		Stop string `json:"stopReason"`
	}
	if json.Unmarshal(result, &r) != nil || r.Stop == "" {
		b.bad = true
		b.failLocked("")
		return
	}
	b.dropPromptLocked(id)
	if s := b.steers[id]; s != nil {
		// The correction's own reply: agy ran the turn with it.
		delete(b.steers, id)
		b.seen[s.id] = s.text
		s.result <- steerAccepted
	}
	if len(b.prompts) > 0 {
		// Not the turn's end while a prompt is still open: the request
		// settles only on its last outstanding prompt's reply.
		if i := slices.IndexFunc(b.prompts, func(p int64) bool { return p > id }); i >= 0 {
			// The handover: agy ended this prompt because a newer one joined
			// the turn. The newer prompt is taken; what was said before is not
			// its answer.
			if s := b.steers[b.prompts[i]]; s != nil {
				delete(b.steers, b.prompts[i])
				b.seen[s.id] = s.text
				s.result <- steerAccepted
			}
			b.run, b.answer, b.answering = "", "", false
			return
		}
		// A newer prompt answered before an older one: keep what it said
		// for the answer and wait for the rest of the turn.
		if strings.TrimSpace(b.run) != "" {
			b.answer = b.run
		}
		b.run, b.answering = "", false
		return
	}
	if strings.TrimSpace(b.run) != "" {
		b.answer = b.run
	}
	b.stop, b.complete = r.Stop, true
	b.endLocked()
	_ = b.input.Close()
}

// outcome is the request's terminal view in the stream transport's terms, so
// both transports report needs-you, sign-in and failure in the same words.
// failure is a technical reason that ended the run before any turn result.
func (b *agyACPBridge) outcome() (s agyStream, failure string, auth bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failure != "" && !b.bad {
		return agyStream{}, b.failure, false
	}
	s = agyStream{conversation: b.session, complete: b.complete, damaged: b.bad, denied: b.denied, response: b.answer}
	switch {
	case b.waiting:
		s.status = "WAITING"
	case b.stop == "end_turn":
		s.status = "SUCCESS"
	case b.stop == "cancelled":
		s.status = "CANCELED"
	default:
		s.status = strings.ToUpper(b.stop)
	}
	return s, "", b.auth
}

// conversation is the conversation this request runs in, for a stop.
func (b *agyACPBridge) conversation() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.session
}
