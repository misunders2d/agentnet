package client

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

const consoleProbe = "AGENTNET_CONSOLE_PROBE"

// Natively: a program started by startInOwnConsole has the new console as
// its standard input and output (GetConsoleMode succeeds on both), so an
// interactive agentnet open can read and write there. The child is this
// test binary, running TestConsoleProbeChild only.
func TestOwnConsoleGivesConsoleStdio(t *testing.T) {
	result := filepath.Join(t.TempDir(), "probe")
	t.Setenv(consoleProbe, result)
	if err := startInOwnConsole([]string{os.Args[0], "-test.run=^TestConsoleProbeChild$"}, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		data, err := os.ReadFile(result)
		if err == nil && len(data) > 0 {
			if got := strings.TrimSpace(string(data)); got != "console console" {
				t.Fatalf("the child's standard input and output: %s", got)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the child did not report")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestConsoleProbeChild is the child side of the test above.
func TestConsoleProbeChild(t *testing.T) {
	out := os.Getenv(consoleProbe)
	if out == "" {
		t.Skip("child of TestOwnConsoleGivesConsoleStdio only")
	}
	kind := func(f *os.File) string {
		var mode uint32
		if err := windows.GetConsoleMode(windows.Handle(f.Fd()), &mode); err != nil {
			return "not-console(" + err.Error() + ")"
		}
		return "console"
	}
	os.WriteFile(out, []byte(kind(os.Stdin)+" "+kind(os.Stdout)), 0o600)
}
