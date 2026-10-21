package ui

import (
	"os"
	"os/exec"
	"testing"
)

func TestComicOutboxAttention(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node unavailable")
	}
	if _, err := os.Stat("web/node_modules/esbuild"); err != nil {
		t.Skip("installed frontend compiler required")
	}
	out, err := exec.CommandContext(t.Context(), "node", "testdata/outbox_attention_check.cjs").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Log(string(out))
}
