package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Re-executed synthetic protocol fixture only; no installed native executable,
// model, provider, global configuration or live AgentNet data is used.
func TestNativeAdapterProtocolProcess(t *testing.T) {
	name := os.Getenv("AGENTNET_ADAPTER_PROTOCOL_FIXTURE")
	if name == "" {
		t.Skip("owned synthetic subprocess only")
	}
	dec, enc := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
	var initial map[string]any
	if dec.Decode(&initial) != nil {
		os.Exit(2)
	}
	if name == "claude" {
		text := initial["message"].(map[string]any)["content"].(string)
		_ = enc.Encode(claudeAdapterUser(text))
	} else {
		_ = enc.Encode(map[string]string{"type": "agent_start"})
	}
	var correction map[string]any
	if dec.Decode(&correction) != nil {
		os.Exit(3)
	}
	if name == "claude" {
		if correction["type"] != "user" {
			os.Exit(4)
		}
		text := correction["message"].(map[string]any)["content"].(string)
		_ = enc.Encode(claudeAdapterUser(text))
		_ = enc.Encode(map[string]string{"type": "result", "session_id": "owned", "result": "Synthetic corrected final"})
	} else {
		if correction["type"] != "steer" {
			os.Exit(5)
		}
		_ = enc.Encode(map[string]any{"type": "response", "id": correction["id"], "command": "steer", "success": true})
		_ = enc.Encode(map[string]any{"type": "message_start", "messageId": "native-msg", "message": map[string]any{"role": "user", "steering": true, "attribution": "user", "content": correction["message"]}})
		_ = enc.Encode(map[string]any{"type": "message_end", "message": map[string]string{"role": "assistant", "content": "Synthetic corrected final", "stopReason": "stop"}})
		_ = enc.Encode(map[string]string{"type": "agent_end"})
		_ = enc.Encode(map[string]any{"type": "prompt_result", "id": initial["id"], "status": "completed", "sessionSettled": true})
	}
	// Runtime bridge closes stdin only after recording its one terminal answer.
	var unexpected any
	if dec.Decode(&unexpected) == nil {
		os.Exit(6)
	}
	os.Exit(0)
}
func TestNativeAdapterOwnedProcessRoundTrip(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("Unix fixture launcher")
	}
	for _, name := range []string{"claude", "omp"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			exe, e := os.Executable()
			if e != nil {
				t.Fatal(e)
			}
			file := filepath.Join(t.TempDir(), "native-fixture")
			if e = os.WriteFile(file, []byte("#!/bin/sh\nexec \"$AGENTNET_ADAPTER_TEST_EXE\" -test.run '^TestNativeAdapterProtocolProcess$'\n"), 0700); e != nil {
				t.Fatal(e)
			}
			cmd := exec.CommandContext(ctx, file, "-p", "--output-format", "text")
			cmd.Env = append(os.Environ(), "AGENTNET_ADAPTER_TEST_EXE="+exe, "AGENTNET_ADAPTER_PROTOCOL_FIXTURE="+name)
			var out bytes.Buffer
			var bridge nativeFollowupBridge
			var finish, closeBridge func()
			if name == "claude" {
				b, e := newClaudeStreamBridge(ctx, cmd, "Original", &out, "owned")
				if e != nil {
					t.Fatal(e)
				}
				bridge = b
				finish = b.finish
				closeBridge = b.close
			} else {
				b, e := newPiRPCBridge(ctx, cmd, "Original", &out, "omp")
				if e != nil {
					t.Fatal(e)
				}
				bridge = b
				finish = b.finish
				closeBridge = b.close
			}
			defer closeBridge()
			if e = cmd.Start(); e != nil {
				t.Fatal(e)
			}
			select {
			case <-bridge.followupReady():
			case <-ctx.Done():
				t.Fatal("owned startup", ctx.Err())
			}
			if result := bridge.steer(ctx, "correction", "Use English"); result != steerAccepted {
				t.Fatal(result)
			}
			if e = cmd.Wait(); e != nil {
				t.Fatal(e)
			}
			finish()
			closeBridge()
			if strings.TrimSpace(out.String()) != "Synthetic corrected final" {
				t.Fatal(fmt.Sprintf("one final answer required: %q", out.String()))
			}
		})
	}
}
