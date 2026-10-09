package ui

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestFlatPersonTopicProjection(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node unavailable")
	}
	out, err := exec.CommandContext(t.Context(), "node", "testdata/person_topics_check.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Log(string(out))
}

func TestPersonTopicsAllSkinsRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in: set AGENTNET_PLAYWRIGHT to an installed playwright-core")
	}
	cmd := exec.CommandContext(t.Context(), "node", "testdata/person_topics_rendered.cjs")
	cmd.Env = os.Environ()
	if os.Getenv("AGENTNET_SCREENSHOTS") == "" {
		cmd.Env = append(cmd.Env, "AGENTNET_SCREENSHOTS="+t.TempDir())
	}
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "person topics comic 390 PASS") || !strings.Contains(string(out), "person topics zoom 390 PASS") {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Log(string(out))
}

func TestPersonTopicUnreadRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in: set AGENTNET_PLAYWRIGHT to an installed playwright-core")
	}
	cmd := exec.CommandContext(t.Context(), "node", "testdata/person_topics_rendered.cjs")
	cmd.Env = append(os.Environ(), "AGENTNET_TOPIC_UNREAD=1")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "topic unread comic 390 PASS") || !strings.Contains(string(out), "topic unread zoom 390 PASS") {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Log(string(out))
}
