package ui

import (
	"os/exec"
	"strings"
	"testing"
)

// The browser device's engine gives the new messenger what the daemon's
// page gives it (testdata/messenger_fields_engine_check.mjs): replies
// linked to the message as shown here, participation records' type and
// author, the chat list's guests, decisions and last record, and a device
// thread's agent.
func TestBrowserMessengerFieldsEngine(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	out, err := exec.Command(node, "testdata/messenger_fields_engine_check.mjs").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "PASS messenger fields engine checks") {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Logf("%s", out)
}
