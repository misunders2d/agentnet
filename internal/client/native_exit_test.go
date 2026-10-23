package client

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
)

// Native events come from an owned shell stand-in, never an installed model.
// Every technical end fails the run with its exact reason and the exit status;
// the harness's stderr stays local. Only a native approval or input wait
// asks the owner, in AgentNet's own words (oct23 T2).
func testNativeProcessExitClassification(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("owned shell fixture")
	}
	for _, harness := range []string{"claude", "pi", "omp"} {
		t.Run(harness, func(t *testing.T) {
			name := map[string]string{"claude": "Claude", "pi": "Pi", "omp": "OMP"}[harness]
			for _, mode := range []string{"exit-only", "started-exit", "approval-exit", "provider-exit", "malformed-exit", "incomplete-clean", "partial-line-clean"} {
				t.Run(mode, func(t *testing.T) {
					bin := t.TempDir()
					script := "#!/bin/sh\nif [ \"$1\" = \"--help\" ]; then echo \"--print --no-session --extension\"; exit 0; fi\nsid=fixture-session; prev=; for a in \"$@\"; do [ \"$prev\" = --session-id ] && sid=$a; prev=$a; done\nIFS= read -r request\nprintf run >> \"$NATIVE_EXIT_RUN_LOG\"\necho LOCAL_STDERR_DETAIL >&2\n"
					want, reason := stateJobFailed, name+" ended before its request settled (exit status 1)"
					switch mode {
					case "exit-only":
						script += "exit 1\n"
					case "started-exit":
						if harness == "claude" {
							script += "printf '%s\\n' '{\"type\":\"system\",\"subtype\":\"init\"}'\n"
						} else {
							script += "printf '%s\\n' '{\"type\":\"agent_start\"}'\n"
						}
						script += "exit 1\n"
					case "approval-exit":
						if harness == "claude" {
							script += "printf '%s\\n' '{\"type\":\"control_request\"}'\n"
						} else {
							script += "printf '%s\\n' '{\"type\":\"extension_ui_request\",\"method\":\"confirm\"}'\n"
						}
						script += "exit 1\n"
						want, reason = stateNeedHuman, nativeStatusPrefix
					case "provider-exit":
						if harness == "claude" {
							script += "printf '%s\\n' \"{\\\"type\\\":\\\"system\\\",\\\"subtype\\\":\\\"init\\\",\\\"session_id\\\":\\\"$sid\\\"}\" \"{\\\"type\\\":\\\"result\\\",\\\"session_id\\\":\\\"$sid\\\",\\\"is_error\\\":true,\\\"result\\\":\\\"API Error: 529 overloaded\\\"}\"\n"
							reason = "Claude reported an error: API Error: 529 overloaded (exit status 1)"
						} else {
							script += "printf '%s\\n' '{\"type\":\"response\",\"id\":\"agentnet-initial\",\"success\":false,\"error\":\"No API key found for provider\"}'\n"
							reason = name + " refused the request: No API key found for provider (exit status 1)"
						}
						script += "exit 1\n"
					case "malformed-exit":
						script += "printf '%s\\n' '{'\nexit 1\n"
						reason = name + " sent an unreadable protocol line; no answer was used (exit status 1)"
					case "incomplete-clean":
						script += "exit 0\n"
						reason = name + " ended before its request settled"
					case "partial-line-clean":
						script += "printf '%s' '{'\nexit 0\n"
						reason = name + " ended before its request settled"
					}
					if err := os.WriteFile(filepath.Join(bin, harness), []byte(script), 0700); err != nil {
						t.Fatal(err)
					}
					t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
					runLog := filepath.Join(bin, "runs")
					t.Setenv("NATIVE_EXIT_RUN_LOG", runLog)
					w := newWorld(t, "")
					setResponder(t, w.bob, harness, t.TempDir(), time.Minute)
					if err := w.bob.Approve(w.alice.Address); err != nil {
						t.Fatal(err)
					}
					runAgent(t, w.alice)
					runAgent(t, w.bob)
					sent, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "synthetic native exit classification", Kind: envelope.KindQuestion})
					if err != nil {
						t.Fatal(err)
					}
					var got string
					eventually(t, "native process terminal state", func() bool {
						got, _ = w.bob.store.jobState(sent.ID)
						return got == stateJobFailed || got == stateNeedHuman || got == stateAnswered
					})
					if got != want {
						t.Fatalf("native %s/%s state=%s want=%s", harness, mode, got, want)
					}
					data, err := os.ReadFile(runLog)
					if err != nil || string(data) != "run" {
						t.Fatalf("owned process ran more than once: bytes=%d err=%v", len(data), err)
					}
					row := inboxRow(t, w.bob, sent.ID)
					if want == stateNeedHuman {
						if !strings.HasPrefix(row.Detail, reason) {
							t.Fatalf("native wait not in AgentNet's words: %q", row.Detail)
						}
						return
					}
					var reply Message
					eventually(t, "failure reply", func() bool { var ok bool; reply, ok = findReply(w.alice, sent.ID); return ok })
					if wantBody := harness + " failed: " + reason; reply.Status != envelope.StatusFailed || reply.Body != wantBody || row.Detail != wantBody {
						t.Fatalf("got %s %q (detail %q), want %q", reply.Status, reply.Body, row.Detail, wantBody)
					}
				})
			}
		})
	}
}
