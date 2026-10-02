package ui

import (
	"os/exec"
	"strings"
	"testing"
)

func TestReplySessionCatalogQualifiedHarnesses(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	out, err := exec.Command(node, "testdata/receiver_catalog_check.cjs").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "receiver catalog validator ok") {
		t.Fatalf("%v\n%s", err, out)
	}
}
