package testhub

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// WriteNativeScript preserves the original script's stdin, argv, $0, CWD and environment.
// The calling test package must expose TestNativeScriptFixtureProcess calling RunNativeScript.
func WriteNativeScript(bin, script, harness string, childEntry ...string) error {
	entry := "TestNativeScriptFixtureProcess"
	if len(childEntry) > 0 {
		entry = childEntry[0]
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if err = os.WriteFile(bin+".script", []byte(script), 0600); err != nil {
		return err
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	launcher := "#!/bin/sh\nAGENTNET_NATIVE_SCRIPT_HARNESS=" + quote(harness) + " AGENTNET_NATIVE_SCRIPT_FILE=\"$0.script\" exec " + quote(exe) + " -test.run '^" + entry + "$' -- \"$0\" \"$@\"\n"
	return os.WriteFile(bin, []byte(launcher), 0700)
}

// RunNativeScript is the owned test-binary subprocess entry point. It reads a single
// native request while stdin stays open, then runs the unchanged shell script.
func RunNativeScript(t *testing.T) {
	harness := os.Getenv("AGENTNET_NATIVE_SCRIPT_HARNESS")
	if harness == "" {
		t.Skip("owned synthetic subprocess only")
	}
	var args []string
	for i, arg := range os.Args {
		if arg == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	if len(args) == 0 {
		os.Exit(2)
	}
	dec, enc := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
	var request struct {
		Type    string          `json:"type"`
		Message json.RawMessage `json:"message"`
	}
	if dec.Decode(&request) != nil {
		os.Exit(3)
	}
	var prompt string
	session := protocol.NewID()
	if harness == "claude" {
		var message struct{ Role, Content string }
		if request.Type != "user" || json.Unmarshal(request.Message, &message) != nil || message.Role != "user" {
			os.Exit(4)
		}
		prompt = message.Content
		for i := 1; i+1 < len(args); i++ {
			if args[i] == "--session-id" || args[i] == "--resume" {
				session = args[i+1]
			}
		}
		_ = enc.Encode(map[string]string{"type": "system", "subtype": "init", "session_id": session})
		_ = enc.Encode(map[string]any{"type": "user", "uuid": protocol.NewID(), "isReplay": true, "parent_tool_use_id": nil, "session_id": session, "message": map[string]string{"role": "user", "content": prompt}})
	} else {
		if harness != "pi" || request.Type != "prompt" || json.Unmarshal(request.Message, &prompt) != nil {
			os.Exit(5)
		}
		_ = enc.Encode(map[string]string{"type": "agent_start"})
	}
	script, err := os.ReadFile(os.Getenv("AGENTNET_NATIVE_SCRIPT_FILE"))
	if err != nil {
		os.Exit(6)
	}
	cmd := exec.Command("sh", append([]string{"-c", string(script)}, args...)...)
	cmd.Stdin, cmd.Stderr = strings.NewReader(prompt), os.Stderr
	answer, err := cmd.Output()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		if exit, ok := err.(*exec.ExitError); ok {
			os.Exit(exit.ExitCode())
		}
		os.Exit(7)
	}
	if harness == "claude" {
		_ = enc.Encode(map[string]string{"type": "result", "subtype": "success", "session_id": session, "result": string(answer)})
	} else {
		_ = enc.Encode(map[string]any{"type": "message_end", "message": map[string]string{"role": "assistant", "content": string(answer), "stopReason": "stop"}})
		_ = enc.Encode(map[string]string{"type": "agent_end"})
		_ = enc.Encode(map[string]any{"type": "agent_settled", "aborted": false})
	}
	// Terminal native output makes the worker close its input, never vice versa.
	var unexpected any
	if dec.Decode(&unexpected) == nil {
		os.Exit(8)
	}
	os.Exit(0)
}
