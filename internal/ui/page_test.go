package ui

import (
	"os/exec"
	"strings"
	"testing"
)

// The page's own logic (drafts per conversation, one send at a time, dialogs
// that confirm once, trust naming the compared key) runs in node against a
// stand-in DOM: testdata/page_check.js.
func TestPageLogic(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	out, err := exec.Command(node, "testdata/page_check.js").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

// The service worker (static/sw.js) in node with a stand-in worker scope:
// TestStreamFraming checks the signed stream's framing (the vendored
// eventsource-parser as engine.mjs drives it) against the relay's real
// frames and the SSE grammar: testdata/sse_check.mjs.
func TestStreamFraming(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	out, err := exec.Command(node, "testdata/sse_check.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(string(out), "stream framing ok") {
		t.Fatalf("%s", out)
	}
}

// testdata/sw_check.js.
func TestServiceWorker(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	out, err := exec.Command(node, "testdata/sw_check.js").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

// The vendored QR encoder with a full-size new-device link:
// testdata/qr_check.mjs.
func TestQRCode(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	out, err := exec.Command(node, "testdata/qr_check.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}
