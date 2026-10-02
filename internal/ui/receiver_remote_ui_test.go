package ui

import (
	"github.com/misunders2d/agentnet/internal/ui/static"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemoteReplyReceiverComposer(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	out, err := exec.Command(node, "testdata/receiver_remote_check.cjs").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "remote receiver production UI functions ok") {
		t.Fatalf("%v\n%s", err, out)
	}
}

// The bundled UI over an owned synthetic provider. Native delegation is a
// separate runtime gate; this fixture verifies real rendering and exact payloads.
func TestRemoteReplyReceiverRendered(t *testing.T) {
	if os.Getenv("AGENTNET_REMOTE_RECEIVER_EVIDENCE") == "" || os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("rendered fixture not selected")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	icon := filepath.Join(t.TempDir(), "icon-192.png")
	if err := os.WriteFile(icon, static.AppIcon(192), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, "testdata/receiver_remote_ui_journey.cjs")
	cmd.Env = append(os.Environ(), "AGENTNET_REMOTE_RECEIVER_ICON="+icon)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "remote receiver rendered UI ok") {
		t.Fatalf("%v\n%s", err, out)
	}
}
