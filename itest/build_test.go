package itest

import (
	"os"
	"os/exec"
	"testing"
)

// buildVersion builds agentnet stamped as version, taking releases from base.
func buildVersion(t *testing.T, out, version, base string) []byte {
	t.Helper()
	ld := "-X github.com/misunders2d/agentnet/internal/protocol.Version=" + version + " -X main.releaseBase=" + base
	if b, err := exec.Command("go", "build", "-ldflags", ld, "-o", out, "../cmd/agentnet").CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", version, err, b)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
