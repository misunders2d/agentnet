package ui

import (
	"os"
	"strings"
	"testing"
)

func TestTopicTransferComicRendered(t *testing.T) {
	if os.Getenv("AGENTNET_SCREENSHOTS") == "" {
		t.Setenv("AGENTNET_SCREENSHOTS", t.TempDir())
	}
	out := browserCheck(t, "testdata/topictransfer_rendered.cjs")
	if strings.Count(out, "TOPIC TRANSFER RENDERED PASS ") != 6 {
		t.Fatalf("missing topic continuation surfaces:\n%s", out)
	}
	t.Log(out)
}
