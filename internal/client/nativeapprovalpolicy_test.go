package client

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

type nativePolicyWriteCloser struct{ *bytes.Buffer }

func (nativePolicyWriteCloser) Close() error { return nil }
func TestCodexAutomaticReviewerPreservesSandbox(t *testing.T) {
	for _, resume := range []string{"", "owned-thread"} {
		t.Run("resume="+resume, func(t *testing.T) {
			var wire, output bytes.Buffer
			b := &codexRoomBridge{ctx: context.Background(), input: nativePolicyWriteCloser{&wire}, output: &output, cwd: "/workspace", resume: resume}
			b.event([]byte(`{"id":1,"result":{}}`))
			dec := json.NewDecoder(&wire)
			for {
				var m struct {
					Method string         `json:"method"`
					Params map[string]any `json:"params"`
				}
				err := dec.Decode(&m)
				if err == io.EOF {
					t.Fatal("missing thread request")
				}
				if err != nil {
					t.Fatal(err)
				}
				if m.Method != "thread/start" && m.Method != "thread/resume" {
					continue
				}
				p := m.Params
				if p["approvalPolicy"] != "on-request" || p["approvalsReviewer"] != "auto_review" {
					t.Fatalf("requested native automatic review omitted: %v", p)
				}
				for _, k := range []string{"sandbox", "sandboxPolicy", "config", "baseInstructions", "developerInstructions", "disabledPluginIds"} {
					if _, ok := p[k]; ok {
						t.Fatalf("native configuration changed: %s", k)
					}
				}
				if resume != "" && p["threadId"] != resume {
					t.Fatal("owned thread changed")
				}
				break
			}
		})
	}
}

func TestHarnessNativeAutomaticModes(t *testing.T) {
	h := Harnesses["claude"]
	for _, args := range [][]string{h.question, h.task, createArgs(h, h.question, "owned"), resumeArgs(h, "question", h.question, "owned")} {
		at := slices.Index(args, "--permission-mode")
		if at < 0 || at+1 >= len(args) || args[at+1] != "auto" {
			t.Errorf("Claude missing native auto mode: %v", args)
		}
	}
	h = Harnesses["omp"]
	for _, args := range [][]string{h.question, h.task, createArgs(h, h.question, "owned"), resumeArgs(h, "question", h.question, "owned")} {
		if !slices.Contains(args, "--auto-approve") {
			t.Errorf("OMP missing native auto approval: %v", args)
		}
	}
	for name, h := range Harnesses {
		for _, arg := range append(slices.Clone(h.question), h.task...) {
			if strings.Contains(arg, "dangerously") || arg == "--yolo" || arg == "--yes" {
				t.Errorf("%s injected bypass or invented flag: %s", name, arg)
			}
		}
	}
	for _, name := range []string{"pi", "agy"} {
		for _, arg := range Harnesses[name].question {
			if arg == "--auto-approve" || arg == "--approve" || arg == "--mode" {
				t.Errorf("%s injected unproven approval/trust mode", name)
			}
		}
	}
}
func TestAutomaticModesSurviveNativeTransport(t *testing.T) {
	t.Run("omp", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		cmd := exec.Command("unused", append(slices.Clone(Harnesses["omp"].question), "--extension", "native-extension")...)
		var output bytes.Buffer
		b, err := newPiRPCBridge(ctx, cmd, "fixture", &output, "omp")
		if err != nil {
			t.Fatal(err)
		}
		var initial map[string]any
		if err := json.NewDecoder(cmd.Stdin).Decode(&initial); err != nil {
			t.Fatal(err)
		}
		defer cmd.Stdin.(io.Closer).Close()
		defer b.close()
		if !slices.Contains(cmd.Args, "--auto-approve") || !slices.Contains(cmd.Args, "native-extension") || slices.Contains(cmd.Args, "-p") || !slices.Contains(cmd.Args, "rpc") {
			t.Fatal(cmd.Args)
		}
	})
	t.Run("claude", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		cmd := exec.Command("unused", Harnesses["claude"].question...)
		var output bytes.Buffer
		b, err := newClaudeStreamBridge(ctx, cmd, "fixture", &output, "")
		if err != nil {
			t.Fatal(err)
		}
		var initial map[string]any
		if err := json.NewDecoder(cmd.Stdin).Decode(&initial); err != nil {
			t.Fatal(err)
		}
		defer cmd.Stdin.(io.Closer).Close()
		defer b.close()
		at := slices.Index(cmd.Args, "--permission-mode")
		if at < 0 || at+1 >= len(cmd.Args) || cmd.Args[at+1] != "auto" {
			t.Fatal(cmd.Args)
		}
	})
}
