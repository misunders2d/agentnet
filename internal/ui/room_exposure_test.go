package ui

import (
	"os/exec"
	"testing"
)

func TestRoomExposureEmptyHistory(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required for Comic room exposure model checks")
	}
	out, err := exec.CommandContext(t.Context(), node, "testdata/room_exposure_check.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Log(string(out))
}
