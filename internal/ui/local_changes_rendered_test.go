package ui

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestTrustComparisonRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("set AGENTNET_PLAYWRIGHT for rendered trust comparison checks")
	}
	cmd := exec.CommandContext(t.Context(), "node", "testdata/local_changes_rendered.cjs")
	cmd.Env = append(os.Environ(), "AGENTNET_LOCAL_CHANGES_MODE=trust")
	output, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "trust comparison rendered PASS") {
		t.Fatalf("rendered trust comparison: %v\n%s", err, output)
	}
	t.Log(string(output))
}

func TestLocalChangesRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("set AGENTNET_PLAYWRIGHT for rendered local-change checks")
	}
	cmd := exec.CommandContext(t.Context(), "node", "testdata/local_changes_rendered.cjs")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("rendered local changes: %v\n%s", err, output)
	}
	t.Log(string(output))
}

func TestTimelineNavigationRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("set AGENTNET_PLAYWRIGHT for rendered navigation checks")
	}
	output, err := exec.CommandContext(t.Context(), "node", "testdata/timeline_navigation_rendered.cjs").CombinedOutput()
	if err != nil {
		t.Fatalf("rendered timeline navigation: %v\n%s", err, output)
	}
	t.Log(string(output))
}

func TestReadScrollRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("set AGENTNET_PLAYWRIGHT for rendered read and scroll checks")
	}
	output, err := exec.CommandContext(t.Context(), "node", "testdata/read_scroll_rendered.cjs").CombinedOutput()
	if err != nil {
		t.Fatalf("rendered read and scroll: %v\n%s", err, output)
	}
	t.Log(string(output))
}

// MEL-775: a reopened chat's cached copy already sits at the first unread
// (or the latest) message while its fresh copy loads.
func TestChatEntryCachedRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("set AGENTNET_PLAYWRIGHT for rendered chat entry checks")
	}
	output, err := exec.CommandContext(t.Context(), "node", "testdata/chat_entry_cached_rendered.cjs").CombinedOutput()
	if err != nil {
		t.Fatalf("rendered chat entry: %v\n%s", err, output)
	}
	t.Log(string(output))
}
