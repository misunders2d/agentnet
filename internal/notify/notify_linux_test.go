package notify

import "testing"

// Without a desktop session nothing is started and the reason is returned.
func TestHeadlessLinux(t *testing.T) {
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "")
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("PATH", "") // a notifier must not be found or run either way
	if err := Show("AgentNet", "test"); err == nil {
		t.Fatal("headless Show succeeded")
	}
}
