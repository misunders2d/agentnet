package ui

import (
	"os/exec"
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
