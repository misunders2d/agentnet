package ui

import (
	"os/exec"
	"strings"
	"testing"
)

// Node checks of host and Comic modules that need no browser: the Android
// shell bridge (host.android, onBack, app actions, export) and Comic's
// Android send journal. They run with every UI test run.
func TestNodeModuleChecks(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	for _, check := range [][]string{
		{"static/android_host_test.mjs"},
		{"--test", "web/src/send-journal.test.mjs"},
	} {
		out, err := exec.Command(node, check...).CombinedOutput()
		if err != nil {
			t.Errorf("%s: %v\n%s", strings.Join(check, " "), err, out)
		}
		t.Logf("%s\n%s", strings.Join(check, " "), out)
	}
}
