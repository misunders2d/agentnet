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
	b := newCodexRoomBridge(ctx, cmd, "PRIVATE_PROMPT", &output, func(ctx context.Context, raw json.RawMessage) (string, error) {
		calls <- string(raw)
		return "CORRELATED_REPLY", nil
	})
	defer b.close()
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
	for _, forbidden := range []string{"disabledPluginIds", "config", "baseInstructions", "developerInstructions", "sandbox", "sandboxPolicy", "approvalPolicy"} {
		if _, ok := p[forbidden]; ok {
			t.Fatal(forbidden)
		}
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

func TestCodexRoomBridgeFailsClosed(t *testing.T) {
	for _, event := range []string{`{"id":2,"result":{"thread":{}}}`, `{"id":4,"method":"item/tool/call","params":{"threadId":"foreign","turnId":"turn","tool":"agentnet_room","callId":"call"}}`, `{"id":5,"method":"item/commandExecution/requestApproval","params":{}}`, `{"id":6,"method":"item/tool/call","params":{"threadId":"thread","turnId":"turn","tool":"other_tool","callId":"call"}}`, `{"id":1,"error":{"message":"unsupported"}}`} {
		t.Run(event, func(t *testing.T) {
			in, out := io.Pipe()
			defer in.Close()
			var output bytes.Buffer
			b := &codexRoomBridge{input: out, output: &output, thread: "thread", turn: "turn", calls: map[string]bool{}, call: func(context.Context, json.RawMessage) (string, error) { t.Error("unauthorized call"); return "", nil }}
			b.event([]byte(event))
			b.finish()
			if !strings.HasPrefix(output.String(), needsHumanMarker+"\n") || !strings.Contains(output.String(), codexRoomUnavailable) {
				t.Fatal(output.String())
			}
		})
	}
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
	for _, bad := range []string{`{"action":"task","pid":"x","text":"forbidden"}`, `{"action":"ask","pid":"x","text":"foreign"}`, `{"action":"wait","id":"x","extra":"forbidden"}`, `{"action":"ask","pid":"x","text":"foreign"} {}`} {
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
		}
		if d.Decode(&m) != nil {
			os.Exit(0)
		}
		switch m.Method {
		case "initialize":
			if mode == "eof" {
				os.Exit(0)
			}
			if mode == "unsupported" {
				enc.Encode(map[string]any{"id": m.ID, "error": map[string]any{"code": -32601, "message": "unsupported"}})
				continue
			}
			enc.Encode(map[string]any{"id": m.ID, "result": map[string]any{}})
		case "thread/start":
			enc.Encode(map[string]any{"id": m.ID, "result": map[string]any{"thread": map[string]string{"id": "thread"}, "approvalPolicy": "never", "sandbox": map[string]any{"type": "readOnly", "networkAccess": false}}})
		case "turn/start":
			enc.Encode(map[string]any{"id": m.ID, "result": map[string]any{"turn": map[string]string{"id": "turn"}}})
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
			b := newCodexRoomBridge(ctx, cmd, "bound prompt", &output, func(ctx context.Context, _ json.RawMessage) (string, error) {
				called <- struct{}{}
				if mode == "cancel" {
					<-ctx.Done()
					return "", ctx.Err()
				}
				return "CORRELATED", nil
			})
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
			} else if !strings.HasPrefix(output.String(), needsHumanMarker+"\n") {
				t.Fatal(output.String())
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
			if !strings.HasPrefix(output.String(), needsHumanMarker+"\n") || strings.Contains(output.String(), codexRoomUnavailable) || !strings.Contains(output.String(), "native approval") {
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
	b.event([]byte(`{"id":2,"result":{"thread":{"id":"exact"},"model":"native-model"}}`))
	if got := b.modelReport(); got != "native-model" {
		t.Fatal(got)
	}
	b.event([]byte(`{"method":"thread/settings/updated","params":{"threadId":"foreign","threadSettings":{"model":"foreign-model"}}}`))
	if got := b.modelReport(); got != "native-model" {
		t.Fatal(got)
	}
	b.event([]byte(`{"method":"thread/settings/updated","params":{"threadId":"exact","threadSettings":{"model":"new-native-model"}}}`))
	if got := b.modelReport(); got != "new-native-model" {
		t.Fatal(got)
	}
	b.event([]byte(`{"method":"thread/settings/updated","params":{"threadId":"exact","threadSettings":{"model":"bad\nmodel"}}}`))
	if got := b.modelReport(); got != "new-native-model" {
		t.Fatal(got)
	}
}
