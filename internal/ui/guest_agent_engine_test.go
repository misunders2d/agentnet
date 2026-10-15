package ui

import (
	"os/exec"
	"testing"
)

func TestBrowserGuestOwnAgent(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	out, err := exec.CommandContext(t.Context(), node, "testdata/guest_agent_engine_check.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("browser guest own agent: %v\n%s", err, out)
	}
	t.Log(string(out))
}

func TestBrowserGuestAgentArchive(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	out, err := exec.CommandContext(t.Context(), node, "testdata/guest_agent_archive_check.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("guest archive: %v\n%s", err, out)
	}
	t.Log(string(out))
}
