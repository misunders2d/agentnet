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
	out = browserCheck(t, "testdata/browserback_limits_rendered.cjs", demoPage(t, ""))
	if !strings.Contains(out, "browser Back limits PASS") {
		t.Fatalf("no complete proof of Back at history's limits: %s", out)
	}
}
