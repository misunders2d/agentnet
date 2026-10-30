package client

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/misunders2d/agentnet/internal/envelope"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCodexRoomBridgeBoundTransport(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var output bytes.Buffer
	calls := make(chan string, 1)
	cmd := exec.Command("codex")
	cmd.Dir = t.TempDir()
	b, bridgeErr := newCodexRoomBridge(ctx, cmd, "PRIVATE_PROMPT", &output, func(ctx context.Context, raw json.RawMessage) (string, error) {
		calls <- string(raw)
		return "CORRELATED_REPLY", nil
	})
	if bridgeErr != nil {
		t.Fatal(bridgeErr)
	}
	defer b.close()
	defer cmd.Stdin.(io.Closer).Close()
	messages := make(chan map[string]any, 10)
	go func() {
		d := json.NewDecoder(cmd.Stdin)
		for {
			var m map[string]any
			if d.Decode(&m) != nil {
				return
			}
			messages <- m
		}
	}()
	next := func() map[string]any {
		t.Helper()
		select {
		case m := <-messages:
			return m
		case <-time.After(time.Second):
			t.Fatal("missing protocol message")
			return nil
		}
	}
	event := func(s string) {
		t.Helper()
		if _, e := b.Write([]byte(s + "\n")); e != nil {
			t.Fatal(e)
		}
	}
	if strings.Join(cmd.Args, " ") != cmd.Path+` app-server` {
		t.Fatal(cmd.Args)
	}
	if next()["method"] != "initialize" {
		t.Fatal("initialize")
	}
	event(`{"id":1,"result":{}}`)
	if next()["method"] != "initialized" {
		t.Fatal("initialized")
	}
	start := next()
	p := start["params"].(map[string]any)
	if p["cwd"] != cmd.Dir || p["ephemeral"] != true {
		t.Fatal(p)
	}
	for _, forbidden := range []string{"disabledPluginIds", "config", "baseInstructions", "developerInstructions", "sandbox", "sandboxPolicy"} {
		if _, ok := p[forbidden]; ok {
			t.Fatal(forbidden)
		}
	}
	if p["approvalPolicy"] != "on-request" || p["approvalsReviewer"] != "auto_review" {
		t.Fatal(p)
	}
	spec := p["dynamicTools"].([]any)[0].(map[string]any)
	schema := spec["inputSchema"].(map[string]any)
	kind, _ := json.Marshal(schema["properties"].(map[string]any)["kind"])
	if string(kind) != `{"enum":["question","task"],"type":"string"}` || len(schema["required"].([]any)) != 1 || schema["required"].([]any)[0] != "action" || !strings.Contains(spec["description"].(string), "recipient consent") {
		t.Fatalf("room kind schema/consent: %s %v", kind, spec)
	}
	event(`{"id":2,"result":{"thread":{"id":"thread"},"approvalPolicy":"on-request","sandbox":{"type":"workspaceWrite","networkAccess":true}}}`)
	turn := next()["params"].(map[string]any)
	if turn["approvalPolicy"] != nil || turn["sandboxPolicy"] != nil || turn["sandbox"] != nil || turn["config"] != nil || turn["threadId"] != "thread" {
		t.Fatal(turn)
	}
	event(`{"id":3,"result":{"turn":{"id":"turn"}}}`)
	tool := `{"id":"tool1","method":"item/tool/call","params":{"threadId":"thread","turnId":"turn","tool":"agentnet_room","callId":"call1","arguments":{"action":"wait","id":"exact"}}}`
	event(tool)
	select {
	case args := <-calls:
		if args != `{"action":"wait","id":"exact"}` {
			t.Fatal(args)
		}
	case <-time.After(time.Second):
		t.Fatal("tool not called")
	}
	result := next()
	if result["id"] != "tool1" || result["result"].(map[string]any)["success"] != true {
		t.Fatal(result)
	}
	event(tool)
	if next()["result"].(map[string]any)["success"] != false {
		t.Fatal("duplicate accepted")
	}
	event(`{"method":"item/completed","params":{"threadId":"other","turnId":"turn","item":{"type":"agentMessage","text":"FOREIGN"}}}`)
	event(`{"method":"item/completed","params":{"threadId":"thread","turnId":"turn","item":{"type":"agentMessage","text":"ANSWER"}}}`)
	event(`{"method":"turn/completed","params":{"threadId":"thread","turn":{"id":"turn","status":"completed"}}}`)
	b.finish()
	if output.String() != "ANSWER" {
		t.Fatal(output.String())
	}
}

// Every technical bridge failure fails the run with its exact reason and no
// NEEDS-HUMAN marker; only a genuine native wait asks the owner (oct23 T2).
func TestCodexRoomBridgeFailsClosed(t *testing.T) {
	for _, c := range []struct{ event, want string }{
		{`{"id":2,"result":{"thread":{}}}`, "Codex returned no usable session"},
		{`{"id":4,"method":"item/tool/call","params":{"threadId":"foreign","turnId":"turn","tool":"agentnet_room","callId":"call"}}`, `unexpected tool call "agentnet_room"`},
		{`{"id":5,"method":"item/commandExecution/requestApproval","params":{}}`, "item/commandExecution/requestApproval for another turn; nothing was approved"},
		{`{"id":6,"method":"item/tool/call","params":{"threadId":"thread","turnId":"turn","tool":"other_tool","callId":"call"}}`, `unexpected tool call "other_tool"`},
		{`{"id":1,"error":{"code":-32601,"message":"unsupported"}}`, "Codex refused initialize: unsupported (code -32601)"},
		{`{"id":2,"error":{"code":-32600,"message":"no rollout found for thread"}}`, "Codex refused thread start: no rollout found for thread (code -32600)"},
		{`{"method":"turn/completed","params":{"threadId":"thread","turn":{"id":"turn","status":"failed","error":{"message":"stream disconnected before completion"}}}}`, `Codex ended the turn as "failed": stream disconnected before completion`},
		{`{"method":"turn/completed","params":{"threadId":"thread","turn":{"id":"turn","status":"completed"}}}`, "Codex completed the turn without an answer"},
		{`{"id":7,"method":"account/chatgptAuthTokens/refresh","params":{}}`, `unsupported request "account/chatgptAuthTokens/refresh"; nothing was approved`},
		{`{`, "Codex sent an unreadable protocol line"},
	} {
		t.Run(c.event, func(t *testing.T) {
			in, out := io.Pipe()
			defer in.Close()
			go io.Copy(io.Discard, in)
			var output bytes.Buffer
			b := &codexRoomBridge{ctx: t.Context(), input: out, output: &output, thread: "thread", turn: "turn", calls: map[string]bool{}, call: func(context.Context, json.RawMessage) (string, error) { t.Error("unauthorized call"); return "", nil }}
			b.event([]byte(c.event))
			b.finish()
			if output.Len() != 0 || !strings.Contains(b.failureText(), c.want) {
				t.Fatalf("output %q failure %q", output.String(), b.failureText())
			}
		})
	}
	t.Run("eof", func(t *testing.T) {
		for thread, want := range map[string]string{"": "Codex exited before starting a session", "thread": "Codex ended before the turn completed"} {
			in, out := io.Pipe()
			in.Close()
			var output bytes.Buffer
			b := &codexRoomBridge{ctx: t.Context(), input: out, output: &output, thread: thread, calls: map[string]bool{}}
			b.finish()
			if output.Len() != 0 || b.failureText() != want {
				t.Fatalf("output %q failure %q", output.String(), b.failureText())
			}
		}
	})
	t.Run("session-owned-needs-human", func(t *testing.T) {
		in, out := io.Pipe()
		defer in.Close()
		go io.Copy(io.Discard, in)
		var output bytes.Buffer
		b := &codexRoomBridge{ctx: t.Context(), input: out, output: &output, calls: map[string]bool{}, threadAvailable: func(string) bool { return false }}
		b.event([]byte(`{"id":2,"result":{"thread":{"id":"owned"}}}`))
		b.finish()
		if b.failureText() != "" || !strings.HasPrefix(output.String(), needsHumanMarker+"\n"+nativeStatusPrefix+"The native session is already owned") {
			t.Fatalf("output %q failure %q", output.String(), b.failureText())
		}
	})
}

func TestCodexRoomToolQuestionAndCancellation(t *testing.T) {
	stub := installAgentStub(t)
	w, _, packet, _ := groupTurnsFixture(t)
	conv := packet.State.Conv
	if e := w.bob.SetResponder(&Responder{Harness: "agentstub", Dir: stub.dir}); e != nil {
		t.Fatal(e)
	}
	if e := w.bob.Approve(w.alice.Address); e != nil {
		t.Fatal(e)
	}
	from := p6Member(t, w.alice, w.alice, conv)
	to := p6Member(t, w.alice, w.bob, conv)
	eventually(t, "source scope", func() bool { return stateAt(t, w.bob, from.PID).Claimable() })
	root, e := w.alice.AskAgent(tctx(t), from.PID, envelope.KindQuestion, "running source")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = w.alice.store.db.Exec(`UPDATE inbox SET state=? WHERE id=?`, stateRunning, root.LID); e != nil {
		t.Fatal(e)
	}
	j := job{Kind: envelope.KindQuestion, PID: from.PID, Conv: conv}
	if !w.alice.codexRoomEligible(j, &Responder{Harness: "codex"}, sessionPlan{}) {
		t.Fatal("active Codex room question not selected")
	}
	j.Kind = envelope.KindTask
	if w.alice.codexRoomEligible(j, &Responder{Harness: "codex"}, sessionPlan{}) {
		t.Fatal("task execution changed")
	}
	raw, _ := json.Marshal(map[string]string{"action": "ask", "pid": to.PID, "text": "native bridge question"})
	reply, e := w.alice.codexRoomTool(tctx(t), root.LID, raw)
	if e != nil || !strings.Contains(reply, "from agent participation "+to.PID) {
		t.Fatalf("%s %v", reply, e)
	}
	taskRaw, _ := json.Marshal(map[string]string{"action": "ask", "kind": envelope.KindTask, "pid": to.PID, "text": "native task needs recipient consent"})
	taskCtx, cancel := context.WithCancel(tctx(t))
	defer cancel()
	type toolResult struct {
		text string
		err  error
	}
	done := make(chan toolResult, 1)
	beforeRuns := stub.runs()
	go func() { text, err := w.alice.codexRoomTool(taskCtx, root.LID, taskRaw); done <- toolResult{text, err} }()
	var child, childLID string
	eventually(t, "native task awaits recipient consent", func() bool {
		select {
		case result := <-done:
			t.Fatalf("task tool ended before consent: %s %v", result.text, result.err)
		default:
		}
		return w.bob.store.db.QueryRow(`SELECT id,lid FROM inbox WHERE conv=? AND pid=? AND reply_to=? AND kind='task' AND state=?`, conv, to.PID, root.LID, stateAwaiting).Scan(&child, &childLID) == nil
	})
	if stub.runs() != beforeRuns {
		t.Fatal("native task ran before recipient consent")
	}
	if e = w.bob.Accept(child); e != nil {
		t.Fatal(e)
	}
	select {
	case result := <-done:
		if result.err != nil || !strings.Contains(result.text, "reply "+childLID+" from agent participation "+to.PID) || stub.runs() != beforeRuns+1 {
			t.Fatalf("task correlation/result: %s %v runs=%d", result.text, result.err, stub.runs())
		}
	case <-taskCtx.Done():
		t.Fatal(taskCtx.Err())
	}
	for _, kind := range []string{"", envelope.KindQuestion} {
		explicit, _ := json.Marshal(map[string]string{"action": "ask", "kind": kind, "pid": to.PID, "text": "explicit native question"})
		if reply, e := w.alice.codexRoomTool(tctx(t), root.LID, explicit); e != nil || !strings.Contains(reply, "from agent participation "+to.PID) {
			t.Fatalf("question kind %q: %s %v", kind, reply, e)
		}
	}
	for _, bad := range []string{`{"action":"task","pid":"x","text":"forbidden"}`, `{"action":"ask","pid":"x","text":"foreign"}`, `{"action":"wait","id":"x","extra":"forbidden"}`, `{"action":"ask","pid":"x","text":"foreign"} {}`, `{"action":"ask","kind":"message","pid":"` + to.PID + `","text":"invalid kind"}`, `{"action":"wait","kind":"question","id":"` + child + `"}`, `{"action":"organize","kind":"task"}`, `{"action":"react","kind":"question"}`, `{"action":"send","kind":"task"}`} {
		if _, e = w.alice.codexRoomTool(tctx(t), root.LID, json.RawMessage(bad)); e == nil {
			t.Fatal("accepted", bad)
		}
	}
	if _, e = w.alice.store.db.Exec(`UPDATE inbox SET state=? WHERE id=?`, stateCancelled, root.LID); e != nil {
		t.Fatal(e)
	}
	if _, e = w.alice.codexRoomTool(tctx(t), root.LID, raw); e == nil {
		t.Fatal("cancelled cause sent new question")
	}
	if _, e = w.alice.codexRoomTool(tctx(t), root.LID, taskRaw); e == nil {
		t.Fatal("cancelled cause sent new task")
	}
}

// Re-executed test helper: no Codex installation, model, credentials or socket.
func TestCodexRoomFixtureProcess(t *testing.T) {
	mode := os.Getenv("AGENTNET_CODEX_ROOM_FIXTURE")
	if mode == "" {
		t.Skip("subprocess fixture")
	}
	d := json.NewDecoder(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	for {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
		}
		if d.Decode(&m) != nil {
			os.Exit(0)
		}
		switch m.Method {
		case "initialize":
			if mode == "native-approval" {
				f, e := os.OpenFile(os.Getenv("AGENTNET_NATIVE_PROCESS_LOG"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
				if e != nil {
					os.Exit(2)
				}
				_, _ = f.WriteString("started\n")
				_ = f.Close()
			}
			if mode == "eof" {
				os.Exit(0)
			}
			if mode == "unsupported" {
				enc.Encode(map[string]any{"id": m.ID, "error": map[string]any{"code": -32601, "message": "unsupported"}})
				continue
			}
			enc.Encode(map[string]any{"id": m.ID, "result": map[string]any{}})
		case "thread/start":
			policy := "never"
			if mode == "native-approval" {
				policy = "on-request"
			}
			enc.Encode(map[string]any{"id": m.ID, "result": map[string]any{"thread": map[string]string{"id": "thread"}, "approvalPolicy": policy, "sandbox": map[string]any{"type": "readOnly", "networkAccess": false}}})
		case "turn/start":
			enc.Encode(map[string]any{"id": m.ID, "result": map[string]any{"turn": map[string]string{"id": "turn"}}})
			if mode == "native-approval" {
				cwd, _ := os.Getwd()
				command := "write exact native fixture result"
				enc.Encode(map[string]any{"method": "item/started", "params": map[string]any{"threadId": "thread", "turnId": "turn", "item": map[string]string{"id": "native-item", "type": "commandExecution", "command": command, "cwd": cwd, "status": "inProgress"}}})
				enc.Encode(map[string]any{"id": "native-exact-rpc", "method": "item/commandExecution/requestApproval", "params": map[string]any{"threadId": "thread", "turnId": "turn", "itemId": "native-item", "approvalId": "native-distinct-approval", "command": command, "cwd": cwd}})
				continue
			}
			if mode == "steer" {
				continue
			}
			enc.Encode(map[string]any{"id": "tool", "method": "item/tool/call", "params": map[string]any{"threadId": "thread", "turnId": "turn", "tool": "agentnet_room", "callId": "call", "arguments": map[string]string{"action": "wait", "id": "fixture"}}})
		case "turn/steer":
			var p struct {
				ThreadID       string                        `json:"threadId"`
				ExpectedTurnID string                        `json:"expectedTurnId"`
				ClientID       string                        `json:"clientUserMessageId"`
				Input          []struct{ Type, Text string } `json:"input"`
			}
			if json.Unmarshal(m.Params, &p) != nil || p.ThreadID != "thread" || p.ExpectedTurnID != "turn" || p.ClientID == "" || len(p.Input) != 1 || !strings.Contains(p.Input[0].Text, "Use English instead") {
				os.Exit(2)
			}
			enc.Encode(map[string]any{"id": m.ID, "result": map[string]string{"turnId": "turn"}})
			enc.Encode(map[string]any{"method": "item/completed", "params": map[string]any{"threadId": "thread", "turnId": "turn", "item": map[string]string{"type": "agentMessage", "text": "OWNED_CORRECTION_ACCEPTED"}}})
			enc.Encode(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "thread", "turn": map[string]string{"id": "turn", "status": "completed"}}})
		case "":
			if mode == "native-approval" && string(m.ID) == `"native-exact-rpc"` {
				var r struct {
					Decision string `json:"decision"`
				}
				if json.Unmarshal(m.Result, &r) != nil || r.Decision != "accept" {
					os.Exit(3)
				}
				if os.WriteFile(os.Getenv("AGENTNET_NATIVE_RESULT"), []byte("ONE_NATIVE_OPERATION"), 0600) != nil {
					os.Exit(2)
				}
				enc.Encode(map[string]any{"method": "serverRequest/resolved", "params": map[string]any{"threadId": "thread", "requestId": "native-exact-rpc"}})
				enc.Encode(map[string]any{"method": "item/completed", "params": map[string]any{"threadId": "thread", "turnId": "turn", "item": map[string]string{"type": "agentMessage", "text": "ONE_NATIVE_OPERATION"}}})
				enc.Encode(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "thread", "turn": map[string]string{"id": "turn", "status": "completed"}}})
				continue
			}
			if string(m.ID) == `"tool"` {
				enc.Encode(map[string]any{"method": "item/completed", "params": map[string]any{"threadId": "thread", "turnId": "turn", "item": map[string]string{"type": "agentMessage", "text": "NATIVE_REPLY"}}})
				enc.Encode(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "thread", "turn": map[string]string{"id": "turn", "status": "completed"}}})
			}
		}
	}
}

func TestCodexRoomExecutableLifecycle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix executable wrapper fixture")
	}
	exe, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	wrapper := filepath.Join(t.TempDir(), "codex-fixture")
	if e = os.WriteFile(wrapper, []byte("#!/bin/sh\nexec \"$AGENTNET_CODEX_FIXTURE_EXE\" -test.run '^TestCodexRoomFixtureProcess$'\n"), 0700); e != nil {
		t.Fatal(e)
	}
	for _, mode := range []string{"success", "unsupported", "eof", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, wrapper)
			cmd.Dir = t.TempDir()
			cmd.Env = append(cmd.Environ(), "AGENTNET_CODEX_FIXTURE_EXE="+exe, "AGENTNET_CODEX_ROOM_FIXTURE="+mode)
			var output bytes.Buffer
			called := make(chan struct{}, 1)
			b, bridgeErr := newCodexRoomBridge(ctx, cmd, "bound prompt", &output, func(ctx context.Context, _ json.RawMessage) (string, error) {
				called <- struct{}{}
				if mode == "cancel" {
					<-ctx.Done()
					return "", ctx.Err()
				}
				return "CORRELATED", nil
			})
			if bridgeErr != nil {
				t.Fatal(bridgeErr)
			}
			defer b.close()
			cmd.WaitDelay = time.Second
			if e = cmd.Start(); e != nil {
				t.Fatal(e)
			}
			if mode == "cancel" {
				select {
				case <-called:
					cancel()
				case <-ctx.Done():
					t.Fatal("never called")
				}
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case <-done:
			case <-time.After(6 * time.Second):
				t.Fatal("process did not terminate")
			}
			b.finish()
			if mode == "success" {
				if output.String() != "NATIVE_REPLY" {
					t.Fatal(output.String())
				}
			} else if mode == "unsupported" {
				if output.Len() != 0 || !strings.Contains(b.failureText(), "Codex refused initialize: unsupported (code -32601)") {
					t.Fatalf("output %q failure %q", output.String(), b.failureText())
				}
			} else if output.Len() != 0 || b.failureText() == "" {
				t.Fatalf("output %q failure %q", output.String(), b.failureText())
			}
		})
	}
}

func TestCodexRoomBridgeNativeApprovalNeedsHuman(t *testing.T) {
	for _, method := range []string{"item/commandExecution/requestApproval", "item/fileChange/requestApproval", "item/permissions/requestApproval", "item/tool/requestUserInput", "mcpServer/elicitation/request"} {
		t.Run(method, func(t *testing.T) {
			in, out := io.Pipe()
			defer in.Close()
			wire := make(chan string, 1)
			go func() { data, _ := io.ReadAll(in); wire <- string(data) }()
			var output bytes.Buffer
			b := &codexRoomBridge{ctx: context.Background(), input: out, output: &output, thread: "thread", turn: "turn", calls: map[string]bool{}, call: func(context.Context, json.RawMessage) (string, error) {
				t.Error("approval executed room call")
				return "", nil
			}}
			raw, _ := json.Marshal(map[string]any{"id": "approval", "method": method, "params": map[string]any{"threadId": "thread", "turnId": "turn", "itemId": "item"}})
			b.event(raw)
			b.finish()
			if !strings.HasPrefix(output.String(), needsHumanMarker+"\n"+nativeStatusPrefix) || b.failureText() != "" || !strings.Contains(output.String(), "native approval") {
				t.Fatal(output.String())
			}
			select {
			case got := <-wire:
				if got != "" {
					t.Fatalf("native decision without owner: %s", got)
				}
			case <-time.After(time.Second):
				t.Fatal("approval did not release input")
			}
		})
	}
}

func TestCodexRoomBridgeSteerExactTurn(t *testing.T) {
	for _, mode := range []string{"accepted", "unsupported", "wrong-turn", "error", "complete-first", "cancel", "completed-before"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			in, out := io.Pipe()
			defer in.Close()
			var output bytes.Buffer
			b := &codexRoomBridge{ctx: ctx, input: out, output: &output, thread: "owned-thread", turn: "owned-turn", calls: map[string]bool{}}
			defer b.close()
			if mode == "completed-before" {
				b.event([]byte(`{"method":"item/completed","params":{"threadId":"owned-thread","turnId":"owned-turn","item":{"type":"agentMessage","text":"done"}}}`))
				b.event([]byte(`{"method":"turn/completed","params":{"threadId":"owned-thread","turn":{"id":"owned-turn","status":"completed"}}}`))
				if got := b.steer(ctx, "correction", "Use English"); got != steerQueued {
					t.Fatal(got)
				}
				return
			}
			wire := make(chan map[string]any, 1)
			go func() { var request map[string]any; _ = json.NewDecoder(in).Decode(&request); wire <- request }()
			done := make(chan string, 1)
			go func() { done <- b.steer(ctx, "exact-correction", "Use English") }()
			var request map[string]any
			select {
			case request = <-wire:
			case <-time.After(time.Second):
				t.Fatal("no native steer")
			}
			if request["method"] != "turn/steer" {
				t.Fatal(request)
			}
			params := request["params"].(map[string]any)
			if params["threadId"] != "owned-thread" || params["expectedTurnId"] != "owned-turn" || params["clientUserMessageId"] != "exact-correction" {
				t.Fatal(params)
			}
			if len(params) != 4 {
				t.Fatalf("native settings overridden: %v", params)
			}
			encoded, _ := json.Marshal(params["input"])
			if string(encoded) != `[{"text":"Use English","type":"text"}]` {
				t.Fatal(string(encoded))
			}
			expected := steerUnknown
			var response map[string]any
			switch mode {
			case "accepted", "complete-first":
				response = map[string]any{"id": request["id"], "result": map[string]string{"turnId": "owned-turn"}}
				expected = steerAccepted
			case "unsupported":
				response = map[string]any{"id": request["id"], "error": map[string]any{"code": -32601, "message": "method unavailable"}}
				expected = steerUnsupported
			case "wrong-turn":
				response = map[string]any{"id": request["id"], "result": map[string]string{"turnId": "other-turn"}}
			case "error":
				response = map[string]any{"id": request["id"], "error": map[string]any{"code": -32000, "message": "uncertain failure"}}
			case "cancel":
				cancel()
			}
			if mode == "complete-first" {
				b.event([]byte(`{"method":"item/completed","params":{"threadId":"owned-thread","turnId":"owned-turn","item":{"type":"agentMessage","text":"done"}}}`))
				b.event([]byte(`{"method":"turn/completed","params":{"threadId":"owned-thread","turn":{"id":"owned-turn","status":"completed"}}}`))
			}
			if response != nil {
				raw, _ := json.Marshal(response)
				b.event(raw)
			}
			select {
			case got := <-done:
				if got != expected {
					t.Fatalf("got %s want %s", got, expected)
				}
			case <-time.After(time.Second):
				t.Fatal("missing correlated outcome")
			}
			b.stateMu.Lock()
			ended := b.done
			b.stateMu.Unlock()
			if ended && mode != "complete-first" {
				t.Fatal("steer rejection killed the original run")
			}
		})
	}
}

func TestCodexRoomBridgeModelReportBound(t *testing.T) {
	in, out := io.Pipe()
	defer in.Close()
	go io.Copy(io.Discard, in)
	var output bytes.Buffer
	b := &codexRoomBridge{ctx: context.Background(), input: out, output: &output, calls: map[string]bool{}}
	defer b.close()
	for _, tt := range []struct{ event, model, reasoning string }{
		{`{"id":2,"result":{"thread":{"id":"exact"},"model":"native-model","reasoningEffort":"xhigh"}}`, "native-model", "xhigh"},
		{`{"method":"thread/settings/updated","params":{"threadId":"foreign","threadSettings":{"model":"foreign-model","effort":"low"}}}`, "native-model", "xhigh"},
		{`{"method":"thread/settings/updated","params":{"threadId":"exact","threadSettings":{"model":"new-native-model","effort":"medium"}}}`, "new-native-model", "medium"},
		{`{"method":"thread/settings/updated","params":{"threadId":"exact","threadSettings":{"model":"bad\nmodel","effort":"high"}}}`, "new-native-model", "medium"},
		{`{"method":"thread/settings/updated","params":{"threadId":"exact","threadSettings":{"model":"new-native-model","effort":null}}}`, "new-native-model", ""},
		{`{"method":"thread/settings/updated","params":{"threadId":"exact","threadSettings":{"model":"new-native-model","effort":"high"}}}`, "new-native-model", "high"},
		{`{"method":"thread/settings/updated","params":{"threadId":"exact","threadSettings":{"model":"changed-model"}}}`, "changed-model", ""},
		{`{"method":"thread/settings/updated","params":{"threadId":"exact","threadSettings":{"model":"changed-model","effort":"bad\neffort"}}}`, "changed-model", ""},
	} {
		b.event([]byte(tt.event))
		got := b.modelReport()
		if got.Model != tt.model || got.Reasoning != tt.reasoning {
			t.Fatalf("%s: %+v", tt.event, got)
		}
	}
}
