package itest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// writeClaudeFixture adapts the existing one-shot shell stand-ins to Claude's
// streaming protocol. The script still gets the plain prompt, original argv,
// working directory, environment and $0, including its existing sidecar files.
func writeClaudeFixture(t *testing.T, bin, script string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin+".script", []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	quoted := "'" + strings.ReplaceAll(exe, "'", "'\"'\"'") + "'"
	launcher := "#!/bin/sh\nAGENTNET_ITEST_CLAUDE_SCRIPT=\"$0.script\" exec " + quoted + " -test.run '^TestClaudeFixtureProcess$' -- \"$0\" \"$@\"\n"
	if err := os.WriteFile(bin, []byte(launcher), 0o700); err != nil {
		t.Fatal(err)
	}
}

// TestClaudeFixtureProcess runs only as an owned synthetic harness subprocess.
func TestClaudeFixtureProcess(t *testing.T) {
	scriptPath := os.Getenv("AGENTNET_ITEST_CLAUDE_SCRIPT")
	if scriptPath == "" {
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
	session := protocol.NewID()
	for i := 1; i+1 < len(args); i++ {
		if args[i] == "--session-id" || args[i] == "--resume" {
			session = args[i+1]
		}
	}
	dec, enc := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
	var request struct {
		Type    string `json:"type"`
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
	}
	if dec.Decode(&request) != nil || request.Type != "user" || request.Message.Role != "user" {
		os.Exit(3)
	}
	script, err := os.ReadFile(scriptPath)
	if err != nil {
		os.Exit(4)
	}
	if enc.Encode(map[string]string{"type": "system", "subtype": "init", "session_id": session}) != nil ||
		enc.Encode(map[string]any{"type": "user", "uuid": protocol.NewID(), "isReplay": true, "parent_tool_use_id": nil, "session_id": session, "message": request.Message}) != nil {
		os.Exit(5)
	}
	cmd := exec.Command("sh", append([]string{"-c", string(script)}, args...)...)
	cmd.Stdin, cmd.Stderr = strings.NewReader(request.Message.Content), os.Stderr
	answer, err := cmd.Output()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(6)
	}
	if enc.Encode(map[string]string{"type": "result", "subtype": "success", "session_id": session, "result": string(answer)}) != nil {
		os.Exit(7)
	}
	// The worker closes input after the terminal event. Never need EOF first.
	var unexpected any
	if dec.Decode(&unexpected) == nil {
		os.Exit(8)
	}
	os.Exit(0)
}

func TestClaudeFixtureAdapter(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stand-in responder is a shell script")
	}
	for _, mode := range []string{"--session-id", "--resume", "outbox"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			bin := filepath.Join(dir, "claude")
			prompt, answer := "first line\nsecond line", "first\nemotion: happy\n"
			script := "cat > \"$0.prompt\"\nprintf '%s\\n' \"$@\" > \"$0.args\"\npwd > \"$0.cwd\"\necho run >> \"$0.runs\"\nprintf 'first\\nemotion: happy\\n'\n"
			if mode == "outbox" {
				input, out := filepath.Join(dir, "notes.bin"), filepath.Join(dir, "out")
				if err := os.WriteFile(input, []byte("unchanged attachment"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(out, 0o700); err != nil {
					t.Fatal(err)
				}
				prompt = fmt.Sprintf("- \"notes.bin\" (read only): %q\nplace them directly in %q\n", input, out)
				script, answer = stubOutboxClaude, "report written\n"
			}
			writeClaudeFixture(t, bin, script)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			session := "4bb2c764-62b3-4ce6-bb46-8e1dd46f803d"
			flag := mode
			if mode == "outbox" {
				flag = "--session-id"
			}
			args := []string{flag, session, "--input-format", "stream-json", "--output-format", "stream-json"}
			cmd := exec.CommandContext(ctx, bin, args...)
			cmd.Dir = dir
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			input, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			output, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			if err := json.NewEncoder(input).Encode(map[string]any{"type": "user", "message": map[string]string{"role": "user", "content": prompt}}); err != nil {
				t.Fatal(err)
			}
			dec := json.NewDecoder(output)
			for _, typ := range []string{"system", "user", "result"} {
				var event struct {
					Type, Result string
					Session      string `json:"session_id"`
					Replay       bool   `json:"isReplay"`
					Message      struct{ Role, Content string }
				}
				if err := dec.Decode(&event); err != nil {
					t.Fatalf("%s before stdin EOF: %v", typ, err)
				}
				if event.Type != typ || event.Session != session || typ == "result" && event.Result != answer ||
					typ == "user" && (!event.Replay || event.Message.Role != "user" || event.Message.Content != prompt) {
					t.Fatalf("%s event: %+v", typ, event)
				}
			}
			input.Close()
			if err := cmd.Wait(); err != nil {
				t.Fatalf("fixture: %v\n%s", err, &stderr)
			}
			got, err := os.ReadFile(bin + ".prompt")
			if err != nil || strings.TrimSuffix(string(got), "\n") != strings.TrimSuffix(prompt, "\n") {
				t.Fatalf("prompt: %q, %v", got, err)
			}
			if mode == "outbox" {
				got, err := os.ReadFile(filepath.Join(dir, "out", "x.bin"))
				if err != nil || string(got) != "unchanged attachment" {
					t.Fatalf("outbox copy: %q, %v", got, err)
				}
			} else {
				for suffix, want := range map[string]string{".args": strings.Join(args, "\n") + "\n", ".cwd": dir + "\n", ".runs": "run\n"} {
					got, err := os.ReadFile(bin + suffix)
					if err != nil || string(got) != want {
						t.Fatalf("%s: %q, %v; want %q", suffix, got, err, want)
					}
				}
			}
		})
	}
}
