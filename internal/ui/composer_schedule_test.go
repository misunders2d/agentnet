package ui

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestComposerScheduleModel(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node unavailable")
	}
	out, err := exec.CommandContext(t.Context(), "node", "testdata/composer_schedule_check.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Log(string(out))
}

func TestComposerScheduleDialogs(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node unavailable")
	}
	if _, err := os.Stat("web/node_modules/esbuild"); err != nil {
		t.Skip("installed frontend compiler required")
	}
	out, err := exec.CommandContext(t.Context(), "node", "testdata/composer_schedule_ui_check.cjs").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Log(string(out))
}
func TestComposerScheduleRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in: installed Playwright required")
	}
	cmd := exec.CommandContext(t.Context(), "node", "testdata/person_topics_rendered.cjs")
	cmd.Env = append(os.Environ(), "AGENTNET_COMPOSER_SCHEDULE=1")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "schedule comic 1280 PASS") || !strings.Contains(string(out), "schedule comic 390 PASS") {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Log(string(out))
}

func TestComposerScheduleLifetime(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node unavailable")
	}
	out, err := exec.CommandContext(t.Context(), "node", "testdata/composer_schedule_lifetime_check.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Log(string(out))
}
