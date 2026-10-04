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

// BUG-32: the pi responder gets the decrypted request on its standard
// input, never on its command line, which every local user can read
// (/proc/PID/cmdline). The harness is a stand-in named pi (no model).
func TestPiPromptNotOnCommandLine(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stand-in")
	}
	bin := t.TempDir()
	log := filepath.Join(bin, "log")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" > '" + log + ".args'\ncat > '" + log + ".stdin'\necho 'stub answer'\n"
	if err := os.WriteFile(filepath.Join(bin, "pi"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	w := newWorld(t, "")
	setResponder(t, w.bob, "pi", t.TempDir(), time.Minute)
	if err := w.bob.Approve(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	q, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "SECRET-TOKEN-4242: which key opens the vault?", Kind: envelope.KindQuestion})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, q.ID, stateAnswered)
	args, err := os.ReadFile(log + ".args")
	if err != nil {
		t.Fatal(err)
	}
	stdin, _ := os.ReadFile(log + ".stdin")
	if strings.Contains(string(args), "SECRET-TOKEN-4242") {
		t.Fatalf("the request is on pi's command line: %s", args)
	}
	if !strings.Contains(string(stdin), "SECRET-TOKEN-4242: which key opens the vault?") {
		t.Fatalf("pi did not get the request on stdin: %q", stdin)
	}
}
