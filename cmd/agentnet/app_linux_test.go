package main

import (
	"os"
	"path/filepath"
	"testing"
)

// A wireless mouse's battery (scope Device) does not make a desktop a
// laptop; a system battery (no scope, or System) does.
func TestAppBatteryIgnoresPeripherals(t *testing.T) {
	root := t.TempDir()
	old := powerSupplies
	powerSupplies = root
	t.Cleanup(func() { powerSupplies = old })
	write := func(dev, kind, scope string) {
		d := filepath.Join(root, dev)
		os.MkdirAll(d, 0o755)
		os.WriteFile(filepath.Join(d, "type"), []byte(kind+"\n"), 0o644)
		if scope != "" {
			os.WriteFile(filepath.Join(d, "scope"), []byte(scope+"\n"), 0o644)
		}
	}
	write("AC", "Mains", "")
	write("hidpp_battery_0", "Battery", "Device")
	if hasBattery() {
		t.Fatal("a mouse battery made a laptop")
	}
	write("BAT0", "Battery", "")
	if !hasBattery() {
		t.Fatal("a system battery not found")
	}
}
