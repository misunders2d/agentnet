package client

import (
	"encoding/json"
	"os"
	"testing"
)

// Tests never show real desktop notifications or open terminals; the
// tests of notification clicks set their own stand-in launcher.
func TestMain(m *testing.M) {
	// A copy of this binary named claude stands in for a native Claude
	// ancestor: it captures its own route and reports it (nativecompat_test).
	if sid := os.Getenv("AGENTNET_TEST_CAPTURE_CLAUDE_ROUTE"); sid != "" {
		r, e := captureClaudeRoute(sid)
		msg := ""
		if e != nil {
			msg = e.Error()
		}
		json.NewEncoder(os.Stdout).Encode(map[string]any{"route": r, "error": msg})
		os.Exit(0)
	}
	os.Setenv("AGENTNET_NOTIFY", "off")
	terminalLauncher = ""
	os.Exit(m.Run())
}
