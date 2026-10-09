package ui

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestPreviewTopicRoutesAllSkinsRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in: set AGENTNET_PLAYWRIGHT")
	}
	cmd := exec.CommandContext(t.Context(), "node", "testdata/person_topics_rendered.cjs")
	cmd.Env = append(os.Environ(), "AGENTNET_PREVIEW_ROUTE=1", "AGENTNET_TOPIC_UNREAD=1")
	if os.Getenv("AGENTNET_SCREENSHOTS") == "" {
		cmd.Env = append(cmd.Env, "AGENTNET_SCREENSHOTS="+t.TempDir())
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, skin := range []string{"comic", "classic", "zoom"} {
		for _, width := range []string{"1280", "390"} {
			if !strings.Contains(string(out), "preview route "+skin+" "+width+" PASS") {
				t.Fatalf("missing rendered result\n%s", out)
			}
		}
	}
	t.Log(string(out))
}
