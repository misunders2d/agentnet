package ui

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestGroupGuestControlsRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in: set AGENTNET_PLAYWRIGHT to installed Playwright")
	}
	cmd := exec.CommandContext(t.Context(), "node", "testdata/group_guest_controls_rendered.cjs")
	cmd.Env = append(os.Environ(), "AGENTNET_SCREENSHOTS="+t.TempDir())
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), `"ok":true`) {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Logf("%s", out)
}

func TestBrowserAppConnectionRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in: set AGENTNET_PLAYWRIGHT to installed Playwright")
	}
	cmd := exec.CommandContext(t.Context(), "node", "testdata/group_guest_controls_rendered.cjs")
	cmd.Env = append(os.Environ(), "AGENTNET_SCREENSHOTS="+t.TempDir(), "AGENTNET_TEST_SKINS=comic,classic,zoom", "AGENTNET_BROWSER_APP_REGRESSION=1")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), `"ok":true`) {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Logf("%s", out)
}

func TestFullApprovalReasonRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in: set AGENTNET_PLAYWRIGHT to installed Playwright")
	}
	cmd := exec.CommandContext(t.Context(), "node", "testdata/group_guest_controls_rendered.cjs")
	cmd.Env = append(os.Environ(), "AGENTNET_SCREENSHOTS="+t.TempDir(), "AGENTNET_TEST_SKINS=comic,classic,zoom", "AGENTNET_FULL_REASON_REGRESSION=1")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), `"ok":true`) {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Logf("%s", out)
}

func TestStoppedSendDeliveryRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in: set AGENTNET_PLAYWRIGHT to installed Playwright")
	}
	cmd := exec.CommandContext(t.Context(), "node", "testdata/group_guest_controls_rendered.cjs")
	cmd.Env = append(os.Environ(), "AGENTNET_SCREENSHOTS="+t.TempDir(), "AGENTNET_TEST_SKINS=comic,classic,zoom", "AGENTNET_DELIVERY_STOP_REGRESSION=1")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), `"ok":true`) {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Logf("%s", out)
}

func TestComicGroupGuestControlsRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in: set AGENTNET_PLAYWRIGHT to installed Playwright")
	}
	cmd := exec.CommandContext(t.Context(), "node", "testdata/group_guest_controls_rendered.cjs")
	cmd.Env = append(os.Environ(), "AGENTNET_SCREENSHOTS="+t.TempDir(), "AGENTNET_TEST_SKINS=comic")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), `"ok":true`) {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Logf("%s", out)
}

func TestComicOKsNavigationRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in: set AGENTNET_PLAYWRIGHT to installed Playwright")
	}
	cmd := exec.CommandContext(t.Context(), "node", "testdata/group_guest_controls_rendered.cjs")
	cmd.Env = append(os.Environ(), "AGENTNET_SCREENSHOTS="+t.TempDir(), "AGENTNET_TEST_SKINS=comic", "AGENTNET_OKS_REGRESSION=1")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), `"ok":true`) {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Logf("%s", out)
}

func TestBundledDirectRunningReviewRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in: set AGENTNET_PLAYWRIGHT to installed Playwright")
	}
	cmd := exec.CommandContext(t.Context(), "node", "testdata/group_guest_controls_rendered.cjs")
	cmd.Env = append(os.Environ(), "AGENTNET_SCREENSHOTS="+t.TempDir(), "AGENTNET_TEST_SKINS=comic,classic,zoom", "AGENTNET_DIRECT_REVIEW_REGRESSION=1")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), `"ok":true`) {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Logf("%s", out)
}

func TestComicNotificationChatMuteRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in: set AGENTNET_PLAYWRIGHT to installed Playwright")
	}
	cmd := exec.CommandContext(t.Context(), "node", "testdata/group_guest_controls_rendered.cjs")
	cmd.Env = append(os.Environ(), "AGENTNET_SCREENSHOTS="+t.TempDir(), "AGENTNET_TEST_SKINS=comic", "AGENTNET_NOTIFY_REGRESSION=1")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), `"ok":true`) {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Logf("%s", out)
}
