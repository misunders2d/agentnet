package ui

import (
	"strings"
	"testing"
)

func TestBrowserBackRendered(t *testing.T) {
	out := browserCheck(t, "testdata/browserback_rendered.cjs", demoPage(t, ""))
	if !strings.Contains(out, "browser Back PASS") {
		t.Fatalf("no complete Back proof: %s", out)
	}
}
