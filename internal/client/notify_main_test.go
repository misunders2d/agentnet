package client

import (
	"os"
	"testing"
)

// Tests never show real desktop notifications or open terminals; the
// tests of notification clicks set their own stand-in launcher.
func TestMain(m *testing.M) {
	os.Setenv("AGENTNET_NOTIFY", "off")
	terminalLauncher = ""
	os.Exit(m.Run())
}
