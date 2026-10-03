package itest

import (
	"os"
	"testing"
)

// Tests never show real desktop notifications, and the binaries they run are
// not inside the developer's assistant session (no reply returns to it).
func TestMain(m *testing.M) {
	os.Setenv("AGENTNET_NOTIFY", "off")
	os.Unsetenv("CLAUDE_CODE_SESSION_ID")
	os.Unsetenv("CODEX_THREAD_ID")
	os.Exit(m.Run())
}
