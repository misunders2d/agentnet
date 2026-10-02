package ui

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

// Opt-in real Chrome installed-registry/standalone journey. The coordinator
// supplies the already frozen product binary; this test never builds one.
func TestPWAInstalledWindowJourney(t *testing.T) {
	if os.Getenv("AGENTNET_PWA_INSTALLED") != "1" {
		t.Skip("opt-in isolated real Chrome PWA journey with frozen binary")
	}
	if runtime.GOOS == "windows" {
		t.Skip("fixture requires bash and loopback TLS proxy")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "internal/ui/testdata/pwa_installed_world.sh")
	cmd.Dir = "../.."
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("installed PWA fixture: %v\n%s", err, out)
	}
	t.Logf("%s", out)
}
