package ui

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Keeps the phase-one inventory executable without making normal Go checks
// install Node packages. The build already owns the locked TypeScript install.
func TestInteractionInventory(t *testing.T) {
	if _, err := os.Stat(filepath.Join("web", "node_modules", "typescript")); err != nil {
		t.Skip("interaction inventory needs internal/ui/web npm ci")
	}
	out, err := exec.Command("node", "testdata/interaction_inventory.cjs").Output()
	if err != nil {
		if failed, ok := err.(*exec.ExitError); ok {
			t.Fatalf("inventory: %v\n%s", err, failed.Stderr)
		}
		t.Fatalf("inventory: %v", err)
	}
	var report struct {
		Schema   string `json:"schema"`
		Controls []struct {
			ID string `json:"definition_id"`
		} `json:"controls"`
	}
	if err := json.Unmarshal(out, &report); err != nil {
		t.Fatalf("inventory JSON: %v", err)
	}
	if report.Schema != "agentnet-interaction-inventory/v1" || len(report.Controls) == 0 || report.Controls[0].ID == "" {
		t.Fatalf("invalid interaction inventory: schema=%q controls=%d", report.Schema, len(report.Controls))
	}
	runNodeCheck(t, "testdata/interaction_inventory_jsx_check.cjs")
}
