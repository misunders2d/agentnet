package ui

import (
	"strings"
	"testing"
)

// A committed cross-site document must trade the token for a Strict cookie,
// keep its deep-link fragment, and leave no token-bearing history entry.
func TestTokenHandoffRendered(t *testing.T) {
	out := browserCheck(t, "testdata/token_handoff_browser_check.cjs", demoPage(t, ""))
	if !strings.Contains(out, "token handoff PASS") {
		t.Fatalf("no pass line: %s", out)
	}
	t.Log(out)
}
