package client

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// The real worker talks to this subprocess through the native app-server wire
// contract. It never starts Codex, uses credentials or opens a network socket.
func installCodexSessionStub(t *testing.T, log string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell subprocess fixture")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTNET_CODEX_SESSION_EXE", exe)
	t.Setenv("AGENTNET_CODEX_SESSION_FIXTURE", "1")
	t.Setenv("STUB_LOG", log)
	t.Setenv("STUB_THREAD", "fresh")
	path := filepath.Join(t.TempDir(), "codex")
	script := "#!/bin/sh\nprintf 'run %s\\n' \"$*\" >> \"$STUB_LOG\"\nexec \"$AGENTNET_CODEX_SESSION_EXE\" -test.run '^TestCodexSessionFixtureProcess$'\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCodexSessionFixtureProcess(t *testing.T) {
	if os.Getenv("AGENTNET_CODEX_SESSION_FIXTURE") != "1" {
		t.Skip("subprocess fixture")
	}
	d, enc := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
	thread := os.Getenv("STUB_THREAD")
	for {
		var m struct {
			ID     json.RawMessage            `json:"id"`
			Method string                     `json:"method"`
			Params map[string]json.RawMessage `json:"params"`
		}
		if d.Decode(&m) != nil {
			os.Exit(0)
		}
		for _, key := range []string{"config", "sandbox", "sandboxPolicy", "baseInstructions", "developerInstructions", "disabledPluginIds"} {
			if _, present := m.Params[key]; present {
				os.Exit(31)
			}
		}
		if m.Method == "thread/start" || m.Method == "thread/resume" {
			if string(m.Params["approvalPolicy"]) != `"on-request"` || string(m.Params["approvalsReviewer"]) != `"auto_review"` {
				os.Exit(38)
			}
		} else if m.Params["approvalPolicy"] != nil || m.Params["approvalsReviewer"] != nil {
			os.Exit(39)
		}
		switch m.Method {
		case "initialize":
			enc.Encode(map[string]any{"id": m.ID, "result": map[string]any{}})
		case "thread/start", "thread/resume":
			var id string
			json.Unmarshal(m.Params["threadId"], &id)
			if m.Method == "thread/resume" {
				thread = id
			} else if thread == "fresh" {
				thread = newUUID()
			}
			f, err := os.OpenFile(os.Getenv("STUB_LOG")+".rpc", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
			if err != nil {
				os.Exit(32)
			}
			fmt.Fprintln(f, m.Method, id)
			f.Close()
			enc.Encode(map[string]any{"id": m.ID, "result": map[string]any{"thread": map[string]string{"id": thread}, "approvalPolicy": "on-request"}})
		case "turn/start":
			var input []struct {
				Text string `json:"text"`
			}
			json.Unmarshal(m.Params["input"], &input)
			if len(input) != 1 {
				os.Exit(33)
			}
			prompt := input[0].Text
			if err := os.WriteFile(os.Getenv("STUB_LOG")+".prompt", []byte(prompt), 0o600); err != nil {
				os.Exit(34)
			}
			if os.Getenv("STUB_WRITE_OUTPUT") == "1" {
				match := regexp.MustCompile(`place them directly in "([^"]+)"`).FindStringSubmatch(prompt)
				if len(match) != 2 {
					os.Exit(35)
				}
				if err := os.WriteFile(filepath.Join(match[1], "x.txt"), []byte("CODEX_FILE\n"), 0o600); err != nil {
					os.Exit(36)
				}
			}
			enc.Encode(map[string]any{"id": m.ID, "result": map[string]any{"turn": map[string]string{"id": "turn"}}})
			if n, _ := strconv.Atoi(os.Getenv("STUB_NOISE")); n > 0 {
				enc.Encode(map[string]any{"method": "item/commandExecution/outputDelta", "params": map[string]any{"threadId": thread, "turnId": "turn", "delta": strings.Repeat("n", n)}})
			}
			mode := os.Getenv("STUB_TURN")
			answer := os.Getenv("STUB_NATIVE_ANSWER")
			if answer == "" {
				answer = "codex native answer"
			}
			if mode != "" {
				answer = "partial commentary"
			}
			if mode != "empty" {
				enc.Encode(map[string]any{"method": "item/completed", "params": map[string]any{"threadId": thread, "turnId": "turn", "item": map[string]string{"type": "agentMessage", "text": answer}}})
			}
			if mode == "cut" {
				os.Exit(0)
			}
			if mode == "approval" {
				enc.Encode(map[string]any{"id": "permission", "method": "item/commandExecution/requestApproval", "params": map[string]any{"threadId": thread, "turnId": "turn", "reason": "fixture native permission"}})
				// A native approval must never be fabricated by this client.
				if d.Decode(new(any)) == nil {
					os.Exit(37)
				}
				os.Exit(0)
			}
			turn := map[string]any{"id": "turn", "status": "completed"}
			if mode == "fail" {
				turn = map[string]any{"id": "turn", "status": "failed", "error": map[string]string{"message": "fixture provider error"}}
			}
			enc.Encode(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": thread, "turn": turn}})
		}
	}
}
