package ui

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestBrowserModelSync(t *testing.T) {
	node, e := exec.LookPath("node")
	if e != nil {
		t.Skip("node unavailable")
	}
	out, e := exec.CommandContext(t.Context(), node, "testdata/modelsync_engine_check.mjs").CombinedOutput()
	if e != nil {
		t.Fatalf("%v\n%s", e, out)
	}
	t.Log(string(out))
}

func TestOwnModelStatusBundledSkinsRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in: AGENTNET_PLAYWRIGHT")
	}
	cmd := exec.CommandContext(t.Context(), "node", "testdata/modelstatus_rendered.cjs")
	cmd.Env = append(os.Environ(), "AGENTNET_SCREENSHOTS="+t.TempDir())
	out, e := cmd.CombinedOutput()
	if e != nil || !strings.Contains(string(out), `"ok":true`) {
		t.Fatalf("%v\n%s", e, out)
	}
	t.Log(string(out))
}
