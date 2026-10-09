package ui

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestWorkspaceBrowserContracts(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	out, err := exec.Command(node, "testdata/workspaces_check.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

func TestWorkspaceBrowserNetworkBounds(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	out, err := exec.Command(node, "testdata/workspaces_network_check.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Logf("%s", out)
}

func TestDeviceBootstrapLocalFirst(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	out, err := exec.Command(node, "testdata/device_bootstrap_check.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Logf("%s", out)
}

// BUG-19 on the page: after a disconnect the bar points to Reconnect and
// reconnects that same membership. Opt-in: needs an installed Playwright
// (AGENTNET_PLAYWRIGHT) and Chromium (AGENTNET_CHROMIUM or /usr/bin/chromium).
func TestWorkspaceReconnectRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in: set AGENTNET_PLAYWRIGHT to an installed playwright-core")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(node, "testdata/workspace_reconnect_browser_check.cjs").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "workspace reconnect check PASS") {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Logf("%s", out)
}
