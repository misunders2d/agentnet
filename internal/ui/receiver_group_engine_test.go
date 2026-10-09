//go:build linux

package ui

import (
	"bufio"
	"os/exec"
	"testing"
)

// Reuse the existing real Go group proof/carrier vectors, then test only the
// additive receiver source transition. No native executor/real browser claim.
func TestBrowserGroupReceiverPreparedEngine(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	cmd := exec.Command(node, "testdata/receiver_group_engine_check.mjs")
	w := &wireNode{t: t, stderr: &nodeOutput{}}
	cmd.Stderr = w.stderr
	w.in, err = cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.in.Close(); cmd.Wait() })
	w.out = bufio.NewScanner(out)
	w.out.Buffer(make([]byte, 4096), 8<<20)
	challenge, finish := groupEngineVectors(t, w.ok(map[string]any{"op": "setup"}))
	consent := w.ok(map[string]any{"op": "consent", "challenge": challenge})
	result := w.ok(map[string]any{"op": "checks", "vectors": finish(consent["consent"].(string))})
	if result["ok"] != true {
		t.Fatalf("group receiver transition: %+v", result)
	}
	t.Log(result["evidence"])
}
