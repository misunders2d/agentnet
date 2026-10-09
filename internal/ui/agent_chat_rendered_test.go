package ui

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestAgentChatStarterRendered(t *testing.T) {
	testAgentChatRendered(t, false)
}

func TestOwnAgentModelStatusRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in: set AGENTNET_PLAYWRIGHT to an installed playwright-core")
	}
	cmd := exec.CommandContext(t.Context(), "node", "testdata/agent_chat_rendered.cjs")
	cmd.Env = append(os.Environ(), "AGENTNET_MODEL_REPORT=1", "AGENTNET_SCREENSHOTS="+t.TempDir())
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), `"ok":true`) {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Logf("%s", out)
}

func TestAssignSelectedMessageRendered(t *testing.T) {
	testAgentChatRendered(t, true)
}

func testAgentChatRendered(t *testing.T, assignment bool) {
	t.Helper()
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in: set AGENTNET_PLAYWRIGHT to an installed playwright-core")
	}
	cmd := exec.CommandContext(t.Context(), "node", "testdata/agent_chat_rendered.cjs")
	cmd.Env = append(os.Environ(), "AGENTNET_SCREENSHOTS="+t.TempDir())
	if assignment {
		cmd.Env = append(cmd.Env, "AGENTNET_ASSIGN_MESSAGE=1")
	}
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), `"ok":true`) {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Logf("%s", out)
}
