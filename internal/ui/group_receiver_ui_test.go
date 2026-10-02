package ui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/ui/static"
)

func TestGroupReplyReceiverComposer(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	out, err := exec.Command(node, "testdata/group_receiver_check.cjs").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "group receiver production functions ok") {
		t.Fatalf("%v\n%s", err, out)
	}
}

// Opt-in real rendering over the owned synthetic provider. The native group
// receiver authority is qualified separately; this exercises the bundled UI.
func TestGroupReplyReceiverRendered(t *testing.T) {
	if os.Getenv("AGENTNET_GROUP_RECEIVER_EVIDENCE") == "" || os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
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
	cmd := exec.Command(node, "testdata/group_receiver_ui_journey.cjs")
	cmd.Env = append(os.Environ(), "AGENTNET_GROUP_RECEIVER_ICON="+icon)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "group receiver rendered UI ok") {
		t.Fatalf("%v\n%s", err, out)
	}
}
