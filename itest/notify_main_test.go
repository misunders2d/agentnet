package itest

import (
	"os"
	"testing"
)

// Tests never show real desktop notifications, and the binaries they run are
// not inside the developer's assistant session (no reply returns to it), nor
// inside an agent run, whose guard refuses sends (a worker sets it for its own
// runs).
func TestMain(m *testing.M) {
	os.Setenv("AGENTNET_NOTIFY", "off")
	os.Unsetenv("CLAUDE_CODE_SESSION_ID")
	os.Unsetenv("CODEX_THREAD_ID")
	os.Unsetenv("AGENTNET_BACKGROUND")
	os.Exit(m.Run())
}
