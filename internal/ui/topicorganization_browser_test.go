package ui

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestBrowserTopicOrganization(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	if out, err := exec.Command(node, "testdata/topicorganization_engine_check.mjs").CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	} else {
		t.Log(string(out))
	}
}

func TestTopicOrganizationComicRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in: set AGENTNET_PLAYWRIGHT to an installed playwright-core")
	}
	cmd := exec.CommandContext(t.Context(), "node", "testdata/chattopics_rendered.cjs")
	cmd.Env = append(os.Environ(), "P11_ORGANIZE_ONLY=1", "P11_SKINS=comic", "AGENTNET_SCREENSHOTS="+t.TempDir())
	if out, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(out), "ORGANIZE PASS") {
		t.Fatalf("%v\n%s", err, out)
	} else {
		t.Log(string(out))
	}
}
