package a2abind

import (
	"os"
	"testing"
)

// Tests never show real desktop notifications.
func TestMain(m *testing.M) {
	os.Setenv("AGENTNET_NOTIFY", "off")
	os.Exit(m.Run())
}
