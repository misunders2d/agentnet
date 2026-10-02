package ui

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

// Production rendering and user controls against an inert provider fixture.
// This proves UI bindings, not encrypted transport or real agent execution.
func TestExternalAgentUIRendered(t *testing.T) {
	if os.Getenv("AGENTNET_EXTERNAL_UI_RENDERED") != "1" {
		t.Skip("opt-in existing Playwright installation")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, node, "testdata/external_agent_ui_rendered.cjs").CombinedOutput()
	if err != nil {
		t.Fatalf("external agent rendered fixture: %v\n%s", err, out)
	}
	t.Logf("%s", out)
}
