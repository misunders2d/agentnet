package testhub

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestNativeScriptFixtureProcess(t *testing.T) { RunNativeScript(t) }

func TestScriptAdapterPreservesFailedExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stand-in")
	}
	bin := filepath.Join(t.TempDir(), "claude")
	if err := WriteNativeScript(bin, "cat >/dev/null\nexit 17\n", "claude"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin)
	cmd.Stdin = strings.NewReader(`{"type":"user","message":{"role":"user","content":"failed request"}}` + "\n")
	out, err := cmd.Output()
	exit, ok := err.(*exec.ExitError)
	if !ok || exit.ExitCode() != 17 || bytes.Contains(out, []byte(`"type":"result"`)) {
		t.Fatalf("script failure changed: output=%s, err=%v", out, err)
	}
}

func TestScriptAdapterBeforeInputEOF(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stand-in")
	}
	for _, harness := range []string{"claude", "pi"} {
		t.Run(harness, func(t *testing.T) {
			dir := t.TempDir()
			physicalDir, err := filepath.EvalSymlinks(dir)
			if err != nil {
				t.Fatal(err)
			}
			bin := filepath.Join(dir, harness)
			script := "cat > \"$0.prompt\"\nprintf '%s\\n' \"$@\" > \"$0.args\"\npwd > \"$0.cwd\"\nprintf '%s' \"$AGENTNET_BACKGROUND\" > \"$0.environment\"\nprintf 'answer\\n'\n"
			if err := WriteNativeScript(bin, script, harness); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, bin, "--session-id", "owned")
			cmd.Dir, cmd.Env = dir, append(os.Environ(), "AGENTNET_BACKGROUND=owned-worker")
			input, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			output, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			prompt := "private request\nsecond line"
			initial := map[string]any{"type": "prompt", "message": prompt}
			if harness == "claude" {
				initial = map[string]any{"type": "user", "message": map[string]string{"role": "user", "content": prompt}}
			}
			if err := json.NewEncoder(input).Encode(initial); err != nil {
				t.Fatal(err)
			}
			dec, found := json.NewDecoder(output), false
			for !found {
				var event map[string]any
				if err := dec.Decode(&event); err != nil {
					t.Fatalf("before EOF: %v", err)
				}
				if event["type"] == "result" {
					found = event["result"] == "answer\n" && event["session_id"] == "owned"
				}
				if event["type"] == "message_end" {
					found = event["message"].(map[string]any)["content"] == "answer\n"
				}
			}
			input.Close()
			if err := cmd.Wait(); err != nil {
				t.Fatalf("%v: %s", err, &stderr)
			}
			for suffix, want := range map[string]string{".prompt": prompt, ".args": "--session-id\nowned\n", ".cwd": physicalDir + "\n", ".environment": "owned-worker", ".script": script} {
				got, err := os.ReadFile(bin + suffix)
				if err != nil || string(got) != want {
					t.Fatalf("%s: %q, %v; want %q", suffix, got, err, want)
				}
			}
			if strings.Contains(string(stderr.Bytes()), "PASS") {
				t.Fatalf("test output leaked: %s", &stderr)
			}
		})
	}
}
